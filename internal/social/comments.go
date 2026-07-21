package social

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidComment       = errors.New("invalid comment")
	ErrInvalidCommentParent = errors.New("comments support one reply level")
	ErrCommentNotFound      = errors.New("comment not found")
	ErrCommentForbidden     = errors.New("comment deletion forbidden")
	ErrInvalidCursor        = errors.New("invalid comment cursor")
)

type Actor struct {
	UserID string
	Role   string
}

type Comment struct {
	ID        string    `json:"id"`
	VideoID   string    `json:"video_id"`
	AuthorID  string    `json:"author_id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Body      string    `json:"body"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"created_at"`
	Replies   []Comment `json:"replies,omitempty"`
}

type CommentPage struct {
	Items      []Comment `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
}

type commentCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

// CreateComment validates and persists a trimmed comment on a published video, optionally as a direct reply to an existing undeleted top-level comment, and atomically enqueues a creation event when an outbox is available.
func (repository *PostgresRepository) CreateComment(ctx context.Context, authorID, videoID, parentID, body string) (Comment, error) {
	body = strings.TrimSpace(body)
	if authorID == "" || videoID == "" || body == "" || len([]rune(body)) > 1000 {
		return Comment{}, ErrInvalidComment
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, fmt.Errorf("begin comment creation: %w", err)
	}
	defer transaction.Rollback()
	var published bool
	if err := transaction.QueryRowContext(ctx, `SELECT state = 'published' FROM video.videos WHERE id = $1`, videoID).Scan(&published); err != nil || !published {
		return Comment{}, ErrInvalidComment
	}
	if parentID != "" {
		var parentVideo string
		var grandparent sql.NullString
		var deletedAt sql.NullTime
		err := transaction.QueryRowContext(ctx, `SELECT video_id::text, parent_id::text, deleted_at FROM social.comments WHERE id = $1 FOR UPDATE`, parentID).Scan(&parentVideo, &grandparent, &deletedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Comment{}, ErrInvalidCommentParent
		}
		if err != nil {
			return Comment{}, fmt.Errorf("read comment parent: %w", err)
		}
		if grandparent.Valid || parentVideo != videoID || deletedAt.Valid {
			return Comment{}, ErrInvalidCommentParent
		}
	}
	var comment Comment
	var scannedParent sql.NullString
	err = transaction.QueryRowContext(ctx, `
		INSERT INTO social.comments (video_id, author_id, parent_id, body)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4)
		RETURNING id::text, video_id::text, author_id::text, parent_id::text, body, created_at
	`, videoID, authorID, parentID, body).Scan(
		&comment.ID, &comment.VideoID, &comment.AuthorID, &scannedParent, &comment.Body, &comment.CreatedAt,
	)
	if err != nil {
		return Comment{}, fmt.Errorf("create comment: %w", err)
	}
	if scannedParent.Valid {
		comment.ParentID = scannedParent.String
	}
	if repository.outbox != nil {
		payload, _ := json.Marshal(map[string]any{"comment_id": comment.ID, "video_id": videoID, "author_id": authorID, "parent_id": parentID})
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "social.comment.created", Version: 1,
			AggregateType: "comment", AggregateID: comment.ID, AggregateVersion: 1, Data: payload,
		}); err != nil {
			return Comment{}, fmt.Errorf("enqueue comment creation: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return Comment{}, fmt.Errorf("commit comment creation: %w", err)
	}
	return comment, nil
}

// DeleteComment atomically soft-deletes a comment for its author, the video's creator, a moderator, or an admin, and enqueues a deletion event when available; repeated deletion is a no-op.
func (repository *PostgresRepository) DeleteComment(ctx context.Context, commentID string, actor Actor) error {
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin comment deletion: %w", err)
	}
	defer transaction.Rollback()
	var videoID, authorID, creatorID string
	var deletedAt sql.NullTime
	err = transaction.QueryRowContext(ctx, `
		SELECT c.video_id::text, c.author_id::text, v.creator_id::text, c.deleted_at
		FROM social.comments c JOIN video.videos v ON v.id = c.video_id
		WHERE c.id = $1 FOR UPDATE OF c
	`, commentID).Scan(&videoID, &authorID, &creatorID, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCommentNotFound
	}
	if err != nil {
		return fmt.Errorf("lock comment deletion: %w", err)
	}
	if actor.UserID == "" || (actor.UserID != authorID && actor.UserID != creatorID && actor.Role != "moderator" && actor.Role != "admin") {
		return ErrCommentForbidden
	}
	if deletedAt.Valid {
		return nil
	}
	now := time.Now().UTC()
	if _, err := transaction.ExecContext(ctx, `UPDATE social.comments SET body = '', deleted_at = $2, deleted_by = $3 WHERE id = $1`, commentID, now, actor.UserID); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	if repository.outbox != nil {
		payload, _ := json.Marshal(map[string]any{"comment_id": commentID, "video_id": videoID, "deleted_by": actor.UserID})
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "social.comment.deleted", Version: 1,
			AggregateType: "comment", AggregateID: commentID, AggregateVersion: 2, Data: payload,
		}); err != nil {
			return fmt.Errorf("enqueue comment deletion: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit comment deletion: %w", err)
	}
	return nil
}

