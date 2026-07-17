package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/sealessland/sea-music/internal/platform/config"
	"github.com/sealessland/sea-music/internal/platform/logging"
	"github.com/sealessland/sea-music/internal/platform/telemetry"
	"github.com/sealessland/sea-music/internal/social"
	"github.com/sealessland/sea-music/internal/video"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

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
	repository := video.NewPostgresRepository(database)
	processor := video.NewFFmpegProcessor(store, cfg.Worker.FFprobePath, cfg.Worker.FFmpegPath, cfg.Worker.MediaTimeout, cfg.ObjectStore.MaxUploadBytes)
	service := video.NewProcessingService(repository, processor, workerID, cfg.Worker.LeaseDuration)
	publisher, err := events.NewKafkaPublisher(cfg.Broker.Brokers)
	if err != nil {
		return err
	}
	defer publisher.Close()
	if err := publisher.Ping(ctx); err != nil {
		return fmt.Errorf("ping Kafka broker: %w", err)
	}
	dispatcher := events.NewDispatcher(events.NewPostgresRepository(database), publisher, workerID, cfg.Events.BatchSize, cfg.Events.LeaseDuration)
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
	eventConsumer, err := events.NewKafkaConsumer(events.ConsumerConfig{
		Brokers: cfg.Broker.Brokers, Topic: "domain-events", Group: "sea-music-media-processing-v1",
		Name: "media-job-activation", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer eventConsumer.Close()
	counterConsumer, err := events.NewKafkaConsumer(events.ConsumerConfig{
		Brokers: cfg.Broker.Brokers, Topic: "domain-events", Group: "sea-music-social-counters-v1",
		Name: "social-counters", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer counterConsumer.Close()
	counterProjector := social.NewCounterProjector(redisClient)
	counterReconciler := social.NewCounterReconciler(database, redisClient)
	hotConsumer, err := events.NewKafkaConsumer(events.ConsumerConfig{
		Brokers: cfg.Broker.Brokers, Topic: "domain-events", Group: "sea-music-hot-ranking-v1",
		Name: "hot-ranking", MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		return err
	}
	defer hotConsumer.Close()
	hotProjector := discovery.NewHotProjector(database, redisClient, 24*time.Hour)
	logger.Info("worker started", "worker_id", workerID)
	defer logger.Info("worker stopped", "worker_id", workerID)
	var wait sync.WaitGroup
	wait.Add(7)
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
	<-ctx.Done()
	wait.Wait()
	return nil
}

func runHotLoop(ctx context.Context, consumer *events.KafkaConsumer, projector *discovery.HotProjector, logger *slog.Logger) {
	handler := func(ctx context.Context, transaction *sql.Tx, envelope events.Envelope) error {
		return projector.Handle(ctx, transaction, discovery.EngagementEvent{ID: envelope.ID, Type: envelope.Type, OccurredAt: envelope.OccurredAt, Data: envelope.Data})
	}
	for {
		processed, err := consumer.RunOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "hot ranking event consumption failed", "error", err)
		} else if processed {
			logger.InfoContext(ctx, "hot ranking event consumed")
		}
	}
}

func runReconciliationLoop(ctx context.Context, reconciler *social.CounterReconciler, interval time.Duration, batch int, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checked, drift, err := reconciler.ReconcileBatch(ctx, batch)
			if err != nil {
				logger.ErrorContext(ctx, "counter reconciliation failed", "error", err)
			} else if drift > 0 {
				logger.WarnContext(ctx, "counter drift repaired", "videos", checked, "drift_total", drift)
			}
		}
	}
}

func runQueuedActivationLoop(ctx context.Context, repository *video.PostgresRepository, interval, threshold time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			activated, err := repository.ActivateStaleQueuedJobs(ctx, threshold)
			if err != nil {
				logger.ErrorContext(ctx, "queued processing job activation failed", "error", err)
			} else if activated > 0 {
				logger.WarnContext(ctx, "queued processing jobs activated without finalize event", "jobs", activated)
			}
		}
	}
}

func runCounterLoop(ctx context.Context, consumer *events.KafkaConsumer, projector *social.CounterProjector, logger *slog.Logger) {
	handler := func(ctx context.Context, transaction *sql.Tx, envelope events.Envelope) error {
		return projector.Handle(ctx, transaction, social.CounterEvent{ID: envelope.ID, Type: envelope.Type, Data: envelope.Data})
	}
	for {
		processed, err := consumer.RunOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "counter event consumption failed", "error", err)
		} else if processed {
			logger.InfoContext(ctx, "counter event consumed")
		}
	}
}

func runConsumerLoop(ctx context.Context, consumer *events.KafkaConsumer, logger *slog.Logger) {
	handler := func(ctx context.Context, transaction *sql.Tx, envelope events.Envelope) error {
		if envelope.Type != "video.source_finalized" {
			return nil
		}
		var data struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			return fmt.Errorf("decode media activation event: %w", err)
		}
		return video.ActivateProcessingJobTx(ctx, transaction, data.JobID)
	}
	for {
		processed, err := consumer.RunOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "domain event consumption failed", "error", err)
		} else if processed {
			logger.InfoContext(ctx, "domain event consumed")
		}
	}
}

func runMediaLoop(ctx context.Context, service *video.ProcessingService, pollInterval time.Duration, logger *slog.Logger) {
	for {
		_, err := service.RunOnce(ctx)
		switch {
		case err == nil:
			logger.InfoContext(ctx, "media processing job completed")
		case errors.Is(err, video.ErrNoProcessingJob):
			timer := time.NewTimer(pollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		case ctx.Err() != nil:
			return
		default:
			logger.ErrorContext(ctx, "media processing job failed", "error", err)
		}
	}
}

func runEventLoop(ctx context.Context, dispatcher *events.Dispatcher, pollInterval time.Duration, logger *slog.Logger) {
	for {
		count, err := dispatcher.RunOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "outbox dispatch failed", "error", err)
		} else if count > 0 {
			logger.InfoContext(ctx, "outbox batch dispatched", "events", count)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
