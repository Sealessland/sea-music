package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const minimumTokenKeyBytes = 32

const localDatabaseURL = "postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable"

// LookupEnv abstracts environment lookup so configuration parsing stays deterministic in tests.
type LookupEnv func(string) (string, bool)

// Config is shared by the API and worker process composition roots.
type Config struct {
	Environment string
	LogLevel    string
	Auth        Auth
	Database    Database
	Redis       Redis
	ObjectStore ObjectStore
	Worker      Worker
	Broker      Broker
	Events      Events
	Social      Social
	Telemetry   Telemetry
	RateLimit   RateLimit
	HTTP        HTTP
}

type Auth struct {
	TokenKey   string
	Issuer     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

type Database struct {
	URL              string
	MaxOpen          int
	MaxIdle          int
	ConnectionMaxAge time.Duration
}

type Redis struct {
	URL string
}

type ObjectStore struct {
	Endpoint             string
	Region               string
	Bucket               string
	AccessKey            string
	SecretKey            string
	UploadTTL            time.Duration
	PlaybackURLTTL       time.Duration
	MaxUploadBytes       int64
	DisableDownloadCache bool
}

type Worker struct {
	PollInterval              time.Duration
	LeaseDuration             time.Duration
	MediaTimeout              time.Duration
	QueuedActivationInterval  time.Duration
	QueuedActivationThreshold time.Duration
	FFprobePath               string
	FFmpegPath                string
}

type Broker struct {
	Brokers []string
}

type Events struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
}

type Social struct {
	ReconcileInterval time.Duration
	ReconcileBatch    int
}

type Telemetry struct {
	OTLPEndpoint string
}

type RateLimit struct {
	IdentityWriteRate  float64
	IdentityWriteBurst int
	IdentityReadRate   float64
	IdentityReadBurst  int
}

type HTTP struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadinessTimeout  time.Duration
	ShutdownTimeout   time.Duration
	AllowedOrigins    []string
}

