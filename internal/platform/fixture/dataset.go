package fixture

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type DatasetStats struct {
	Seed      int64
	Users     int
	Videos    int
	Follows   int
	Likes     int
	Favorites int
	Comments  int
	Danmaku   int
}

// LoadDataset transactionally clears and regenerates a deterministic fixture (users, published videos, source assets, renditions, follows, likes, favorites, comments, danmaku, counters, and a fixture_manifest row), then returns the persisted row counts; it rejects a nil database, non-positive seed, fewer than 20 users, or videos outside 10..users, and rolls back all changes on failure.
func LoadDataset(ctx context.Context, database *sql.DB, seed int64, users, videos int) (DatasetStats, error) {
	if database == nil || seed <= 0 || users < 20 || videos < 10 || videos > users {
		return DatasetStats{}, errors.New("dataset requires a positive seed, at least 20 users, and 10..users videos")
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return DatasetStats{}, fmt.Errorf("begin dataset transaction: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM video.videos WHERE title LIKE 'Load video %'`); err != nil {
		return DatasetStats{}, fmt.Errorf("clear prior dataset videos: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM identity.users WHERE username LIKE 'load_user_%'`); err != nil {
		return DatasetStats{}, fmt.Errorf("clear prior dataset: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO identity.users (username, email, password_hash)
		SELECT 'load_user_' || lpad(value::text, 6, '0'),
		       'load_user_' || lpad(value::text, 6, '0') || '@example.test',
		       '$argon2id$load-fixture'
		FROM generate_series(1, $1) value
	`, users); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset users: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH creators AS (
			SELECT id, row_number() OVER (ORDER BY username) AS sequence
			FROM identity.users WHERE username LIKE 'load_user_%'
			ORDER BY username LIMIT $1
		)
		INSERT INTO video.videos (creator_id, title, description, category, state, version, published_at, created_at, updated_at)
		SELECT id, 'Load video ' || sequence, 'deterministic load dataset',
		       (ARRAY['music','games','technology','animation','knowledge'])[((sequence + $2) % 5) + 1],
		       'published', 4, now() - (sequence * interval '1 minute'),
		       now() - (sequence * interval '1 minute'), now() - (sequence * interval '1 minute')
		FROM creators
	`, videos, seed); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset videos: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO video.source_assets (video_id, object_key, size_bytes, content_type, checksum_sha256, status, finalized_at)
		SELECT id, 'load/sources/' || id || '/source.mp4', 1048576, 'video/mp4', repeat('a', 64), 'verified', published_at
		FROM video.videos WHERE title LIKE 'Load video %'
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset assets: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO video.renditions (source_asset_id, config_version, kind, object_key, status, width, height, completed_at)
		SELECT a.id, 1, kind, 'load/renditions/' || a.id || '/v1/' || kind, 'ready', 1280, 720, v.published_at
		FROM video.source_assets a
		JOIN video.videos v ON v.id = a.video_id
		CROSS JOIN (VALUES ('playback'), ('cover')) kinds(kind)
		WHERE v.title LIKE 'Load video %'
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset renditions: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH ordered AS (
			SELECT id, row_number() OVER (ORDER BY username) AS sequence, count(*) OVER () AS total
			FROM identity.users WHERE username LIKE 'load_user_%'
		)
		INSERT INTO social.follows (follower_id, followee_id)
		SELECT actor.id, target.id
		FROM ordered actor
		CROSS JOIN generate_series(1, 5) distance
		JOIN ordered target ON target.sequence = ((actor.sequence + distance - 1) % actor.total) + 1
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset follows: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH users AS (
			SELECT id, row_number() OVER (ORDER BY username) AS sequence, count(*) OVER () AS total
			FROM identity.users WHERE username LIKE 'load_user_%'
		), videos AS (
			SELECT id, row_number() OVER (ORDER BY published_at, id) AS sequence
			FROM video.videos WHERE title LIKE 'Load video %'
		)
		INSERT INTO social.video_likes (user_id, video_id)
		SELECT users.id, videos.id
		FROM videos CROSS JOIN generate_series(1, 8) offset_value
		JOIN users ON users.sequence = ((videos.sequence * 7 + offset_value - 1) % users.total) + 1
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset likes: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH users AS (
			SELECT id, row_number() OVER (ORDER BY username) AS sequence, count(*) OVER () AS total
			FROM identity.users WHERE username LIKE 'load_user_%'
		), videos AS (
			SELECT id, row_number() OVER (ORDER BY published_at, id) AS sequence
			FROM video.videos WHERE title LIKE 'Load video %'
		)
		INSERT INTO social.video_favorites (user_id, video_id)
		SELECT users.id, videos.id
		FROM videos CROSS JOIN generate_series(1, 3) offset_value
		JOIN users ON users.sequence = ((videos.sequence * 11 + offset_value - 1) % users.total) + 1
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset favorites: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH author AS (
			SELECT id FROM identity.users WHERE username = 'load_user_000001'
		)
		INSERT INTO social.comments (video_id, author_id, body)
		SELECT v.id, author.id, 'deterministic load comment ' || value
		FROM video.videos v CROSS JOIN author CROSS JOIN generate_series(1, 2) value
		WHERE v.title LIKE 'Load video %'
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset comments: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		WITH author AS (
			SELECT id FROM identity.users WHERE username = 'load_user_000002'
		)
		INSERT INTO social.danmaku (video_id, author_id, position_ms, body, created_at)
		SELECT v.id, author.id, value * 1000, 'load danmaku ' || value, v.published_at
		FROM video.videos v CROSS JOIN author CROSS JOIN generate_series(1, 3) value
		WHERE v.title LIKE 'Load video %'
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset danmaku: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO social.video_counters (video_id, likes, favorites, comments, danmaku)
		SELECT v.id,
		       (SELECT count(*) FROM social.video_likes l WHERE l.video_id = v.id),
		       (SELECT count(*) FROM social.video_favorites f WHERE f.video_id = v.id),
		       (SELECT count(*) FROM social.comments c WHERE c.video_id = v.id AND c.deleted_at IS NULL),
		       (SELECT count(*) FROM social.danmaku d WHERE d.video_id = v.id AND d.visible)
		FROM video.videos v WHERE v.title LIKE 'Load video %'
	`); err != nil {
		return DatasetStats{}, fmt.Errorf("generate dataset counters: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO public.fixture_manifest (fixture_name, seed, fixture_version, loaded_at)
		VALUES ('load-dataset', $1, 1, now())
		ON CONFLICT (fixture_name) DO UPDATE
		SET seed = EXCLUDED.seed, fixture_version = EXCLUDED.fixture_version, loaded_at = EXCLUDED.loaded_at
	`, seed); err != nil {
		return DatasetStats{}, fmt.Errorf("record dataset manifest: %w", err)
	}
	stats := DatasetStats{Seed: seed}
	if err := transaction.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM identity.users WHERE username LIKE 'load_user_%'),
			(SELECT count(*) FROM video.videos WHERE title LIKE 'Load video %'),
			(SELECT count(*) FROM social.follows f JOIN identity.users u ON u.id = f.follower_id WHERE u.username LIKE 'load_user_%'),
			(SELECT count(*) FROM social.video_likes l JOIN video.videos v ON v.id = l.video_id WHERE v.title LIKE 'Load video %'),
			(SELECT count(*) FROM social.video_favorites f JOIN video.videos v ON v.id = f.video_id WHERE v.title LIKE 'Load video %'),
			(SELECT count(*) FROM social.comments c JOIN video.videos v ON v.id = c.video_id WHERE v.title LIKE 'Load video %'),
			(SELECT count(*) FROM social.danmaku d JOIN video.videos v ON v.id = d.video_id WHERE v.title LIKE 'Load video %')
	`).Scan(&stats.Users, &stats.Videos, &stats.Follows, &stats.Likes, &stats.Favorites, &stats.Comments, &stats.Danmaku); err != nil {
		return DatasetStats{}, fmt.Errorf("measure generated dataset: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return DatasetStats{}, fmt.Errorf("commit dataset: %w", err)
	}
	return stats, nil
}
