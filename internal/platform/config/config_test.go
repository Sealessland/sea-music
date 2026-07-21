package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/platform/config"
)

// TestLoadUsesSafeOperationalDefaults verifies that minimal valid input yields the expected development, network, database, authentication, rate-limit, and moderation defaults.
func TestLoadUsesSafeOperationalDefaults(t *testing.T) {
	values := map[string]string{
		"SEA_AUTH_TOKEN_KEY": strings.Repeat("k", 32),
	}

	cfg, err := config.LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.HTTP.Address != ":8080" {
		t.Errorf("HTTP.Address = %q, want :8080", cfg.HTTP.Address)
	}
	if cfg.HTTP.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", cfg.HTTP.ReadHeaderTimeout)
	}
	if cfg.HTTP.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 10s", cfg.HTTP.ShutdownTimeout)
	}
	if cfg.Database.URL != "postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable" {
		t.Errorf("Database.URL = %q, want local development database", cfg.Database.URL)
	}
	if cfg.Auth.AccessTTL != 15*time.Minute || cfg.Auth.RefreshTTL != 30*24*time.Hour {
		t.Errorf("auth TTLs = (%v, %v)", cfg.Auth.AccessTTL, cfg.Auth.RefreshTTL)
	}
	if cfg.RateLimit.IdentityWriteRate != 5 || cfg.RateLimit.IdentityWriteBurst != 10 {
		t.Errorf("identity write rate policy = %+v", cfg.RateLimit)
	}
	if cfg.Moderation.GRPCAddress != ":9090" || cfg.Moderation.MetricsAddress != ":9091" || cfg.Moderation.AgentAddress != "127.0.0.1:9090" {
		t.Errorf("moderation addresses = %+v", cfg.Moderation)
	}
	if cfg.Moderation.Mode != "shadow" || cfg.Moderation.EvaluationTimeout >= cfg.Moderation.LeaseDuration {
		t.Errorf("moderation safety defaults = %+v", cfg.Moderation)
	}
	if cfg.Moderation.ApproveThreshold != 0.90 || cfg.Moderation.RejectThreshold != 0.95 {
		t.Errorf("moderation decision thresholds = %+v", cfg.Moderation)
	}
	if !cfg.Moderation.Insecure {
		t.Fatal("development moderation transport should default to explicitly insecure local mode")
	}
}

// TestLoadRejectsUnsafeModerationConfiguration verifies that unsupported or unsafe moderation settings fail with an error naming the offending environment variable.
func TestLoadRejectsUnsafeModerationConfiguration(t *testing.T) {
	base := map[string]string{"SEA_AUTH_TOKEN_KEY": strings.Repeat("k", 32)}
	tests := []struct {
		name, key, value string
	}{
		{"mode", "SEA_MODERATION_MODE", "automatic"},
		{"empty policy", "SEA_MODERATION_POLICY_VERSION", ""},
		{"timeout exceeds lease", "SEA_MODERATION_EVALUATION_TIMEOUT", "3m"},
		{"provider", "SEA_MODERATION_PROVIDER", "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(base)+1)
			for key, value := range base {
				values[key] = value
			}
			values[test.key] = test.value
			_, err := config.LoadFrom(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("LoadFrom() error = %v, want %s error", err, test.key)
			}
		})
	}
}

// TestLoadRequiresModerationProviderSecret verifies that selecting the OpenAI moderation provider without its API key fails with an error naming the required variable.
func TestLoadRequiresModerationProviderSecret(t *testing.T) {
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY": strings.Repeat("k", 32), "SEA_MODERATION_PROVIDER": "openai",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_MODERATION_PROVIDER_API_KEY") {
		t.Fatalf("LoadFrom() error = %v", err)
	}
}

// TestLoadValidatesModerationDecisionThresholds verifies that thresholds outside the valid range or ordered below the approval threshold fail with an error naming the offending variable.
func TestLoadValidatesModerationDecisionThresholds(t *testing.T) {
	base := map[string]string{"SEA_AUTH_TOKEN_KEY": strings.Repeat("k", 32)}
	tests := []struct {
		name, key, value string
	}{
		{"approve over one", "SEA_MODERATION_APPROVE_THRESHOLD", "1.01"},
		{"reject below approve", "SEA_MODERATION_REJECT_THRESHOLD", "0.80"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(base)+1)
			for key, value := range base {
				values[key] = value
			}
			values[test.key] = test.value
			_, err := config.LoadFrom(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("LoadFrom() error = %v, want %s error", err, test.key)
			}
		})
	}
}