// ListComments returns up to limit top-level comments for a video in reverse chronological order, including at most 100 oldest-first replies per comment and a cursor when more results exist.
func (repository *PostgresRepository) ListComments(ctx context.Context, videoID, cursor string, limit int) (CommentPage, error) {
	if videoID == "" || limit <= 0 || limit > 100 {
		return CommentPage{}, ErrInvalidComment
	}
	var rows *sql.Rows
	var err error
	if cursor == "" {
		rows, err = repository.database.QueryContext(ctx, `
			SELECT id::text, video_id::text, author_id::text, body, deleted_at, created_at
			FROM social.comments
			WHERE video_id = $1 AND parent_id IS NULL
			ORDER BY created_at DESC, id DESC LIMIT $2
		`, videoID, limit+1)
	} else {
		decoded, decodeErr := decodeCommentCursor(cursor)
		if decodeErr != nil {
			return CommentPage{}, decodeErr
		}
		rows, err = repository.database.QueryContext(ctx, `
			SELECT id::text, video_id::text, author_id::text, body, deleted_at, created_at
			FROM social.comments
			WHERE video_id = $1 AND parent_id IS NULL AND (created_at, id) < ($2, $3)
			ORDER BY created_at DESC, id DESC LIMIT $4
		`, videoID, decoded.CreatedAt, decoded.ID, limit+1)
	}
	if err != nil {
		return CommentPage{}, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()
	items := make([]Comment, 0, limit+1)
	for rows.Next() {
		var comment Comment
		var deletedAt sql.NullTime
		if err := rows.Scan(&comment.ID, &comment.VideoID, &comment.AuthorID, &comment.Body, &deletedAt, &comment.CreatedAt); err != nil {
			return CommentPage{}, fmt.Errorf("scan comment: %w", err)
		}
		comment.Deleted = deletedAt.Valid
		replies, err := repository.listReplies(ctx, comment.ID)
		if err != nil {
			return CommentPage{}, err
		}
		comment.Replies = replies
		items = append(items, comment)
	}
	if err := rows.Err(); err != nil {
		return CommentPage{}, fmt.Errorf("iterate comments: %w", err)
	}
	page := CommentPage{Items: items}
	if len(items) > limit {
		page.HasMore = true
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCommentCursor(commentCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

// listReplies returns at most 100 replies to a parent comment in chronological order, marking soft-deleted replies and setting their ParentID.
func (repository *PostgresRepository) listReplies(ctx context.Context, parentID string) ([]Comment, error) {
	rows, err := repository.database.QueryContext(ctx, `
		SELECT id::text, video_id::text, author_id::text, body, deleted_at, created_at
		FROM social.comments WHERE parent_id = $1 ORDER BY created_at, id LIMIT 100
	`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list comment replies: %w", err)
	}
	defer rows.Close()
	replies := make([]Comment, 0)
	for rows.Next() {
		var reply Comment
		var deletedAt sql.NullTime
		if err := rows.Scan(&reply.ID, &reply.VideoID, &reply.AuthorID, &reply.Body, &deletedAt, &reply.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment reply: %w", err)
		}
		reply.ParentID = parentID
		reply.Deleted = deletedAt.Valid
		replies = append(replies, reply)
	}
	return replies, rows.Err()
}

// encodeCommentCursor serializes a comment's creation time and ID as unpadded URL-safe base64 JSON for keyset pagination.
func encodeCommentCursor(cursor commentCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// decodeCommentCursor parses an unpadded URL-safe base64 JSON cursor and returns ErrInvalidCursor unless both its creation time and ID are present.
func decodeCommentCursor(value string) (commentCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return commentCursor{}, ErrInvalidCursor
	}
	var cursor commentCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.ID == "" {
		return commentCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}
