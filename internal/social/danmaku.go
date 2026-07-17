package social

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode"
)

var (
	ErrInvalidDanmaku     = errors.New("invalid danmaku")
	ErrDanmakuRateLimited = errors.New("danmaku rate limit exceeded")
)

type Danmaku struct {
	ID         string    `json:"id"`
	VideoID    string    `json:"video_id"`
	AuthorID   string    `json:"author_id"`
	PositionMS int       `json:"position_ms"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type DanmakuPage struct {
	Items      []Danmaku `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
}

type danmakuCursor struct {
	PositionMS int    `json:"position_ms"`
	ID         string `json:"id"`
}

func (repository *PostgresRepository) CreateDanmaku(ctx context.Context, authorID, videoID string, positionMS int, body string) (Danmaku, error) {
	body, err := sanitizeDanmaku(body)
	if err != nil || authorID == "" || videoID == "" || positionMS < 0 || positionMS > 43_200_000 {
		return Danmaku{}, ErrInvalidDanmaku
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return Danmaku{}, fmt.Errorf("begin danmaku creation: %w", err)
	}
	defer transaction.Rollback()
	var published bool
	if err := transaction.QueryRowContext(ctx, `SELECT state = 'published' FROM video.videos WHERE id = $1`, videoID).Scan(&published); err != nil || !published {
		return Danmaku{}, ErrInvalidDanmaku
	}
	if _, err := transaction.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, authorID); err != nil {
		return Danmaku{}, fmt.Errorf("lock danmaku rate key: %w", err)
	}
	var recent int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM social.danmaku WHERE author_id = $1 AND created_at > now() - interval '10 seconds'`, authorID).Scan(&recent); err != nil {
		return Danmaku{}, fmt.Errorf("count recent danmaku: %w", err)
	}
	if recent >= 5 {
		return Danmaku{}, ErrDanmakuRateLimited
	}
	var message Danmaku
	err = transaction.QueryRowContext(ctx, `
		INSERT INTO social.danmaku (video_id, author_id, position_ms, body)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, video_id::text, author_id::text, position_ms, body, created_at
	`, videoID, authorID, positionMS, body).Scan(
		&message.ID, &message.VideoID, &message.AuthorID, &message.PositionMS, &message.Body, &message.CreatedAt,
	)
	if err != nil {
		return Danmaku{}, fmt.Errorf("create danmaku: %w", err)
	}
	if repository.outbox != nil {
		payload, _ := json.Marshal(map[string]any{"danmaku_id": message.ID, "video_id": videoID, "author_id": authorID, "position_ms": positionMS})
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "social.danmaku.created", Version: 1,
			AggregateType: "danmaku", AggregateID: message.ID, AggregateVersion: 1, Data: payload,
		}); err != nil {
			return Danmaku{}, fmt.Errorf("enqueue danmaku creation: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return Danmaku{}, fmt.Errorf("commit danmaku creation: %w", err)
	}
	return message, nil
}

func (repository *PostgresRepository) ListDanmaku(ctx context.Context, videoID string, startMS, endMS int, cursor string, limit int) (DanmakuPage, error) {
	if videoID == "" || startMS < 0 || endMS <= startMS || endMS-startMS > 300_000 || limit <= 0 || limit > 500 {
		return DanmakuPage{}, ErrInvalidDanmaku
	}
	var rows *sql.Rows
	var err error
	if cursor == "" {
		rows, err = repository.database.QueryContext(ctx, `
			SELECT id::text, video_id::text, author_id::text, position_ms, body, created_at
			FROM social.danmaku
			WHERE video_id = $1 AND visible AND position_ms >= $2 AND position_ms < $3
			ORDER BY position_ms, id LIMIT $4
		`, videoID, startMS, endMS, limit+1)
	} else {
		decoded, decodeErr := decodeDanmakuCursor(cursor)
		if decodeErr != nil {
			return DanmakuPage{}, decodeErr
		}
		rows, err = repository.database.QueryContext(ctx, `
			SELECT id::text, video_id::text, author_id::text, position_ms, body, created_at
			FROM social.danmaku
			WHERE video_id = $1 AND visible AND position_ms >= $2 AND position_ms < $3
			  AND (position_ms, id) > ($4, $5)
			ORDER BY position_ms, id LIMIT $6
		`, videoID, startMS, endMS, decoded.PositionMS, decoded.ID, limit+1)
	}
	if err != nil {
		return DanmakuPage{}, fmt.Errorf("list danmaku: %w", err)
	}
	defer rows.Close()
	items := make([]Danmaku, 0, limit+1)
	for rows.Next() {
		var message Danmaku
		if err := rows.Scan(&message.ID, &message.VideoID, &message.AuthorID, &message.PositionMS, &message.Body, &message.CreatedAt); err != nil {
			return DanmakuPage{}, fmt.Errorf("scan danmaku: %w", err)
		}
		items = append(items, message)
	}
	if err := rows.Err(); err != nil {
		return DanmakuPage{}, fmt.Errorf("iterate danmaku: %w", err)
	}
	page := DanmakuPage{Items: items}
	if len(items) > limit {
		page.HasMore = true
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeDanmakuCursor(danmakuCursor{PositionMS: last.PositionMS, ID: last.ID})
	}
	return page, nil
}

func sanitizeDanmaku(value string) (string, error) {
	for _, character := range value {
		if unicode.IsControl(character) && !unicode.IsSpace(character) {
			return "", ErrInvalidDanmaku
		}
	}
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || len([]rune(value)) > 100 {
		return "", ErrInvalidDanmaku
	}
	return html.EscapeString(value), nil
}

func encodeDanmakuCursor(cursor danmakuCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeDanmakuCursor(value string) (danmakuCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return danmakuCursor{}, ErrInvalidCursor
	}
	var cursor danmakuCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.PositionMS < 0 || cursor.ID == "" {
		return danmakuCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}
