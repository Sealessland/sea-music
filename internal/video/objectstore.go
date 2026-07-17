package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type S3Config struct {
	Endpoint             string
	Region               string
	Bucket               string
	AccessKey            string
	SecretKey            string
	DisableDownloadCache bool
}

type ObjectInspection struct {
	SizeBytes      int64
	ContentType    string
	ChecksumSHA256 string
}

type S3ObjectStore struct {
	bucket               string
	client               *s3.Client
	presigner            *s3.PresignClient
	downloadCacheMu      sync.Mutex
	downloadCache        map[string]cachedDownloadURL
	downloadCacheEnabled bool
}

type cachedDownloadURL struct {
	URL       string
	ExpiresAt time.Time
}

const maxDownloadURLCacheEntries = 10_000

func NewS3ObjectStore(ctx context.Context, cfg S3Config) (*S3ObjectStore, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("invalid S3 configuration")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	loaded, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(loaded, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(cfg.Endpoint)
		options.UsePathStyle = true
	})
	return &S3ObjectStore{
		bucket: cfg.Bucket, client: client, presigner: s3.NewPresignClient(client),
		downloadCache: make(map[string]cachedDownloadURL), downloadCacheEnabled: !cfg.DisableDownloadCache,
	}, nil
}

func (store *S3ObjectStore) PresignUpload(ctx context.Context, key, contentType, checksum string, ttl time.Duration) (string, time.Time, error) {
	expiresAt := time.Now().UTC().Add(ttl)
	result, err := store.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), ContentType: aws.String(contentType),
		Metadata: map[string]string{"sha256": checksum},
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign source upload: %w", err)
	}
	return result.URL, expiresAt, nil
}

func (store *S3ObjectStore) PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	ctx, span := otel.Tracer("sea-music/video").Start(ctx, "object_store.presign_download")
	defer span.End()
	now := time.Now().UTC()
	if store.downloadCacheEnabled {
		store.downloadCacheMu.Lock()
		if cached, ok := store.downloadCache[key]; ok && cached.ExpiresAt.After(now.Add(5*time.Second)) {
			store.downloadCacheMu.Unlock()
			span.SetAttributes(attribute.Bool("cache.hit", true))
			return cached.URL, cached.ExpiresAt, nil
		}
		store.downloadCacheMu.Unlock()
	}
	span.SetAttributes(attribute.Bool("cache.hit", false))
	expiresAt := now.Add(ttl)
	result, err := store.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign rendition download: %w", err)
	}
	if !store.downloadCacheEnabled {
		return result.URL, expiresAt, nil
	}
	store.downloadCacheMu.Lock()
	if len(store.downloadCache) >= maxDownloadURLCacheEntries {
		for cachedKey, cached := range store.downloadCache {
			if !cached.ExpiresAt.After(now) {
				delete(store.downloadCache, cachedKey)
			}
		}
	}
	for len(store.downloadCache) >= maxDownloadURLCacheEntries {
		for cachedKey := range store.downloadCache {
			delete(store.downloadCache, cachedKey)
			break
		}
	}
	store.downloadCache[key] = cachedDownloadURL{URL: result.URL, ExpiresAt: expiresAt}
	store.downloadCacheMu.Unlock()
	return result.URL, expiresAt, nil
}

func (store *S3ObjectStore) DownloadURLCacheSize() int {
	store.downloadCacheMu.Lock()
	defer store.downloadCacheMu.Unlock()
	return len(store.downloadCache)
}

func (store *S3ObjectStore) Inspect(ctx context.Context, key string, maxBytes int64) (ObjectInspection, error) {
	result, err := store.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(store.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInspection{}, fmt.Errorf("read uploaded source: %w", err)
	}
	defer result.Body.Close()
	if result.ContentLength == nil || *result.ContentLength <= 0 || *result.ContentLength > maxBytes {
		return ObjectInspection{}, ErrInvalidUpload
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		return ObjectInspection{}, fmt.Errorf("hash uploaded source: %w", err)
	}
	if written != *result.ContentLength || written > maxBytes {
		return ObjectInspection{}, ErrInvalidUpload
	}
	return ObjectInspection{SizeBytes: written, ContentType: aws.ToString(result.ContentType), ChecksumSHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (store *S3ObjectStore) Check(ctx context.Context) error {
	_, err := store.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(store.bucket)})
	if err != nil {
		return fmt.Errorf("check media bucket: %w", err)
	}
	return nil
}

func (store *S3ObjectStore) DownloadFile(ctx context.Context, key, destination string, maxBytes int64) error {
	result, err := store.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(store.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download object: %w", err)
	}
	defer result.Body.Close()
	if result.ContentLength == nil || *result.ContentLength <= 0 || *result.ContentLength > maxBytes {
		return ErrInvalidUpload
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create object destination: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(result.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download object body: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close object destination: %w", closeErr)
	}
	if written != *result.ContentLength || written > maxBytes {
		return ErrInvalidUpload
	}
	return nil
}

func (store *S3ObjectStore) UploadFile(ctx context.Context, key, contentType, source string) error {
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open rendition: %w", err)
	}
	defer file.Close()
	if _, err := store.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), ContentType: aws.String(contentType), Body: file,
	}); err != nil {
		return fmt.Errorf("upload rendition: %w", err)
	}
	return nil
}
