package discovery

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrInvalidFeedRequest = errors.New("invalid feed request")
	ErrInvalidFeedCursor  = errors.New("invalid feed cursor")
)

type FeedItem struct {
	ID          string    `json:"id"`
	CreatorID   string    `json:"creator_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	PublishedAt time.Time `json:"published_at"`
	ReasonCode  string    `json:"reason_code,omitempty"`
}

type FeedPage struct {
	Items      []FeedItem `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
	HasMore    bool       `json:"has_more"`
	Degraded   bool       `json:"degraded,omitempty"`
}

type feedCursor struct {
	PublishedAt time.Time `json:"published_at"`
	ID          string    `json:"id"`
}

type PostgresRepository struct {
	database *sql.DB
	ranking  redis.UniversalClient
}

// WithRanking sets the optional ranking client on the repository in place and returns the same repository for chaining.
func (repository *PostgresRepository) WithRanking(client redis.UniversalClient) *PostgresRepository {
	repository.ranking = client
	return repository
}

// NewPostgresRepository creates a repository backed by database, with ranking disabled until WithRanking is called.
func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	return &PostgresRepository{database: database}
}

// Following returns a cursor-paginated, reverse-chronological feed of published videos from creators the viewer follows, excluding blocked relationships and marking each item as followed_creator.
func (repository *PostgresRepository) Following(ctx context.Context, viewerID, cursor string, limit int) (FeedPage, error) {
	if viewerID == "" || limit <= 0 || limit > 100 {
		return FeedPage{}, ErrInvalidFeedRequest
	}
	var rows *sql.Rows
	var err error
	if cursor == "" {
		rows, err = repository.database.QueryContext(ctx, `
			SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at
			FROM social.follows f
			JOIN video.videos v ON v.creator_id = f.followee_id
			WHERE f.follower_id = $1 AND v.state = 'published'
			  AND NOT EXISTS (
				SELECT 1 FROM social.blocks b
				WHERE (b.blocker_id = $1 AND b.blocked_id = v.creator_id)
				   OR (b.blocker_id = v.creator_id AND b.blocked_id = $1)
			  )
			ORDER BY v.published_at DESC, v.id DESC
			LIMIT $2
		`, viewerID, limit+1)
	} else {
		decoded, decodeErr := decodeFeedCursor(cursor)
		if decodeErr != nil {
			return FeedPage{}, decodeErr
		}
		rows, err = repository.database.QueryContext(ctx, `
			SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at
			FROM social.follows f
			JOIN video.videos v ON v.creator_id = f.followee_id
			WHERE f.follower_id = $1 AND v.state = 'published'
			  AND NOT EXISTS (
				SELECT 1 FROM social.blocks b
				WHERE (b.blocker_id = $1 AND b.blocked_id = v.creator_id)
				   OR (b.blocker_id = v.creator_id AND b.blocked_id = $1)
			  )
			  AND (v.published_at, v.id) < ($2, $3)
			ORDER BY v.published_at DESC, v.id DESC
			LIMIT $4
		`, viewerID, decoded.PublishedAt, decoded.ID, limit+1)
	}
	if err != nil {
		return FeedPage{}, fmt.Errorf("query following feed: %w", err)
	}
	defer rows.Close()
	items, err := scanFeedItems(rows)
	if err != nil {
		return FeedPage{}, err
	}
	for index := range items {
		items[index].ReasonCode = "followed_creator"
	}
	return pageFeed(items, limit), nil
}

// scanFeedItems scans all remaining rows into feed items and wraps scan or iteration errors.
func scanFeedItems(rows *sql.Rows) ([]FeedItem, error) {
	items := make([]FeedItem, 0)
	for rows.Next() {
		var item FeedItem
		if err := rows.Scan(&item.ID, &item.CreatorID, &item.Title, &item.Description, &item.Category, &item.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan feed item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate feed items: %w", err)
	}
	return items, nil
}

// pageFeed trims an over-fetched item slice to limit and, when another page exists, sets HasMore and a cursor derived from the last returned item.
func pageFeed(items []FeedItem, limit int) FeedPage {
	page := FeedPage{Items: items}
	if len(items) > limit {
		page.HasMore = true
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeFeedCursor(feedCursor{PublishedAt: last.PublishedAt, ID: last.ID})
	}
	return page
}

// encodeFeedCursor serializes a feed position as unpadded URL-safe base64; JSON marshaling errors are ignored because feedCursor is always serializable.
func encodeFeedCursor(cursor feedCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeFeedCursor parses an unpadded URL-safe base64 JSON cursor and returns ErrInvalidFeedCursor if decoding fails or either required field is empty.
func decodeFeedCursor(value string) (feedCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return feedCursor{}, ErrInvalidFeedCursor
	}
	var cursor feedCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.PublishedAt.IsZero() || cursor.ID == "" {
		return feedCursor{}, ErrInvalidFeedCursor
	}
	return cursor, nil
}
