package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sealessland/sea-music/internal/discovery"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/social"
	"github.com/sealessland/sea-music/internal/video"
)

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
