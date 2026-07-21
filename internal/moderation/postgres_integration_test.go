package moderation_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/moderation"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

// TestPostgresStorePersistsIdempotentReviewAndCompletedEvidence verifies that PostgreSQL reuses an operation for identical request IDs, rejects conflicting reuse, and persists completed escalation evidence without permitting publication.
func TestPostgresStorePersistsIdempotentReviewAndCompletedEvidence(t *testing.T) {
	database := moderationTestDatabase(t)
	store := moderation.NewPostgresStore(database)
	service := moderation.NewService(store)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request := validRequest()

	first, err := service.StartReview(ctx, request)
	if err != nil {
		t.Fatalf("first StartReview() error = %v", err)
	}
	second, err := service.StartReview(ctx, request)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second StartReview() = (%+v, %v), want operation %q", second, err, first.ID)
	}
	conflict := request
	conflict.Description = "different input with reused request id"
	if _, err := service.StartReview(ctx, conflict); !errors.Is(err, moderation.ErrIdempotencyConflict) {
		t.Fatalf("conflicting StartReview() error = %v, want ErrIdempotencyConflict", err)
	}

	want := moderation.Result{
		Verdict: moderation.VerdictEscalate, Confidence: 0.81, Summary: "human review required",
		Findings: []moderation.Finding{{Code: "uncertain", Category: "context", Score: 0.81}},
		Provider: "manual-fallback", Model: "none", PolicyVersion: request.PolicyVersion,
	}
	if _, err := service.CompleteReview(ctx, first.ID, want); err != nil {
		t.Fatalf("CompleteReview() error = %v", err)
	}
	got, err := service.GetReview(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetReview() error = %v", err)
	}
	if got.Status != moderation.StatusCompleted || got.Result == nil || got.Result.Verdict != want.Verdict || got.Result.CanPublish {
		t.Fatalf("GetReview() = %+v", got)
	}
}

// TestPostgresStoreClaimsOneOperationWithALeaseAndRetriesFailure verifies exclusive leasing, immediate retry after failure, rejection of a stale lease holder, and successful completion by the current claimant.
func TestPostgresStoreClaimsOneOperationWithALeaseAndRetriesFailure(t *testing.T) {
	database := moderationTestDatabase(t)
	store := moderation.NewPostgresStore(database)
	service := moderation.NewService(store)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	operation, err := service.StartReview(ctx, validRequest())
	if err != nil {
		t.Fatalf("StartReview() error = %v", err)
	}

	claimed, err := store.Claim(ctx, "agent-1", time.Minute)
	if err != nil || claimed.ID != operation.ID || claimed.Status != moderation.StatusRunning {
		t.Fatalf("Claim() = (%+v, %v)", claimed, err)
	}
	if _, err := store.Claim(ctx, "agent-2", time.Minute); !errors.Is(err, moderation.ErrNoOperation) {
		t.Fatalf("second Claim() error = %v, want ErrNoOperation", err)
	}
	if err := store.Fail(ctx, operation.ID, "agent-1", errors.New("provider unavailable"), 0); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	retried, err := store.Claim(ctx, "agent-2", time.Minute)
	if err != nil || retried.ID != operation.ID || retried.Status != moderation.StatusRunning {
		t.Fatalf("retry Claim() = (%+v, %v)", retried, err)
	}
	result := moderation.Result{
		Verdict: moderation.VerdictEscalate, Confidence: 0.5, Summary: "manual review",
		Provider: "test", Model: "test", PolicyVersion: operation.Request.PolicyVersion,
	}
	if _, err := store.CompleteClaimed(ctx, operation.ID, "agent-1", result); !errors.Is(err, moderation.ErrLeaseLost) {
		t.Fatalf("stale CompleteClaimed() error = %v, want ErrLeaseLost", err)
	}
	completed, err := store.CompleteClaimed(ctx, operation.ID, "agent-2", result)
	if err != nil || completed.Status != moderation.StatusCompleted {
		t.Fatalf("CompleteClaimed() = (%+v, %v)", completed, err)
	}
}

