package video_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/video"
)

func TestModerationPublishesPlayableVideoAndOwnerCanWithdraw(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	source := generateVideoFixture(t, ctx)
	creatorID := insertVideoCreator(t, ctx, database, "publish_creator", "publish@example.com")
	moderatorID := insertVideoCreator(t, ctx, database, "publish_moderator", "moderator@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "Public video", "moderated publication")
	if err != nil {
		t.Fatalf("CreateDraft(): %v", err)
	}
	finalizeUploadedFile(t, ctx, repository, store, draft, creatorID, source)
	processor := video.NewFFmpegProcessor(store, "ffprobe", "ffmpeg", 30*time.Second, 100<<20)
	worker := video.NewProcessingService(repository, processor, "publish-worker", time.Minute)
	processed, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	publication := video.NewPublicationService(repository, store, 5*time.Minute)
	if _, err := publication.Review(ctx, processed.Video.ID, video.Actor{UserID: creatorID, Role: "member"}, processed.Video.Version, true, "self approval"); !errors.Is(err, video.ErrModerationForbidden) {
		t.Fatalf("member review error = %v, want ErrModerationForbidden", err)
	}
	published, err := publication.Review(ctx, processed.Video.ID, video.Actor{UserID: moderatorID, Role: "moderator"}, processed.Video.Version, true, "policy passed")
	if err != nil || published.State != video.StatePublished || published.PublishedAt == nil {
		t.Fatalf("moderator review = (%+v, %v)", published, err)
	}
	detail, err := publication.GetPublic(ctx, published.ID)
	if err != nil || detail.PlaybackURL == "" || detail.CoverURL == "" {
		t.Fatalf("GetPublic() = (%+v, %v)", detail, err)
	}
	response, err := http.Get(detail.PlaybackURL)
	if err != nil {
		t.Fatalf("GET signed playback: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("signed playback status = %d", response.StatusCode)
	}
	withdrawn, err := publication.Withdraw(ctx, published.ID, video.Actor{UserID: creatorID, Role: "member"}, published.Version, "creator withdrew")
	if err != nil || withdrawn.State != video.StateWithdrawn {
		t.Fatalf("Withdraw() = (%+v, %v)", withdrawn, err)
	}
	if _, err := publication.GetPublic(ctx, published.ID); !errors.Is(err, video.ErrPublicVideoNotFound) {
		t.Fatalf("GetPublic() after withdrawal error = %v, want ErrPublicVideoNotFound", err)
	}
	var transitions int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM video.state_transitions WHERE video_id = $1 AND to_state IN ('published', 'withdrawn')`, published.ID).Scan(&transitions); err != nil {
		t.Fatalf("count publication transitions: %v", err)
	}
	if transitions != 2 {
		t.Fatalf("publication transition audits = %d, want 2", transitions)
	}
}
