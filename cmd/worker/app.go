package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/discovery"
	"github.com/sealessland/sea-music/internal/events"
	moderationv1 "github.com/sealessland/sea-music/internal/gen/moderation/v1"
	"github.com/sealessland/sea-music/internal/moderation"
	"github.com/sealessland/sea-music/internal/moderation/grpcadapter"
	"github.com/sealessland/sea-music/internal/platform/config"
	"github.com/sealessland/sea-music/internal/platform/logging"
	"github.com/sealessland/sea-music/internal/platform/telemetry"
	"github.com/sealessland/sea-music/internal/social"
	"github.com/sealessland/sea-music/internal/video"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// run is the worker composition root. It owns process-scoped resources and
// keeps domain packages independent from one another.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel, "worker")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTelemetry, err := telemetry.Setup(ctx, "sea-music-worker", cfg.Telemetry.OTLPEndpoint)
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

	connectionConfig, err := pgx.ParseConfig(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("parse worker database configuration: %w", err)
	}
	connectionConfig.Tracer = otelpgx.NewTracer()
	database := stdlib.OpenDB(*connectionConfig)
	defer database.Close()
	database.SetMaxOpenConns(cfg.Database.MaxOpen)
	database.SetMaxIdleConns(cfg.Database.MaxIdle)
	database.SetConnMaxLifetime(cfg.Database.ConnectionMaxAge)
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping worker database: %w", err)
	}

	store, err := video.NewS3ObjectStore(ctx, video.S3Config{Endpoint: cfg.ObjectStore.Endpoint, Region: cfg.ObjectStore.Region, Bucket: cfg.ObjectStore.Bucket, AccessKey: cfg.ObjectStore.AccessKey, SecretKey: cfg.ObjectStore.SecretKey, DisableDownloadCache: cfg.ObjectStore.DisableDownloadCache})
	if err != nil {
		return err
	}
	if err := store.Check(ctx); err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())
	eventRepository := events.NewPostgresRepository(database)
	outboxWriter := video.OutboxWriterFunc(func(ctx context.Context, transaction *sql.Tx, event video.DomainEvent) (string, error) {
		envelope, err := eventRepository.EnqueueTx(ctx, transaction, events.NewEvent{
			Topic: event.Topic, Type: event.Type, Version: event.Version,
			AggregateType: event.AggregateType, AggregateID: event.AggregateID, AggregateVersion: event.AggregateVersion,
			OccurredAt: time.Now().UTC(), TraceParent: telemetry.TraceParent(ctx), Data: event.Data,
		})
		return envelope.ID, err
	})
	repository := video.NewPostgresRepository(database).WithOutbox(outboxWriter)
	processor := video.NewFFmpegProcessor(store, cfg.Worker.FFprobePath, cfg.Worker.FFmpegPath, cfg.Worker.MediaTimeout, cfg.ObjectStore.MaxUploadBytes)
	service := video.NewProcessingService(repository, processor, workerID, cfg.Worker.LeaseDuration)

	brokerConfig := eventBrokerConfig(cfg)
	publisher, err := events.NewPublisher(brokerConfig, "domain-events")
	if err != nil {
		return err
	}
	defer publisher.Close()
	if err := publisher.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s broker: %w", cfg.Broker.Driver, err)
	}
	dispatcher := events.NewDispatcher(eventRepository, publisher, workerID, cfg.Events.BatchSize, cfg.Events.LeaseDuration)

	redisOptions, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		return fmt.Errorf("parse worker Redis URL: %w", err)
	}
	redisClient := redis.NewClient(redisOptions)
	if err := redisotel.InstrumentTracing(redisClient); err != nil {
		_ = redisClient.Close()
		return fmt.Errorf("instrument worker Redis tracing: %w", err)
	}
	defer redisClient.Close()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping worker Redis: %w", err)
	}

	eventConsumer, err := events.NewConsumer(brokerConfig, events.ConsumerConfig{
		Topic: "domain-events", Group: "sea-music-media-processing-v1",
		Name: "media-job-activation", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer eventConsumer.Close()
	counterConsumer, err := events.NewConsumer(brokerConfig, events.ConsumerConfig{
		Topic: "domain-events", Group: "sea-music-social-counters-v1",
		Name: "social-counters", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer counterConsumer.Close()
	hotConsumer, err := events.NewConsumer(brokerConfig, events.ConsumerConfig{
		Topic: "domain-events", Group: "sea-music-hot-ranking-v1",
		Name: "hot-ranking", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer hotConsumer.Close()
	moderationConsumer, err := events.NewConsumer(brokerConfig, events.ConsumerConfig{
		Topic: "domain-events", Group: "sea-music-moderation-dispatch-v1",
		Name: "moderation-dispatch", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), eventRepository)
	if err != nil {
		return err
	}
	defer moderationConsumer.Close()
	transportCredentials, err := moderationClientCredentials(cfg)
	if err != nil {
		return err
	}
	moderationConnection, err := grpc.NewClient(cfg.Moderation.AgentAddress,
		grpc.WithTransportCredentials(transportCredentials), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		return fmt.Errorf("create moderation gRPC client: %w", err)
	}
	defer moderationConnection.Close()
	moderationClient := grpcadapter.NewClient(moderationv1.NewModerationServiceClient(moderationConnection), cfg.Moderation.RPCTimeout)
	moderationDispatcher := moderation.NewDispatcher(database, moderationClient, workerID, cfg.ObjectStore.Bucket, cfg.Moderation.PolicyVersion, moderation.Mode(cfg.Moderation.Mode), cfg.Moderation.LeaseDuration, cfg.Moderation.PollInterval)

	counterProjector := social.NewCounterProjector(redisClient)
	counterReconciler := social.NewCounterReconciler(database, redisClient)
	hotProjector := discovery.NewHotProjector(database, redisClient, 24*time.Hour)

	logger.Info("worker started", "worker_id", workerID)
	defer logger.Info("worker stopped", "worker_id", workerID)
	var wait sync.WaitGroup
	wait.Add(9)
	go func() {
		defer wait.Done()
		runMediaLoop(ctx, service, cfg.Worker.PollInterval, logger)
	}()
	go func() {
		defer wait.Done()
		runReconciliationLoop(ctx, counterReconciler, cfg.Social.ReconcileInterval, cfg.Social.ReconcileBatch, logger)
	}()
	go func() {
		defer wait.Done()
		runQueuedActivationLoop(ctx, repository, cfg.Worker.QueuedActivationInterval, cfg.Worker.QueuedActivationThreshold, logger)
	}()
	go func() {
		defer wait.Done()
		runConsumerLoop(ctx, eventConsumer, logger)
	}()
	go func() {
		defer wait.Done()
		runCounterLoop(ctx, counterConsumer, counterProjector, logger)
	}()
	go func() {
		defer wait.Done()
		runHotLoop(ctx, hotConsumer, hotProjector, logger)
	}()
	go func() {
		defer wait.Done()
		runEventLoop(ctx, dispatcher, cfg.Events.PollInterval, logger)
	}()
	go func() {
		defer wait.Done()
		runModerationConsumerLoop(ctx, moderationConsumer, logger)
	}()
	go func() {
		defer wait.Done()
		runModerationDispatchLoop(ctx, moderationDispatcher, cfg.Moderation.PollInterval, logger)
	}()
	<-ctx.Done()
	wait.Wait()
	return nil
}

// moderationClientCredentials returns insecure gRPC transport credentials when configured; otherwise it loads the client certificate and private CA to create TLS 1.2-or-newer credentials for the configured server name.
func moderationClientCredentials(cfg config.Config) (credentials.TransportCredentials, error) {
	if cfg.Moderation.Insecure {
		return insecure.NewCredentials(), nil
	}
	certificate, err := tls.LoadX509KeyPair(cfg.Moderation.TLSCertFile, cfg.Moderation.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load moderation client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.Moderation.TLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read moderation server CA: %w", err)
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("parse moderation server CA")
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate},
		RootCAs: rootCAs, ServerName: cfg.Moderation.TLSServerName,
	}), nil
}
