package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sealessland/sea-music/internal/moderation"
)

type operationRunner interface {
	RunOnce(context.Context) (moderation.Operation, error)
}

func runModerationLoop(ctx context.Context, runner operationRunner, pollInterval time.Duration, logger *slog.Logger) {
	for {
		operation, err := runner.RunOnce(ctx)
		switch {
		case err == nil:
			logger.InfoContext(ctx, "moderation operation completed", "operation_id", operation.ID, "verdict", operation.Result.Verdict)
		case ctx.Err() != nil:
			return
		case !errors.Is(err, moderation.ErrNoOperation):
			logger.ErrorContext(ctx, "moderation operation failed", "error", err)
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
