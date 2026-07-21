package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUploadForbidden = errors.New("upload forbidden")
	ErrInvalidUpload   = errors.New("invalid uploaded object")
)

type UploadRequest struct {
	VideoID        string
	CreatorID      string
	SizeBytes      int64
	ContentType    string
	ChecksumSHA256 string
}

type SourceAsset struct {
	ID             string
	VideoID        string
	ObjectKey      string
	SizeBytes      int64
	ContentType    string
	ChecksumSHA256 string
	Status         string
}

type UploadGrant struct {
	AssetID   string    `json:"asset_id"`
	URL       string    `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type FinalizeResult struct {
	AssetID string `json:"asset_id"`
	JobID   string `json:"job_id"`
	Video   Video  `json:"video"`
}

type uploadRepository interface {
	BeginUpload(context.Context, UploadRequest) (SourceAsset, error)
	GetUpload(context.Context, string, string) (SourceAsset, error)
	RejectUpload(context.Context, string, string) error
	FinalizeUpload(context.Context, string, string) (FinalizeResult, error)
}

type uploadObjectStore interface {
	PresignUpload(context.Context, string, string, string, time.Duration) (string, time.Time, error)
	Inspect(context.Context, string, int64) (ObjectInspection, error)
}

type UploadService struct {
	repository uploadRepository
	store      uploadObjectStore
	ttl        time.Duration
	maxBytes   int64
}

// NewUploadService constructs an upload service that enforces maxBytes and issues upload grants valid for ttl.
func NewUploadService(repository uploadRepository, store uploadObjectStore, ttl time.Duration, maxBytes int64) *UploadService {
	return &UploadService{repository: repository, store: store, ttl: ttl, maxBytes: maxBytes}
}

// CreateGrant validates and normalizes MP4 upload metadata, persists a pending source asset, and returns a presigned upload URL or ErrInvalidUpload for invalid input.
func (service *UploadService) CreateGrant(ctx context.Context, request UploadRequest) (UploadGrant, error) {
	request.ContentType = strings.ToLower(strings.TrimSpace(request.ContentType))
	request.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(request.ChecksumSHA256))
	if request.VideoID == "" || request.CreatorID == "" || request.SizeBytes <= 0 || request.SizeBytes > service.maxBytes || request.ContentType != "video/mp4" {
		return UploadGrant{}, ErrInvalidUpload
	}
	decoded, err := hex.DecodeString(request.ChecksumSHA256)
	if err != nil || len(decoded) != sha256.Size {
		return UploadGrant{}, ErrInvalidUpload
	}
	asset, err := service.repository.BeginUpload(ctx, request)
	if err != nil {
		return UploadGrant{}, err
	}
	url, expiresAt, err := service.store.PresignUpload(ctx, asset.ObjectKey, asset.ContentType, asset.ChecksumSHA256, service.ttl)
	if err != nil {
		return UploadGrant{}, err
	}
	return UploadGrant{AssetID: asset.ID, URL: url, ExpiresAt: expiresAt}, nil
}

// Finalize verifies the uploaded object against its declared size, content type, and checksum before finalizing; already verified assets skip inspection, while mismatches are best-effort rejected and return ErrInvalidUpload.
func (service *UploadService) Finalize(ctx context.Context, videoID, creatorID string) (FinalizeResult, error) {
	asset, err := service.repository.GetUpload(ctx, videoID, creatorID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if asset.Status == "verified" {
		return service.repository.FinalizeUpload(ctx, videoID, creatorID)
	}
	inspection, err := service.store.Inspect(ctx, asset.ObjectKey, service.maxBytes)
	if err != nil {
		return FinalizeResult{}, err
	}
	if inspection.SizeBytes != asset.SizeBytes || !strings.EqualFold(inspection.ContentType, asset.ContentType) || inspection.ChecksumSHA256 != asset.ChecksumSHA256 {
		_ = service.repository.RejectUpload(ctx, videoID, creatorID)
		return FinalizeResult{}, fmt.Errorf("%w: object does not match declared size, type, and checksum", ErrInvalidUpload)
	}
	return service.repository.FinalizeUpload(ctx, videoID, creatorID)
}