// Load reads process configuration from the environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom parses and validates configuration without exposing secret values in errors.
func LoadFrom(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment: valueOrDefault(lookup, "SEA_ENV", "development"),
		LogLevel:    valueOrDefault(lookup, "SEA_LOG_LEVEL", "info"),
		Auth: Auth{
			TokenKey:   valueOrDefault(lookup, "SEA_AUTH_TOKEN_KEY", ""),
			Issuer:     valueOrDefault(lookup, "SEA_AUTH_ISSUER", "sea-music"),
			AccessTTL:  15 * time.Minute,
			RefreshTTL: 30 * 24 * time.Hour,
		},
		Database: Database{
			URL:              valueOrDefault(lookup, "SEA_DATABASE_URL", localDatabaseURL),
			MaxOpen:          20,
			MaxIdle:          10,
			ConnectionMaxAge: 30 * time.Minute,
		},
		Redis: Redis{
			URL: valueOrDefault(lookup, "SEA_REDIS_URL", "redis://:local-redis-password@127.0.0.1:26379/0"),
		},
		ObjectStore: ObjectStore{
			Endpoint:       valueOrDefault(lookup, "SEA_S3_ENDPOINT", "http://127.0.0.1:28333"),
			Region:         valueOrDefault(lookup, "SEA_S3_REGION", "us-east-1"),
			Bucket:         valueOrDefault(lookup, "SEA_S3_BUCKET", "sea-music-media"),
			AccessKey:      valueOrDefault(lookup, "SEA_S3_ACCESS_KEY", "sea-music-local"),
			SecretKey:      valueOrDefault(lookup, "SEA_S3_SECRET_KEY", "local-object-store-password"),
			UploadTTL:      15 * time.Minute,
			PlaybackURLTTL: 5 * time.Minute,
			MaxUploadBytes: 10 << 30,
		},
		Worker: Worker{
			PollInterval:              time.Second,
			LeaseDuration:             2 * time.Minute,
			MediaTimeout:              10 * time.Minute,
			QueuedActivationInterval:  30 * time.Second,
			QueuedActivationThreshold: 2 * time.Minute,
			FFprobePath:               valueOrDefault(lookup, "SEA_FFPROBE_PATH", "ffprobe"),
			FFmpegPath:                valueOrDefault(lookup, "SEA_FFMPEG_PATH", "ffmpeg"),
		},
		Broker:    Broker{Brokers: strings.Split(valueOrDefault(lookup, "SEA_KAFKA_BROKERS", "127.0.0.1:29092"), ",")},
		Events:    Events{PollInterval: 500 * time.Millisecond, LeaseDuration: time.Minute, BatchSize: 100},
		Social:    Social{ReconcileInterval: time.Minute, ReconcileBatch: 100},
		Telemetry: Telemetry{OTLPEndpoint: valueOrDefault(lookup, "SEA_OTEL_EXPORTER_OTLP_ENDPOINT", "")},
		RateLimit: RateLimit{
			IdentityWriteRate:  5,
			IdentityWriteBurst: 10,
			IdentityReadRate:   20,
			IdentityReadBurst:  40,
		},
		HTTP: HTTP{
			Address:           valueOrDefault(lookup, "SEA_HTTP_ADDRESS", ":8080"),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ReadinessTimeout:  2 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			AllowedOrigins:    splitNonEmpty(valueOrDefault(lookup, "SEA_CORS_ALLOWED_ORIGINS", "http://localhost:5173")),
		},
	}

	durations := []struct {
		key    string
		target *time.Duration
	}{
		{"SEA_HTTP_READ_HEADER_TIMEOUT", &cfg.HTTP.ReadHeaderTimeout},
		{"SEA_HTTP_READ_TIMEOUT", &cfg.HTTP.ReadTimeout},
		{"SEA_HTTP_WRITE_TIMEOUT", &cfg.HTTP.WriteTimeout},
		{"SEA_HTTP_IDLE_TIMEOUT", &cfg.HTTP.IdleTimeout},
		{"SEA_HTTP_READINESS_TIMEOUT", &cfg.HTTP.ReadinessTimeout},
		{"SEA_HTTP_SHUTDOWN_TIMEOUT", &cfg.HTTP.ShutdownTimeout},
		{"SEA_DATABASE_CONNECTION_MAX_AGE", &cfg.Database.ConnectionMaxAge},
		{"SEA_AUTH_ACCESS_TTL", &cfg.Auth.AccessTTL},
		{"SEA_AUTH_REFRESH_TTL", &cfg.Auth.RefreshTTL},
		{"SEA_S3_UPLOAD_TTL", &cfg.ObjectStore.UploadTTL},
		{"SEA_S3_PLAYBACK_URL_TTL", &cfg.ObjectStore.PlaybackURLTTL},
		{"SEA_WORKER_POLL_INTERVAL", &cfg.Worker.PollInterval},
		{"SEA_WORKER_LEASE_DURATION", &cfg.Worker.LeaseDuration},
		{"SEA_MEDIA_TIMEOUT", &cfg.Worker.MediaTimeout},
		{"SEA_MEDIA_QUEUED_ACTIVATION_INTERVAL", &cfg.Worker.QueuedActivationInterval},
		{"SEA_MEDIA_QUEUED_ACTIVATION_THRESHOLD", &cfg.Worker.QueuedActivationThreshold},
		{"SEA_EVENT_POLL_INTERVAL", &cfg.Events.PollInterval},
		{"SEA_EVENT_LEASE_DURATION", &cfg.Events.LeaseDuration},
		{"SEA_COUNTER_RECONCILE_INTERVAL", &cfg.Social.ReconcileInterval},
	}
	for _, item := range durations {
		if err := parsePositiveDuration(lookup, item.key, item.target); err != nil {
			return Config{}, err
		}
	}
	if err := parsePositiveFloat(lookup, "SEA_RATE_IDENTITY_WRITE_RATE", &cfg.RateLimit.IdentityWriteRate); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt(lookup, "SEA_RATE_IDENTITY_WRITE_BURST", &cfg.RateLimit.IdentityWriteBurst); err != nil {
		return Config{}, err
	}
	if err := parsePositiveFloat(lookup, "SEA_RATE_IDENTITY_READ_RATE", &cfg.RateLimit.IdentityReadRate); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt(lookup, "SEA_RATE_IDENTITY_READ_BURST", &cfg.RateLimit.IdentityReadBurst); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt64(lookup, "SEA_S3_MAX_UPLOAD_BYTES", &cfg.ObjectStore.MaxUploadBytes); err != nil {
		return Config{}, err
	}
	if err := parseBool(lookup, "SEA_S3_DISABLE_DOWNLOAD_CACHE", &cfg.ObjectStore.DisableDownloadCache); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt(lookup, "SEA_EVENT_BATCH_SIZE", &cfg.Events.BatchSize); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt(lookup, "SEA_COUNTER_RECONCILE_BATCH", &cfg.Social.ReconcileBatch); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.Environment {
	case "development", "test", "staging", "production":
	default:
		return fmt.Errorf("SEA_ENV: unsupported environment %q", c.Environment)
	}
	if strings.TrimSpace(c.HTTP.Address) == "" {
		return errors.New("SEA_HTTP_ADDRESS: must not be empty")
	}
	for _, origin := range c.HTTP.AllowedOrigins {
		if origin == "*" {
			return errors.New("SEA_CORS_ALLOWED_ORIGINS: wildcard origins are not allowed")
		}
		if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
			return errors.New("SEA_CORS_ALLOWED_ORIGINS: origins must use http or https")
		}
	}
	if c.Environment == "production" && len(c.HTTP.AllowedOrigins) == 1 && c.HTTP.AllowedOrigins[0] == "http://localhost:5173" {
		return errors.New("SEA_CORS_ALLOWED_ORIGINS: production must configure explicit origins")
	}
	if len(c.Auth.TokenKey) < minimumTokenKeyBytes {
		return fmt.Errorf("SEA_AUTH_TOKEN_KEY: must contain at least %d bytes", minimumTokenKeyBytes)
	}
	if strings.TrimSpace(c.Auth.Issuer) == "" {
		return errors.New("SEA_AUTH_ISSUER: must not be empty")
	}
	if strings.TrimSpace(c.Database.URL) == "" {
		return errors.New("SEA_DATABASE_URL: must not be empty")
	}
	if c.Environment == "production" && c.Database.URL == localDatabaseURL {
		return errors.New("SEA_DATABASE_URL: production must not use local development credentials")
	}
	if strings.TrimSpace(c.Redis.URL) == "" {
		return errors.New("SEA_REDIS_URL: must not be empty")
	}
	if c.Environment == "production" && c.Redis.URL == "redis://:local-redis-password@127.0.0.1:26379/0" {
		return errors.New("SEA_REDIS_URL: production must not use local development credentials")
	}
	if strings.TrimSpace(c.ObjectStore.Endpoint) == "" || strings.TrimSpace(c.ObjectStore.Region) == "" || strings.TrimSpace(c.ObjectStore.Bucket) == "" || strings.TrimSpace(c.ObjectStore.AccessKey) == "" || strings.TrimSpace(c.ObjectStore.SecretKey) == "" {
		return errors.New("SEA_S3_*: endpoint, region, bucket, access key, and secret key are required")
	}
	if c.Environment == "production" && c.ObjectStore.AccessKey == "sea-music-local" {
		return errors.New("SEA_S3_ACCESS_KEY: production must not use local development credentials")
	}
	if strings.TrimSpace(c.Worker.FFprobePath) == "" || strings.TrimSpace(c.Worker.FFmpegPath) == "" {
		return errors.New("SEA_FFPROBE_PATH and SEA_FFMPEG_PATH must not be empty")
	}
	if len(c.Broker.Brokers) == 0 {
		return errors.New("SEA_KAFKA_BROKERS: at least one broker is required")
	}
	for _, broker := range c.Broker.Brokers {
		if strings.TrimSpace(broker) == "" {
			return errors.New("SEA_KAFKA_BROKERS: broker addresses must not be empty")
		}
	}
	return nil
}

func splitNonEmpty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parsePositiveInt64(lookup LookupEnv, key string, target *int64) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive integer", key)
	}
	*target = value
	return nil
}

func parsePositiveFloat(lookup LookupEnv, key string, target *float64) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive number", key)
	}
	*target = value
	return nil
}

func parsePositiveInt(lookup LookupEnv, key string, target *int) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive integer", key)
	}
	*target = value
	return nil
}

func parseBool(lookup LookupEnv, key string, target *bool) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%s: must be a boolean", key)
	}
	*target = value
	return nil
}

func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func parsePositiveDuration(lookup LookupEnv, key string, target *time.Duration) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s: parse duration: %w", key, err)
	}
	if value <= 0 {
		return fmt.Errorf("%s: duration must be positive", key)
	}
	*target = value
	return nil
}
