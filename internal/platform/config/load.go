package config

import (
	"strings"
	"time"
)

// LoadFrom parses and validates configuration without exposing secret values in errors.
func LoadFrom(lookup LookupEnv) (Config, error) {
	cfg := defaults(lookup)

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
		{"SEA_MODERATION_POLL_INTERVAL", &cfg.Moderation.PollInterval},
		{"SEA_MODERATION_LEASE_DURATION", &cfg.Moderation.LeaseDuration},
		{"SEA_MODERATION_EVALUATION_TIMEOUT", &cfg.Moderation.EvaluationTimeout},
		{"SEA_MODERATION_RPC_TIMEOUT", &cfg.Moderation.RPCTimeout},
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
	if err := parseBool(lookup, "SEA_MODERATION_INSECURE", &cfg.Moderation.Insecure); err != nil {
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

func defaults(lookup LookupEnv) Config {
	return Config{
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
		Redis: Redis{URL: valueOrDefault(lookup, "SEA_REDIS_URL", "redis://:local-redis-password@127.0.0.1:26379/0")},
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
		RateLimit: RateLimit{IdentityWriteRate: 5, IdentityWriteBurst: 10, IdentityReadRate: 20, IdentityReadBurst: 40},
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
		Moderation: Moderation{
			GRPCAddress:       valueOrDefault(lookup, "SEA_MODERATION_GRPC_ADDRESS", ":9090"),
			MetricsAddress:    valueOrDefault(lookup, "SEA_MODERATION_METRICS_ADDRESS", ":9091"),
			AgentAddress:      valueOrDefault(lookup, "SEA_MODERATION_AGENT_ADDRESS", "127.0.0.1:9090"),
			PolicyVersion:     valueOrDefault(lookup, "SEA_MODERATION_POLICY_VERSION", "v1"),
			Mode:              valueOrDefault(lookup, "SEA_MODERATION_MODE", "shadow"),
			Provider:          valueOrDefault(lookup, "SEA_MODERATION_PROVIDER", "disabled"),
			ProviderAPIKey:    valueOrDefault(lookup, "SEA_MODERATION_PROVIDER_API_KEY", ""),
			ProviderBaseURL:   valueOrDefault(lookup, "SEA_MODERATION_PROVIDER_BASE_URL", ""),
			ProviderModel:     valueOrDefault(lookup, "SEA_MODERATION_PROVIDER_MODEL", "gpt-4o-mini"),
			Insecure:          true,
			TLSCertFile:       valueOrDefault(lookup, "SEA_MODERATION_TLS_CERT_FILE", ""),
			TLSKeyFile:        valueOrDefault(lookup, "SEA_MODERATION_TLS_KEY_FILE", ""),
			TLSCAFile:         valueOrDefault(lookup, "SEA_MODERATION_TLS_CA_FILE", ""),
			TLSServerName:     valueOrDefault(lookup, "SEA_MODERATION_TLS_SERVER_NAME", ""),
			PollInterval:      time.Second,
			LeaseDuration:     2 * time.Minute,
			EvaluationTimeout: 90 * time.Second,
			RPCTimeout:        5 * time.Second,
		},
	}
}
