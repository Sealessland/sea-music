package video_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/video"
)

// TestPresignedDownloadCacheIsBounded confirms that presigning more than 10,000 distinct downloads against the configured S3 endpoint keeps the download URL cache at or below its 10,000-entry limit.
func TestPresignedDownloadCacheIsBounded(t *testing.T) {
	endpoint := os.Getenv("SEA_VIDEO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SEA_VIDEO_TEST_S3_ENDPOINT is required")
	}
	store, err := video.NewS3ObjectStore(context.Background(), video.S3Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "sea-music-media",
		AccessKey: "sea-music-local", SecretKey: "local-object-store-password",
	})
	if err != nil {
		t.Fatalf("NewS3ObjectStore(): %v", err)
	}
	for index := range 10_050 {
		if _, _, err := store.PresignDownload(context.Background(), fmt.Sprintf("benchmark/object/%d", index), 5*time.Minute); err != nil {
			t.Fatalf("PresignDownload(%d): %v", index, err)
		}
	}
	if size := store.DownloadURLCacheSize(); size > 10_000 {
		t.Fatalf("download URL cache size = %d, want <= 10000", size)
	}
}

// TestPresignedDownloadCacheCanBeDisabled confirms that presigning a download with caching disabled leaves the download URL cache empty.
func TestPresignedDownloadCacheCanBeDisabled(t *testing.T) {
	endpoint := os.Getenv("SEA_VIDEO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SEA_VIDEO_TEST_S3_ENDPOINT is required")
	}
	store, err := video.NewS3ObjectStore(context.Background(), video.S3Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "sea-music-media",
		AccessKey: "sea-music-local", SecretKey: "local-object-store-password",
		DisableDownloadCache: true,
	})
	if err != nil {
		t.Fatalf("NewS3ObjectStore(): %v", err)
	}
	if _, _, err := store.PresignDownload(context.Background(), "benchmark/uncached", 5*time.Minute); err != nil {
		t.Fatalf("PresignDownload(): %v", err)
	}
	if size := store.DownloadURLCacheSize(); size != 0 {
		t.Fatalf("download URL cache size = %d, want 0", size)
	}
}
