package video_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/video"
)

// TestProcessingJobLeaseExpiresAndCanBeRecovered verifies that an active lease blocks other workers, an expired lease permits the same job to be reclaimed with an incremented attempt count, and only the current owner can renew it.
func TestProcessingJobLeaseExpiresAndCanBeRecovered(t *testing.T) {
	database := videoTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	insertPendingProcessingJob(t, ctx, database, 3)
	repository := video.NewPostgresRepository(database)

	first, err := repository.ClaimProcessingJob(ctx, "worker-a", time.Minute)
	if err != nil || first.Attempts != 1 || first.LeaseOwner != "worker-a" {
		t.Fatalf("first claim = (%+v, %v)", first, err)
	}
	if _, err := repository.ClaimProcessingJob(ctx, "worker-b", time.Minute); !errors.Is(err, video.ErrNoProcessingJob) {
		t.Fatalf("claim during active lease error = %v, want ErrNoProcessingJob", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE video.processing_jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	recovered, err := repository.ClaimProcessingJob(ctx, "worker-b", time.Minute)
	if err != nil || recovered.ID != first.ID || recovered.Attempts != 2 || recovered.LeaseOwner != "worker-b" {
		t.Fatalf("recovered claim = (%+v, %v)", recovered, err)
	}
	if _, err := repository.RenewProcessingLease(ctx, first.ID, "worker-a", time.Minute); !errors.Is(err, video.ErrProcessingLeaseLost) {
		t.Fatalf("stale renewal error = %v, want ErrProcessingLeaseLost", err)
	}
	renewed, err := repository.RenewProcessingLease(ctx, recovered.ID, "worker-b", 2*time.Minute)
	if err != nil || !renewed.LeaseUntil.After(recovered.LeaseUntil) {
		t.Fatalf("renewed lease = (%+v, %v)", renewed, err)
	}
}

// TestProcessingJobRetriesAreBounded verifies that failed jobs remain claimable until max_attempts is reached, then stay failed and are excluded from subsequent claims.
func TestProcessingJobRetriesAreBounded(t *testing.T) {
	database := videoTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	insertPendingProcessingJob(t, ctx, database, 2)
	repository := video.NewPostgresRepository(database)

	first, err := repository.ClaimProcessingJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := repository.FailProcessingJob(ctx, first.ID, "worker-a", "transient ffmpeg failure", 0); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	second, err := repository.ClaimProcessingJob(ctx, "worker-b", time.Minute)
	if err != nil || second.Attempts != 2 {
		t.Fatalf("second claim = (%+v, %v)", second, err)
	}
	if err := repository.FailProcessingJob(ctx, second.ID, "worker-b", "permanent failure", 0); err != nil {
		t.Fatalf("second failure: %v", err)
	}
	if _, err := repository.ClaimProcessingJob(ctx, "worker-c", time.Minute); !errors.Is(err, video.ErrNoProcessingJob) {
		t.Fatalf("claim after retry budget error = %v, want ErrNoProcessingJob", err)
	}
	var state string
	var attempts int
	if err := database.QueryRowContext(ctx, `SELECT state, attempts FROM video.processing_jobs`).Scan(&state, &attempts); err != nil {
		t.Fatalf("read exhausted job: %v", err)
	}
	if state != "failed" || attempts != 2 {
		t.Fatalf("exhausted job = state %q attempts %d", state, attempts)
	}
}

// insertPendingProcessingJob creates a creator, uploaded video, verified source asset, and pending processing job with the specified retry limit, failing the test on any database error.
func insertPendingProcessingJob(t *testing.T, ctx context.Context, database *sql.DB, maxAttempts int) {
	t.Helper()
	creatorID := insertVideoCreator(t, ctx, database, "lease_creator", "lease@example.com")
	var videoID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.videos (creator_id, title, state, version) VALUES ($1, 'lease test', 'uploaded', 1) RETURNING id::text`, creatorID).Scan(&videoID); err != nil {
		t.Fatalf("create lease video: %v", err)
	}
	var assetID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.source_assets (video_id, object_key, size_bytes, content_type, checksum_sha256, status) VALUES ($1, $2, 10, 'video/mp4', repeat('a', 64), 'verified') RETURNING id::text`, videoID, "sources/"+creatorID+"/"+videoID+"/source").Scan(&assetID); err != nil {
		t.Fatalf("create lease asset: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO video.processing_jobs (source_asset_id, config_version, max_attempts) VALUES ($1, 1, $2)`, assetID, maxAttempts); err != nil {
		t.Fatalf("create processing job: %v", err)
	}
}

// TestStaleQueuedProcessingJobsAreActivated verifies that only queued jobs older than the threshold become pending, activation is idempotent, and the activated job can be claimed.
func TestStaleQueuedProcessingJobsAreActivated(t *testing.T) {
	database := videoTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	staleJobID := insertQueuedProcessingJob(t, ctx, database, "stale_creator", "stale@example.com")
	freshJobID := insertQueuedProcessingJob(t, ctx, database, "fresh_creator", "fresh@example.com")
	if _, err := database.ExecContext(ctx, `UPDATE video.processing_jobs SET updated_at = now() - interval '10 minutes' WHERE id = $1`, staleJobID); err != nil {
		t.Fatalf("age stale job: %v", err)
	}
	repository := video.NewPostgresRepository(database)

	activated, err := repository.ActivateStaleQueuedJobs(ctx, 2*time.Minute)
	if err != nil || activated != 1 {
		t.Fatalf("ActivateStaleQueuedJobs() = (%d, %v), want 1", activated, err)
	}
	var staleState, freshState string
	if err := database.QueryRowContext(ctx, `SELECT state FROM video.processing_jobs WHERE id = $1`, staleJobID).Scan(&staleState); err != nil {
		t.Fatalf("read stale job: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT state FROM video.processing_jobs WHERE id = $1`, freshJobID).Scan(&freshState); err != nil {
		t.Fatalf("read fresh job: %v", err)
	}
	if staleState != "pending" || freshState != "queued" {
		t.Fatalf("job states = (%q, %q), want (pending, queued)", staleState, freshState)
	}
	again, err := repository.ActivateStaleQueuedJobs(ctx, 2*time.Minute)
	if err != nil || again != 0 {
		t.Fatalf("repeat ActivateStaleQueuedJobs() = (%d, %v), want idempotent 0", again, err)
	}
	claimed, err := repository.ClaimProcessingJob(ctx, "worker-a", time.Minute)
	if err != nil || claimed.ID != staleJobID {
		t.Fatalf("ClaimProcessingJob() = (%+v, %v), want activated job %s", claimed, err, staleJobID)
	}
}

// insertQueuedProcessingJob creates a creator, uploaded video, verified source asset, and queued processing job, returning the job ID and failing the test on any database error.
func insertQueuedProcessingJob(t *testing.T, ctx context.Context, database *sql.DB, username, email string) string {
	t.Helper()
	creatorID := insertVideoCreator(t, ctx, database, username, email)
	var videoID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.videos (creator_id, title, state, version) VALUES ($1, 'queued activation test', 'uploaded', 1) RETURNING id::text`, creatorID).Scan(&videoID); err != nil {
		t.Fatalf("create queued video: %v", err)
	}
	var assetID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.source_assets (video_id, object_key, size_bytes, content_type, checksum_sha256, status) VALUES ($1, $2, 10, 'video/mp4', repeat('a', 64), 'verified') RETURNING id::text`, videoID, "sources/"+creatorID+"/"+videoID+"/source").Scan(&assetID); err != nil {
		t.Fatalf("create queued asset: %v", err)
	}
	var jobID string
	if err := database.QueryRowContext(ctx, `INSERT INTO video.processing_jobs (source_asset_id, config_version, state, max_attempts) VALUES ($1, 1, 'queued', 3) RETURNING id::text`, assetID).Scan(&jobID); err != nil {
		t.Fatalf("create queued processing job: %v", err)
	}
	return jobID
}
