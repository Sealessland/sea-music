package video_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/video"
)

func TestDirectUploadIsVerifiedAndFinalizedExactlyOnce(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creatorID := insertVideoCreator(t, ctx, database, "upload_creator", "upload@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "Direct upload", "real S3 integration")
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	payload := []byte("real source media bytes")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	service := video.NewUploadService(repository, store, 15*time.Minute, 1024)
	grant, err := service.CreateGrant(ctx, video.UploadRequest{
		VideoID: draft.ID, CreatorID: creatorID, SizeBytes: int64(len(payload)),
		ContentType: "video/mp4", ChecksumSHA256: checksum,
	})
	if err != nil {
		t.Fatalf("CreateGrant() error = %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, grant.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create PUT request: %v", err)
	}
	request.Header.Set("Content-Type", "video/mp4")
	request.Header.Set("x-amz-meta-sha256", checksum)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT signed object: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT status = %d: %s", response.StatusCode, body)
	}
	first, err := service.Finalize(ctx, draft.ID, creatorID)
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	second, err := service.Finalize(ctx, draft.ID, creatorID)
	if err != nil {
		t.Fatalf("second Finalize() error = %v", err)
	}
	if first.AssetID != second.AssetID || first.JobID != second.JobID || first.Video.State != video.StateUploaded {
		t.Fatalf("finalize results are not idempotent: first=%+v second=%+v", first, second)
	}
	var jobs int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM video.processing_jobs WHERE source_asset_id = $1`, first.AssetID).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("processing jobs = %d, want 1", jobs)
	}
}

func TestFinalizeRejectsObjectWithInvalidChecksum(t *testing.T) {
	database := videoTestDatabase(t)
	store := videoTestObjectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creatorID := insertVideoCreator(t, ctx, database, "bad_upload_creator", "bad-upload@example.com")
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "Invalid upload", "must reject")
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	payload := []byte("source bytes")
	expected := sha256.Sum256(payload)
	service := video.NewUploadService(repository, store, 15*time.Minute, 1024)
	grant, err := service.CreateGrant(ctx, video.UploadRequest{
		VideoID: draft.ID, CreatorID: creatorID, SizeBytes: int64(len(payload)),
		ContentType: "video/mp4", ChecksumSHA256: hex.EncodeToString(expected[:]),
	})
	if err != nil {
		t.Fatalf("CreateGrant() error = %v", err)
	}
	invalidPayload := []byte("tamper bytes")
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, grant.URL, bytes.NewReader(invalidPayload))
	request.Header.Set("Content-Type", "video/mp4")
	request.Header.Set("x-amz-meta-sha256", hex.EncodeToString(expected[:]))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT signed object: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", response.StatusCode)
	}
	if _, err := service.Finalize(ctx, draft.ID, creatorID); err == nil {
		t.Fatal("Finalize() error = nil, want object validation error")
	}
	var jobs int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM video.processing_jobs`).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 0 {
		t.Fatalf("processing jobs = %d, want 0", jobs)
	}
}

func videoTestObjectStore(t *testing.T) *video.S3ObjectStore {
	t.Helper()
	endpoint := os.Getenv("SEA_VIDEO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SEA_VIDEO_TEST_S3_ENDPOINT is required for S3 integration tests")
	}
	store, err := video.NewS3ObjectStore(context.Background(), video.S3Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "sea-music-media",
		AccessKey: "sea-music-local", SecretKey: "local-object-store-password",
	})
	if err != nil {
		t.Fatalf("NewS3ObjectStore() error = %v", err)
	}
	return store
}

func insertVideoCreator(t *testing.T, ctx context.Context, database *sql.DB, username, email string) string {
	t.Helper()
	var creatorID string
	if err := database.QueryRowContext(ctx, `
		INSERT INTO identity.users (username, email, password_hash)
		VALUES ($1, $2, '$argon2id$fixture') RETURNING id::text
	`, username, email).Scan(&creatorID); err != nil {
		t.Fatalf("create video test user: %v", err)
	}
	return creatorID
}
