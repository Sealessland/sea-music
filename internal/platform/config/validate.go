package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validate enforces safety invariants after all configuration sources are applied.
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
	if c.Broker.Driver != "kafka" && c.Broker.Driver != "rocketmq" {
		return errors.New("SEA_EVENT_BROKER: must be kafka or rocketmq")
	}
	if len(c.Broker.Brokers) == 0 {
		return fmt.Errorf("%s: at least one endpoint is required", brokerEndpointKey(c.Broker.Driver))
	}
	if strings.TrimSpace(c.Moderation.GRPCAddress) == "" {
		return errors.New("SEA_MODERATION_GRPC_ADDRESS: must not be empty")
	}
	if strings.TrimSpace(c.Moderation.MetricsAddress) == "" {
		return errors.New("SEA_MODERATION_METRICS_ADDRESS: must not be empty")
	}
	if strings.TrimSpace(c.Moderation.AgentAddress) == "" {
		return errors.New("SEA_MODERATION_AGENT_ADDRESS: must not be empty")
	}
	if strings.TrimSpace(c.Moderation.PolicyVersion) == "" {
		return errors.New("SEA_MODERATION_POLICY_VERSION: must not be empty")
	}
	if c.Moderation.Mode != "shadow" && c.Moderation.Mode != "enforce" {
		return errors.New("SEA_MODERATION_MODE: must be shadow or enforce")
	}
	if c.Moderation.Provider != "disabled" && c.Moderation.Provider != "openai" {
		return errors.New("SEA_MODERATION_PROVIDER: must be disabled or openai")
	}
	if c.Moderation.Provider == "openai" && strings.TrimSpace(c.Moderation.ProviderAPIKey) == "" {
		return errors.New("SEA_MODERATION_PROVIDER_API_KEY: required for openai provider")
	}
	if strings.TrimSpace(c.Moderation.ProviderModel) == "" {
		return errors.New("SEA_MODERATION_PROVIDER_MODEL: must not be empty")
	}
	if c.Moderation.ApproveThreshold <= 0 || c.Moderation.ApproveThreshold > 1 {
		return errors.New("SEA_MODERATION_APPROVE_THRESHOLD: must be within (0,1]")
	}
	if c.Moderation.RejectThreshold < c.Moderation.ApproveThreshold || c.Moderation.RejectThreshold > 1 {
		return errors.New("SEA_MODERATION_REJECT_THRESHOLD: must be within [approve threshold,1]")
	}
	if c.Environment == "production" && c.Moderation.Insecure {
		return errors.New("SEA_MODERATION_INSECURE: plaintext gRPC is not allowed in production")
	}
	if !c.Moderation.Insecure && (strings.TrimSpace(c.Moderation.TLSCertFile) == "" || strings.TrimSpace(c.Moderation.TLSKeyFile) == "" || strings.TrimSpace(c.Moderation.TLSCAFile) == "") {
		return errors.New("SEA_MODERATION_TLS_*: cert, key, and CA files are required when TLS is enabled")
	}
	if c.Moderation.EvaluationTimeout >= c.Moderation.LeaseDuration {
		return errors.New("SEA_MODERATION_EVALUATION_TIMEOUT: must be shorter than SEA_MODERATION_LEASE_DURATION")
	}
	for _, broker := range c.Broker.Brokers {
		if strings.TrimSpace(broker) == "" {
			return fmt.Errorf("%s: endpoint addresses must not be empty", brokerEndpointKey(c.Broker.Driver))
		}
	}
	if c.Broker.Driver == "rocketmq" && len(c.Broker.Brokers) != 1 {
		return errors.New("SEA_ROCKETMQ_ENDPOINT: exactly one proxy endpoint is required")
	}
	return nil
}

func brokerEndpointKey(driver string) string {
	if driver == "rocketmq" {
		return "SEA_ROCKETMQ_ENDPOINT"
	}
	return "SEA_KAFKA_BROKERS"
}
