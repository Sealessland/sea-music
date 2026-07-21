package discovery

import (
	"context"
	"fmt"
)

// Recommend returns up to limit published videos excluding the viewer's own and mutually blocked creators, ranking by follows, category affinity, popularity, and recency; viewers without follow or like history fall back to cold-start recommendations, and invalid requests return ErrInvalidFeedRequest.
func (repository *PostgresRepository) Recommend(ctx context.Context, viewerID string, limit int) (FeedPage, error) {
	if viewerID == "" || limit <= 0 || limit > 100 {
		return FeedPage{}, ErrInvalidFeedRequest
	}
	var hasHistory bool
	if err := repository.database.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM social.follows WHERE follower_id = $1
			UNION ALL
			SELECT 1 FROM social.video_likes WHERE user_id = $1
		)
	`, viewerID).Scan(&hasHistory); err != nil {
		return FeedPage{}, fmt.Errorf("read recommendation history: %w", err)
	}
	if !hasHistory {
		return repository.coldStart(ctx, viewerID, limit)
	}
	rows, err := repository.database.QueryContext(ctx, `
		WITH category_affinity AS (
			SELECT v.category, count(*) AS affinity
			FROM social.video_likes l
			JOIN video.videos v ON v.id = l.video_id
			WHERE l.user_id = $1
			GROUP BY v.category
		)
		SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at,
		       (f.followee_id IS NOT NULL) AS followed,
		       COALESCE(a.affinity, 0) AS affinity,
		       COALESCE(h.score, 0) AS hot_score
		FROM video.videos v
		LEFT JOIN social.follows f ON f.follower_id = $1 AND f.followee_id = v.creator_id
		LEFT JOIN category_affinity a ON a.category = v.category
		LEFT JOIN discovery.hot_snapshots h ON h.video_id = v.id
		WHERE v.state = 'published' AND v.creator_id <> $1
		  AND NOT EXISTS (
			SELECT 1 FROM social.blocks b
			WHERE (b.blocker_id = $1 AND b.blocked_id = v.creator_id)
			   OR (b.blocker_id = v.creator_id AND b.blocked_id = $1)
		  )
		ORDER BY (f.followee_id IS NOT NULL) DESC, COALESCE(a.affinity, 0) DESC,
		         COALESCE(h.score, 0) DESC, v.published_at DESC, v.id DESC
		LIMIT $2
	`, viewerID, limit)
	if err != nil {
		return FeedPage{}, fmt.Errorf("query recommendations: %w", err)
	}
	defer rows.Close()
	items := make([]FeedItem, 0, limit)
	for rows.Next() {
		var item FeedItem
		var followed bool
		var affinity int64
		var hotScore float64
		if err := rows.Scan(&item.ID, &item.CreatorID, &item.Title, &item.Description, &item.Category, &item.PublishedAt, &followed, &affinity, &hotScore); err != nil {
			return FeedPage{}, fmt.Errorf("scan recommendation: %w", err)
		}
		switch {
		case followed:
			item.ReasonCode = "followed_creator"
		case affinity > 0:
			item.ReasonCode = "category_affinity"
		case hotScore > 0:
			item.ReasonCode = "popular"
		default:
			item.ReasonCode = "fresh"
		}
		items = append(items, item)
	}
	return FeedPage{Items: items}, rows.Err()
}

// coldStart returns a category-diversified selection from the newest published videos, excluding the viewer's own and mutually blocked creators, with each item marked "cold_start_recent".
func (repository *PostgresRepository) coldStart(ctx context.Context, viewerID string, limit int) (FeedPage, error) {
	rows, err := repository.database.QueryContext(ctx, `
		SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at
		FROM video.videos v
		WHERE v.state = 'published' AND v.creator_id <> $1
		  AND NOT EXISTS (
			SELECT 1 FROM social.blocks b
			WHERE (b.blocker_id = $1 AND b.blocked_id = v.creator_id)
			   OR (b.blocker_id = v.creator_id AND b.blocked_id = $1)
		  )
		ORDER BY v.published_at DESC, v.id DESC
		LIMIT $2
	`, viewerID, limit*10)
	if err != nil {
		return FeedPage{}, fmt.Errorf("query cold-start recommendations: %w", err)
	}
	defer rows.Close()
	var candidates []FeedItem
	for rows.Next() {
		var item FeedItem
		if err := rows.Scan(&item.ID, &item.CreatorID, &item.Title, &item.Description, &item.Category, &item.PublishedAt); err != nil {
			return FeedPage{}, err
		}
		item.ReasonCode = "cold_start_recent"
		candidates = append(candidates, item)
	}
	return FeedPage{Items: diversifyCategories(candidates, limit)}, rows.Err()
}

// diversifyCategories round-robins candidates by category in first-seen category order, preserving order within each category and returning at most limit items without modifying the input slice.
func diversifyCategories(candidates []FeedItem, limit int) []FeedItem {
	groups := make(map[string][]FeedItem)
	var order []string
	for _, candidate := range candidates {
		if _, exists := groups[candidate.Category]; !exists {
			order = append(order, candidate.Category)
		}
		groups[candidate.Category] = append(groups[candidate.Category], candidate)
	}
	result := make([]FeedItem, 0, min(limit, len(candidates)))
	for len(result) < limit {
		added := false
		for _, category := range order {
			items := groups[category]
			if len(items) == 0 {
				continue
			}
			result = append(result, items[0])
			groups[category] = items[1:]
			added = true
			if len(result) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return result
}