// TestLoadRejectsWeakTokenKeyWithoutEchoingIt verifies that an undersized authentication token key is rejected without exposing the secret in the error.
func TestLoadRejectsWeakTokenKeyWithoutEchoingIt(t *testing.T) {
	const weakKey = "do-not-log-me"
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY": weakKey,
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil, want weak-key error")
	}
	if strings.Contains(err.Error(), weakKey) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

// TestLoadRejectsInvalidDuration verifies that an unparseable shutdown timeout fails with an error naming its environment variable.
func TestLoadRejectsInvalidDuration(t *testing.T) {
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY":        strings.Repeat("k", 32),
		"SEA_HTTP_SHUTDOWN_TIMEOUT": "eventually",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_HTTP_SHUTDOWN_TIMEOUT") {
		t.Fatalf("LoadFrom() error = %v, want named duration error", err)
	}
}

// TestProductionRejectsLocalDatabaseDefault verifies that production configuration cannot retain the default local database URL and reports the required database variable.
func TestProductionRejectsLocalDatabaseDefault(t *testing.T) {
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_ENV":                  "production",
		"SEA_AUTH_TOKEN_KEY":       strings.Repeat("k", 32),
		"SEA_CORS_ALLOWED_ORIGINS": "https://app.example.com",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_DATABASE_URL") {
		t.Fatalf("LoadFrom() error = %v, want production database error", err)
	}
}

// TestConfigurationRejectsWildcardCORS verifies that a wildcard allowed origin is rejected with an error naming the CORS environment variable.
func TestConfigurationRejectsWildcardCORS(t *testing.T) {
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY":       strings.Repeat("k", 32),
		"SEA_CORS_ALLOWED_ORIGINS": "*",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_CORS_ALLOWED_ORIGINS") {
		t.Fatalf("LoadFrom() error = %v, want CORS error", err)
	}
}

// TestLoadParsesDownloadURLCacheSwitch verifies that the download-cache switch accepts a valid boolean and rejects invalid text with an error naming the environment variable.
func TestLoadParsesDownloadURLCacheSwitch(t *testing.T) {
	cfg, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY":            strings.Repeat("k", 32),
		"SEA_S3_DISABLE_DOWNLOAD_CACHE": "true",
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if !cfg.ObjectStore.DisableDownloadCache {
		t.Fatal("ObjectStore.DisableDownloadCache = false, want true")
	}

	_, err = config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY":            strings.Repeat("k", 32),
		"SEA_S3_DISABLE_DOWNLOAD_CACHE": "sometimes",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_S3_DISABLE_DOWNLOAD_CACHE") {
		t.Fatalf("LoadFrom() error = %v, want named boolean error", err)
	}
}

func TestLoadSelectsRocketMQBroker(t *testing.T) {
	values := map[string]string{
		"SEA_AUTH_TOKEN_KEY":         strings.Repeat("k", 32),
		"SEA_EVENT_BROKER":           "rocketmq",
		"SEA_ROCKETMQ_ENDPOINT":      "127.0.0.1:8081",
		"SEA_ROCKETMQ_ACCESS_KEY":    "access",
		"SEA_ROCKETMQ_ACCESS_SECRET": "secret",
	}
	cfg, err := config.LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Broker.Driver != "rocketmq" || len(cfg.Broker.Brokers) != 1 || cfg.Broker.Brokers[0] != "127.0.0.1:8081" {
		t.Fatalf("Broker = %+v", cfg.Broker)
	}
}

func TestLoadRejectsUnknownEventBroker(t *testing.T) {
	_, err := config.LoadFrom(mapLookup(map[string]string{
		"SEA_AUTH_TOKEN_KEY": strings.Repeat("k", 32),
		"SEA_EVENT_BROKER":   "unknown",
	}))
	if err == nil || !strings.Contains(err.Error(), "SEA_EVENT_BROKER") {
		t.Fatalf("LoadFrom() error = %v, want SEA_EVENT_BROKER validation", err)
	}
}

// mapLookup returns an environment lookup function backed by values, preserving each key's value and presence status.
func mapLookup(values map[string]string) config.LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