// TestDispatcherReliablyStartsAndCollectsShadowReview verifies that a queued video review is dispatched in shadow mode with its source URI, collected to completion, and returned to pending with an incremented failure count when a completed response lacks a result.
func TestDispatcherReliablyStartsAndCollectsShadowReview(t *testing.T) {
	database := moderationTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var creatorID, videoID string
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('moderation_dispatch', 'moderation-dispatch@example.com', 'hash') RETURNING id::text`).Scan(&creatorID); err != nil {
		t.Fatalf("create dispatch user: %v", err)
	}
	if err := database.QueryRowContext(ctx, `INSERT INTO video.videos (creator_id, title, description, state, version) VALUES ($1, 'Dispatch review', 'shadow evidence', 'review', 3) RETURNING id::text`, creatorID).Scan(&videoID); err != nil {
		t.Fatalf("create dispatch video: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO video.source_assets (video_id, object_key, size_bytes, content_type, checksum_sha256, status) VALUES ($1, $2, 10, 'video/mp4', repeat('a', 64), 'verified')`, videoID, "sources/"+videoID+"/source"); err != nil {
		t.Fatalf("create dispatch asset: %v", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "01980c55-7c80-7abc-8def-0123456789ab"
	if err := moderation.EnqueueDispatchTx(ctx, transaction, eventID, videoID, 3); err != nil {
		t.Fatalf("EnqueueDispatchTx(): %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	remote := &dispatchRemote{}
	dispatcher := moderation.NewDispatcher(database, remote, "dispatch-1", "sea-music-media", "ugc-v1", moderation.ModeShadow, time.Minute, time.Millisecond)
	job, err := dispatcher.RunOnce(ctx)
	if err != nil || job.OperationID != "01980c55-7c80-7abc-8def-0123456789ac" {
		t.Fatalf("first RunOnce() = (%+v, %v)", job, err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := dispatcher.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce(): %v", err)
	}
	if remote.request.RequestID != eventID || remote.request.Mode != moderation.ModeShadow || remote.request.Assets[0].URI != "s3://sea-music-media/sources/"+videoID+"/source" {
		t.Fatalf("dispatched request = %+v", remote.request)
	}
	var state string
	var verdict string
	if err := database.QueryRowContext(ctx, `SELECT state, result->>'verdict' FROM moderation.dispatch_jobs WHERE event_id = $1`, eventID).Scan(&state, &verdict); err != nil {
		t.Fatalf("read dispatch result: %v", err)
	}
	if state != "completed" || verdict != string(moderation.VerdictEscalate) {
		t.Fatalf("dispatch result = (%q, %q)", state, verdict)
	}

	badEventID := "01980c55-7c80-7abc-8def-0123456789ad"
	transaction, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := moderation.EnqueueDispatchTx(ctx, transaction, badEventID, videoID, 3); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	badDispatcher := moderation.NewDispatcher(database, badCompletedRemote{}, "dispatch-2", "sea-music-media", "ugc-v1", moderation.ModeShadow, time.Minute, time.Millisecond)
	if _, err := badDispatcher.RunOnce(ctx); err == nil {
		t.Fatal("invalid completed operation did not fail")
	}
	var failures int
	if err := database.QueryRowContext(ctx, `SELECT state, failures FROM moderation.dispatch_jobs WHERE event_id = $1`, badEventID).Scan(&state, &failures); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || failures != 1 {
		t.Fatalf("invalid response retry state = %q failures = %d", state, failures)
	}
}

type dispatchRemote struct {
	request moderation.ReviewRequest
}

// StartReview records the dispatched request and returns a fixed pending operation for dispatcher integration testing.
func (remote *dispatchRemote) StartReview(_ context.Context, request moderation.ReviewRequest) (moderation.Operation, error) {
	remote.request = request
	return moderation.Operation{ID: "01980c55-7c80-7abc-8def-0123456789ac", Status: moderation.StatusPending}, nil
}

// GetReview returns the fixed operation as completed with manual-fallback escalation evidence.
func (remote *dispatchRemote) GetReview(context.Context, string) (moderation.Operation, error) {
	return moderation.Operation{ID: "01980c55-7c80-7abc-8def-0123456789ac", Status: moderation.StatusCompleted, Result: &moderation.Result{
		Verdict: moderation.VerdictEscalate, Confidence: 0, Summary: "manual review", Provider: "manual-fallback", Model: "none", PolicyVersion: "ugc-v1",
	}}, nil
}

type badCompletedRemote struct{}

// StartReview returns a fixed completed operation without a result to exercise invalid-response retry handling.
func (badCompletedRemote) StartReview(context.Context, moderation.ReviewRequest) (moderation.Operation, error) {
	return moderation.Operation{ID: "01980c55-7c80-7abc-8def-0123456789ae", Status: moderation.StatusCompleted}, nil
}

// GetReview always returns an error because the invalid completed operation should fail validation before polling.
func (badCompletedRemote) GetReview(context.Context, string) (moderation.Operation, error) {
	return moderation.Operation{}, errors.New("not used")
}

// moderationTestDatabase opens the database named by SEA_MODERATION_TEST_DATABASE_URL, skips when unset, applies bundled migrations, truncates test tables, and registers timeout and close cleanup.
func moderationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SEA_MODERATION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_MODERATION_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open moderation database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	migrations, err := migrate.Bundled()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := migrate.Apply(ctx, database, migrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE moderation.dispatch_jobs, moderation.review_operations, video.state_transitions, video.processing_jobs, video.renditions, video.source_assets, video.videos, identity.sessions, identity.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate moderation operations: %v", err)
	}
	return database
}
