package video_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/video"
)

// TestRealFFmpegWorkerCreatesPlayableRenditionAndCover verifies that a worker reclaims an expired processing job, produces two nonempty artifacts, advances the video to review, and emits an H.264-playable rendition.
func TestRealFFmpegWorkerCreatesPlayableRenditionAndCover(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	source := generateVideoFixture(t, ctx)
	creatorID := insertVideoCreator(t, ctx, database, "ffmpeg_creator", "ffmpeg@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "FFmpeg integration", "real binaries and object store")
	if err != nil {
		t.Fatalf("CreateDraft(): %v", err)
	}
	finalized := finalizeUploadedFile(t, ctx, repository, store, draft, creatorID, source)
	abandoned, err := repository.ClaimProcessingJob(ctx, "crashed-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim before simulated crash: %v", err)
	}
	if _, err := repository.StartProcessingJob(ctx, abandoned.ID, "crashed-worker"); err != nil {
		t.Fatalf("start before simulated crash: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE video.processing_jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, finalized.JobID); err != nil {
		t.Fatalf("expire crashed worker lease: %v", err)
	}
	processor := video.NewFFmpegProcessor(store, "ffprobe", "ffmpeg", 30*time.Second, 100<<20)
	worker := video.NewProcessingService(repository, processor, "integration-worker", time.Minute)
	result, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if result.Video.State != video.StateReview || len(result.Renditions) != 2 {
		t.Fatalf("processing result = %+v", result)
	}
	for _, rendition := range result.Renditions {
		inspection, err := store.Inspect(ctx, rendition.ObjectKey, 100<<20)
		if err != nil || inspection.SizeBytes == 0 {
			t.Fatalf("inspect %s rendition: (%+v, %v)", rendition.Kind, inspection, err)
		}
	}
	playback := filepath.Join(t.TempDir(), "playback.mp4")
	if err := store.DownloadFile(ctx, result.Renditions[0].ObjectKey, playback, 100<<20); err != nil {
		t.Fatalf("download playback: %v", err)
	}
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1:nk=1", playback)
	if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("h264")) {
		t.Fatalf("ffprobe rendition = %q, %v", output, err)
	}
}

