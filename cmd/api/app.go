package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/appapi"
	"github.com/sealessland/sea-music/internal/discovery"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/config"
	"github.com/sealessland/sea-music/internal/platform/httpserver"
	"github.com/sealessland/sea-music/internal/platform/logging"
	platformmetrics "github.com/sealessland/sea-music/internal/platform/metrics"
	"github.com/sealessland/sea-music/internal/platform/ratelimit"
	"github.com/sealessland/sea-music/internal/platform/telemetry"
	"github.com/sealessland/sea-music/internal/social"
	"github.com/sealessland/sea-music/internal/video"
)

// run is the API composition root. Domain collaboration is wired here rather
// than hidden inside domain packages.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel, "api")
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTelemetry, err := telemetry.Setup(ctx, "sea-music-api", cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("configure telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()

	database, err := openDatabase(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	redisClient, err := openRedis(ctx, cfg)
	if err != nil {
		return err
	}
	defer redisClient.Close()
	objectStore, err := video.NewS3ObjectStore(ctx, video.S3Config{Endpoint: cfg.ObjectStore.Endpoint, Region: cfg.ObjectStore.Region, Bucket: cfg.ObjectStore.Bucket, AccessKey: cfg.ObjectStore.AccessKey, SecretKey: cfg.ObjectStore.SecretKey, DisableDownloadCache: cfg.ObjectStore.DisableDownloadCache})
	if err != nil {
		return err
	}
	if err := objectStore.Check(ctx); err != nil {
		return err
	}
	eventPublisher, err := events.NewPublisher(eventBrokerConfig(cfg), "domain-events")
	if err != nil {
		return err
	}
	defer eventPublisher.Close()

	tokenManager := identity.NewTokenManager([]byte(cfg.Auth.TokenKey), cfg.Auth.Issuer, cfg.Auth.AccessTTL)
	identityService := identity.NewService(identity.NewPostgresRepository(database), identity.NewPasswordHasher(identity.DefaultPasswordParams())).WithSessions(tokenManager, cfg.Auth.RefreshTTL)
	rateMetrics := ratelimit.NewMetrics(platformmetrics.Registry)
	rateLimiter := ratelimit.New(redisClient, rateMetrics)
	rateMiddleware := appapi.NewRateLimitMiddleware(rateLimiter, logger)
	eventRepository := events.NewPostgresRepository(database)
	counterReconciler := social.NewCounterReconciler(database, redisClient)
	platformmetrics.Registry.MustRegister(appapi.NewOperationalMetrics(eventRepository, counterReconciler, database, redisClient))
	identityHandler := appapi.NewIdentityHandler(
		identityService,
		tokenManager,
		rateMiddleware,
		ratelimit.Policy{RatePerSecond: cfg.RateLimit.IdentityWriteRate, Burst: cfg.RateLimit.IdentityWriteBurst},
		ratelimit.Policy{RatePerSecond: cfg.RateLimit.IdentityReadRate, Burst: cfg.RateLimit.IdentityReadBurst},
		logger,
	)
	outboxWriter := video.OutboxWriterFunc(func(ctx context.Context, transaction *sql.Tx, event video.DomainEvent) (string, error) {
		envelope, err := eventRepository.EnqueueTx(ctx, transaction, events.NewEvent{
			Topic: event.Topic, Type: event.Type, Version: event.Version,
			AggregateType: event.AggregateType, AggregateID: event.AggregateID, AggregateVersion: event.AggregateVersion,
			OccurredAt: time.Now().UTC(), TraceParent: telemetry.TraceParent(ctx), Data: event.Data,
		})
		return envelope.ID, err
	})
	videoRepository := video.NewPostgresRepository(database).WithOutbox(outboxWriter)
	uploadService := video.NewUploadService(videoRepository, objectStore, cfg.ObjectStore.UploadTTL, cfg.ObjectStore.MaxUploadBytes)
	publicationService := video.NewPublicationService(videoRepository, objectStore, cfg.ObjectStore.PlaybackURLTTL)
	videoHandler := appapi.NewVideoHandler(videoRepository, uploadService, publicationService, appapi.NewAuthenticator(tokenManager), logger)
	eventsHandler := appapi.NewEventsAdminHandler(events.NewReplayService(eventRepository, eventPublisher), appapi.NewAuthenticator(tokenManager), logger)
	socialOutboxWriter := social.OutboxWriterFunc(func(ctx context.Context, transaction *sql.Tx, event social.DomainEvent) (string, error) {
		envelope, err := eventRepository.EnqueueTx(ctx, transaction, events.NewEvent{
			Topic: event.Topic, Type: event.Type, Version: event.Version,
			AggregateType: event.AggregateType, AggregateID: event.AggregateID, AggregateVersion: event.AggregateVersion,
			OccurredAt: time.Now().UTC(), TraceParent: telemetry.TraceParent(ctx), Data: event.Data,
		})
		return envelope.ID, err
	})
	socialHandler := appapi.NewSocialHandler(social.NewPostgresRepository(database).WithOutbox(socialOutboxWriter), appapi.NewAuthenticator(tokenManager), logger)
	discoveryHandler := appapi.NewDiscoveryHandler(discovery.NewPostgresRepository(database).WithRanking(redisClient), appapi.NewAuthenticator(tokenManager), logger)
	readiness := httpserver.Dependencies{
		Required: map[string]httpserver.ReadinessChecker{
			"database":     httpserver.CheckFunc(func(ctx context.Context) error { return database.PingContext(ctx) }),
			"redis":        httpserver.CheckFunc(func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }),
			"object_store": httpserver.CheckFunc(objectStore.Check),
		},
		Optional: map[string]httpserver.ReadinessChecker{
			"broker": httpserver.CheckFunc(eventPublisher.Ping),
		},
		Timeout: cfg.HTTP.ReadinessTimeout,
	}
	handler := httpserver.NewHandlerWithOrigins(logger, readiness, cfg.HTTP.AllowedOrigins, appapi.RegisterFrontendRoutes, identityHandler.RegisterRoutes, videoHandler.RegisterRoutes, socialHandler.RegisterRoutes, discoveryHandler.RegisterRoutes, eventsHandler.RegisterRoutes, appapi.RegisterMetricsRoutes)
	server := httpserver.New(cfg.HTTP, handler)
	return httpserver.Run(ctx, server, cfg.HTTP.ShutdownTimeout, logger)
}

func openDatabase(cfg config.Config) (*sql.DB, error) {
	connectionConfig, err := pgx.ParseConfig(cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	connectionConfig.Tracer = otelpgx.NewTracer()
	database := stdlib.OpenDB(*connectionConfig)
	database.SetMaxOpenConns(cfg.Database.MaxOpen)
	database.SetMaxIdleConns(cfg.Database.MaxIdle)
	database.SetConnMaxLifetime(cfg.Database.ConnectionMaxAge)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ReadinessTimeout)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return database, nil
}

func openRedis(ctx context.Context, cfg config.Config) (*redis.Client, error) {
	options, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	if err := redisotel.InstrumentTracing(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument Redis tracing: %w", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, cfg.HTTP.ReadinessTimeout)
	defer cancel()
	if err := client.Ping(checkCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return client, nil
}
