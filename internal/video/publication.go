package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrModerationForbidden = errors.New("moderation permission required")
	ErrPublicVideoNotFound = errors.New("public video not found")
)

type Actor struct {
	UserID string
	Role   string
}

type PublicVideo struct {
	ID           string    `json:"id"`
	CreatorID    string    `json:"creator_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	PublishedAt  time.Time `json:"published_at"`
	PlaybackURL  string    `json:"playback_url"`
	CoverURL     string    `json:"cover_url"`
	URLExpiresAt time.Time `json:"url_expires_at"`
}

type publicVideoRecord struct {
	PublicVideo
	PlaybackKey string
	CoverKey    string
}

type PublicationService struct {
	repository *PostgresRepository
	store      *S3ObjectStore
	urlTTL     time.Duration
}

func NewPublicationService(repository *PostgresRepository, store *S3ObjectStore, urlTTL time.Duration) *PublicationService {
	return &PublicationService{repository: repository, store: store, urlTTL: urlTTL}
}

func (service *PublicationService) Review(ctx context.Context, videoID string, actor Actor, expectedVersion int64, approved bool, reason string) (Video, error) {
	if actor.Role != "moderator" && actor.Role != "admin" {
		return Video{}, ErrModerationForbidden
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Video{}, errors.New("moderation reason is required")
	}
	target := StateFailed
	if approved {
		target = StatePublished
	}
	return service.repository.Transition(ctx, videoID, actor.UserID, expectedVersion, target, reason)
}

func (service *PublicationService) Withdraw(ctx context.Context, videoID string, actor Actor, expectedVersion int64, reason string) (Video, error) {
	current, err := service.repository.Get(ctx, videoID)
	if err != nil {
		return Video{}, err
	}
	if actor.UserID == "" || (actor.UserID != current.CreatorID && actor.Role != "moderator" && actor.Role != "admin") {
		return Video{}, ErrUploadForbidden
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Video{}, errors.New("withdrawal reason is required")
	}
	return service.repository.Transition(ctx, videoID, actor.UserID, expectedVersion, StateWithdrawn, reason)
}

func (service *PublicationService) GetPublic(ctx context.Context, videoID string) (PublicVideo, error) {
	record, err := service.repository.getPublicVideo(ctx, videoID)
	if err != nil {
		return PublicVideo{}, err
	}
	playbackURL, expiresAt, err := service.store.PresignDownload(ctx, record.PlaybackKey, service.urlTTL)
	if err != nil {
		return PublicVideo{}, err
	}
	coverURL, _, err := service.store.PresignDownload(ctx, record.CoverKey, service.urlTTL)
	if err != nil {
		return PublicVideo{}, err
	}
	record.PlaybackURL = playbackURL
	record.CoverURL = coverURL
	record.URLExpiresAt = expiresAt
	return record.PublicVideo, nil
}

func (repository *PostgresRepository) getPublicVideo(ctx context.Context, videoID string) (publicVideoRecord, error) {
	var record publicVideoRecord
	err := repository.database.QueryRowContext(ctx, `
		SELECT v.id::text, v.creator_id::text, v.title, v.description, v.published_at,
		       max(r.object_key) FILTER (WHERE r.kind = 'playback'),
		       max(r.object_key) FILTER (WHERE r.kind = 'cover')
		FROM video.videos v
		JOIN video.source_assets a ON a.video_id = v.id AND a.status = 'verified'
		JOIN video.renditions r ON r.source_asset_id = a.id AND r.status = 'ready'
		WHERE v.id = $1 AND v.state = 'published'
		GROUP BY v.id
		HAVING count(*) FILTER (WHERE r.kind = 'playback') > 0
		   AND count(*) FILTER (WHERE r.kind = 'cover') > 0
	`, videoID).Scan(
		&record.ID, &record.CreatorID, &record.Title, &record.Description, &record.PublishedAt,
		&record.PlaybackKey, &record.CoverKey,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return publicVideoRecord{}, ErrPublicVideoNotFound
	}
	if err != nil {
		return publicVideoRecord{}, fmt.Errorf("get public video: %w", err)
	}
	return record, nil
}