// TestRealMediaProcessingTimeoutFailsWithinRetryBudget verifies that an FFmpeg deadline is returned as context.DeadlineExceeded and permanently fails a job whose sole attempt is exhausted.
func TestRealMediaProcessingTimeoutFailsWithinRetryBudget(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source := generateVideoFixture(t, ctx)
	creatorID := insertVideoCreator(t, ctx, database, "timeout_creator", "timeout@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "Timeout media", "must stop")
	if err != nil {
		t.Fatalf("CreateDraft(): %v", err)
	}
	finalized := finalizeUploadedFile(t, ctx, repository, store, draft, creatorID, source)
	if _, err := database.ExecContext(ctx, `UPDATE video.processing_jobs SET max_attempts = 1 WHERE id = $1`, finalized.JobID); err != nil {
		t.Fatalf("set retry budget: %v", err)
	}
	processor := video.NewFFmpegProcessor(store, "ffprobe", "ffmpeg", time.Nanosecond, 100<<20)
	worker := video.NewProcessingService(repository, processor, "timeout-worker", time.Minute)
	if _, err := worker.RunOnce(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunOnce() error = %v, want context deadline exceeded", err)
	}
	var state string
	if err := database.QueryRowContext(ctx, `SELECT state FROM video.processing_jobs WHERE id = $1`, finalized.JobID).Scan(&state); err != nil {
		t.Fatalf("read timed out job: %v", err)
	}
	if state != "failed" {
		t.Fatalf("timed out job state = %q, want failed", state)
	}
}

// TestRealFFmpegWorkerRejectsBadMediaAndExhaustsRetryBudget verifies that invalid uploaded media causes processing to error and marks both the job and video failed when no retries remain.
func TestRealFFmpegWorkerRejectsBadMediaAndExhaustsRetryBudget(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	badSource := filepath.Join(t.TempDir(), "bad.mp4")
	if err := os.WriteFile(badSource, []byte("not a media container"), 0o600); err != nil {
		t.Fatalf("write bad source: %v", err)
	}
	creatorID := insertVideoCreator(t, ctx, database, "bad_media_creator", "bad-media@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "Bad media", "must fail")
	if err != nil {
		t.Fatalf("CreateDraft(): %v", err)
	}
	finalized := finalizeUploadedFile(t, ctx, repository, store, draft, creatorID, badSource)
	if _, err := database.ExecContext(ctx, `UPDATE video.processing_jobs SET max_attempts = 1 WHERE id = $1`, finalized.JobID); err != nil {
		t.Fatalf("set retry budget: %v", err)
	}
	processor := video.NewFFmpegProcessor(store, "ffprobe", "ffmpeg", 10*time.Second, 100<<20)
	worker := video.NewProcessingService(repository, processor, "bad-media-worker", time.Minute)
	if _, err := worker.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce() error = nil, want ffprobe failure")
	}
	var jobState, videoState string
	if err := database.QueryRowContext(ctx, `SELECT j.state, v.state FROM video.processing_jobs j JOIN video.source_assets a ON a.id = j.source_asset_id JOIN video.videos v ON v.id = a.video_id WHERE j.id = $1`, finalized.JobID).Scan(&jobState, &videoState); err != nil {
		t.Fatalf("read failed processing state: %v", err)
	}
	if jobState != "failed" || videoState != "failed" {
		t.Fatalf("states = job %q video %q, want failed/failed", jobState, videoState)
	}
}

// generateVideoFixture creates a one-second H.264/yuv420p test video whose aspect ratio exercises even-dimension scaling, failing the test if FFmpeg cannot generate it.
func generateVideoFixture(t *testing.T, ctx context.Context) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mp4")
	// 854x480 scales to 1280x719 when force_original_aspect_ratio is used
	// without an explicit even-dimension constraint. Keep this fixture to guard
	// the H.264/yuv420p requirement that both output dimensions are even.
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=854x480:rate=24", "-t", "1", "-pix_fmt", "yuv420p", "-c:v", "libx264", "-y", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate real video fixture: %s: %v", output, err)
	}
	return path
}

// finalizeUploadedFile uploads the file through a checksum-bound grant, finalizes the draft, and transactionally activates its processing job, failing the test on any setup error.
func finalizeUploadedFile(t *testing.T, ctx context.Context, repository *video.PostgresRepository, store *video.S3ObjectStore, draft video.Video, creatorID, path string) video.FinalizeResult {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read upload fixture: %v", err)
	}
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	uploads := video.NewUploadService(repository, store, 15*time.Minute, 100<<20)
	grant, err := uploads.CreateGrant(ctx, video.UploadRequest{VideoID: draft.ID, CreatorID: creatorID, SizeBytes: int64(len(payload)), ContentType: "video/mp4", ChecksumSHA256: checksum})
	if err != nil {
		t.Fatalf("CreateGrant(): %v", err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, grant.URL, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "video/mp4")
	request.Header.Set("x-amz-meta-sha256", checksum)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("upload fixture: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload fixture status = %d", response.StatusCode)
	}
	result, err := uploads.Finalize(ctx, draft.ID, creatorID)
	if err != nil {
		t.Fatalf("Finalize(): %v", err)
	}
	transaction, err := repositoryDatabaseTransaction(ctx, repository)
	if err != nil {
		t.Fatalf("begin test event activation: %v", err)
	}
	if err := video.ActivateProcessingJobTx(ctx, transaction, result.JobID); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("activate processing event: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit processing activation: %v", err)
	}
	return result
}

// repositoryDatabaseTransaction begins a repository transaction using ctx and returns any begin error unchanged.
func repositoryDatabaseTransaction(ctx context.Context, repository *video.PostgresRepository) (*sql.Tx, error) {
	return repository.BeginTx(ctx)
}
