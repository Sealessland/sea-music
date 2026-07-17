package social_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/social"
)

func TestLikeFavoriteAndCommentRequirePublishedVideo(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID, creatorID, publishedID := insertSocialFixture(t, ctx, database)
	draftID := insertVideoWithState(t, ctx, database, creatorID, "draft")
	repository := socialRepository(database)
	if _, err := repository.SetLike(ctx, userID, draftID, true); !errors.Is(err, social.ErrInvalidRelation) {
		t.Fatalf("SetLike(draft) error = %v, want ErrInvalidRelation", err)
	}
	if _, err := repository.SetFavorite(ctx, userID, draftID, true); !errors.Is(err, social.ErrInvalidRelation) {
		t.Fatalf("SetFavorite(draft) error = %v, want ErrInvalidRelation", err)
	}
	if _, err := repository.CreateComment(ctx, userID, draftID, "", "on draft"); !errors.Is(err, social.ErrInvalidComment) {
		t.Fatalf("CreateComment(draft) error = %v, want ErrInvalidComment", err)
	}
	if result, err := repository.SetLike(ctx, userID, publishedID, true); err != nil || !result.Changed || !result.Exists {
		t.Fatalf("SetLike(published) = (%+v, %v)", result, err)
	}
	if result, err := repository.SetFavorite(ctx, userID, publishedID, true); err != nil || !result.Changed || !result.Exists {
		t.Fatalf("SetFavorite(published) = (%+v, %v)", result, err)
	}
	if _, err := repository.CreateComment(ctx, userID, publishedID, "", "on published"); err != nil {
		t.Fatalf("CreateComment(published): %v", err)
	}
	var likes, favorites, comments, eventsCount int
	if err := database.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM social.video_likes),
			(SELECT count(*) FROM social.video_favorites),
			(SELECT count(*) FROM social.comments),
			(SELECT count(*) FROM eventing.outbox)
	`).Scan(&likes, &favorites, &comments, &eventsCount); err != nil {
		t.Fatalf("count visibility-gated writes: %v", err)
	}
	if likes != 1 || favorites != 1 || comments != 1 || eventsCount != 3 {
		t.Fatalf("visibility-gated writes = likes %d favorites %d comments %d events %d, want 1/1/1/3", likes, favorites, comments, eventsCount)
	}
}

func TestUnlikeAndUnfavoriteSucceedAfterVideoLeavesPublishedState(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID, _, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	if result, err := repository.SetLike(ctx, userID, videoID, true); err != nil || !result.Changed {
		t.Fatalf("SetLike(enable) = (%+v, %v)", result, err)
	}
	if result, err := repository.SetFavorite(ctx, userID, videoID, true); err != nil || !result.Changed {
		t.Fatalf("SetFavorite(enable) = (%+v, %v)", result, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE video.videos SET state = 'withdrawn' WHERE id = $1`, videoID); err != nil {
		t.Fatalf("withdraw video: %v", err)
	}
	if result, err := repository.SetLike(ctx, userID, videoID, false); err != nil || !result.Changed || result.Exists {
		t.Fatalf("SetLike(disable, withdrawn) = (%+v, %v)", result, err)
	}
	if result, err := repository.SetFavorite(ctx, userID, videoID, false); err != nil || !result.Changed || result.Exists {
		t.Fatalf("SetFavorite(disable, withdrawn) = (%+v, %v)", result, err)
	}
	var relations int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM social.video_likes) + (SELECT count(*) FROM social.video_favorites)`).Scan(&relations); err != nil {
		t.Fatalf("count relations after cleanup: %v", err)
	}
	if relations != 0 {
		t.Fatalf("relations after cleanup = %d, want 0", relations)
	}
}

func insertVideoWithState(t *testing.T, ctx context.Context, database *sql.DB, creatorID, state string) string {
	t.Helper()
	var videoID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.videos (creator_id, title, state) VALUES ($1, 'visibility video', $2) RETURNING id::text`, creatorID, state).Scan(&videoID); err != nil {
		t.Fatalf("insert %s video: %v", state, err)
	}
	return videoID
}
