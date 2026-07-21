package moderation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/moderation"
)

// TestRunnerPersistsSafeEscalationWhenAutomaticProviderIsDisabled verifies that the runner completes an operation with a non-publishing manual-fallback escalation when automatic moderation is unavailable.
func TestRunnerPersistsSafeEscalationWhenAutomaticProviderIsDisabled(t *testing.T) {
	store := newExecutionStore(validRequest())
	runner := moderation.NewRunner(store, moderation.NewManualEscalationEvaluator(), "agent-1", time.Minute, 10*time.Second)

	operation, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if operation.Status != moderation.StatusCompleted || operation.Result == nil {
		t.Fatalf("RunOnce() = %+v", operation)
	}
	if operation.Result.Verdict != moderation.VerdictEscalate || operation.Result.Provider != "manual-fallback" {
		t.Fatalf("fallback result = %+v", operation.Result)
	}
	if operation.Result.CanPublish {
		t.Fatal("fallback result unexpectedly has publishing authority")
	}
}

// TestRunnerReturnsNoOperationWithoutCallingEvaluator verifies that the runner returns ErrNoOperation and skips evaluation when no operation can be claimed.
func TestRunnerReturnsNoOperationWithoutCallingEvaluator(t *testing.T) {
	store := newExecutionStore(validRequest())
	store.operation = moderation.Operation{}
	evaluator := &countingEvaluator{}
	runner := moderation.NewRunner(store, evaluator, "agent-1", time.Minute, 10*time.Second)

	_, err := runner.RunOnce(context.Background())
	if !errors.Is(err, moderation.ErrNoOperation) {
		t.Fatalf("RunOnce() error = %v, want ErrNoOperation", err)
	}
	if evaluator.calls != 0 {
		t.Fatalf("evaluator calls = %d, want 0", evaluator.calls)
	}
}

// TestRunnerChargesInvalidEvaluatorResultToFailureBudget verifies that an invalid evaluation returns ErrInvalidResult, increments the failure count, and restores the operation to pending.
func TestRunnerChargesInvalidEvaluatorResultToFailureBudget(t *testing.T) {
	store := newExecutionStore(validRequest())
	runner := moderation.NewRunner(store, invalidResultEvaluator{}, "agent-1", time.Minute, 10*time.Second)
	if _, err := runner.RunOnce(context.Background()); !errors.Is(err, moderation.ErrInvalidResult) {
		t.Fatalf("RunOnce() error = %v, want ErrInvalidResult", err)
	}
	if store.failures != 1 || store.operation.Status != moderation.StatusPending {
		t.Fatalf("failure accounting = %d, operation = %+v", store.failures, store.operation)
	}
}

type executionStore struct {
	operation moderation.Operation
	failures  int
}

// newExecutionStore returns an in-memory store seeded with a pending operation for the supplied review request.
func newExecutionStore(request moderation.ReviewRequest) *executionStore {
	return &executionStore{operation: moderation.Operation{ID: "operation-1", Request: request, Status: moderation.StatusPending}}
}

// Create always returns an error because operation creation is unsupported by this test store.
func (store *executionStore) Create(context.Context, moderation.ReviewRequest, string) (moderation.Operation, error) {
	return moderation.Operation{}, errors.New("not used")
}

// Get returns the store's current operation without validating the requested identifier.
func (store *executionStore) Get(context.Context, string) (moderation.Operation, error) {
	return store.operation, nil
}

// Complete marks the current operation completed, attaches the supplied result, and returns the updated operation.
func (store *executionStore) Complete(_ context.Context, _ string, result moderation.Result) (moderation.Operation, error) {
	store.operation.Status = moderation.StatusCompleted
	store.operation.Result = &result
	return store.operation, nil
}

// CompleteClaimed completes the current operation for agent-1 and returns ErrLeaseLost for any other worker.
func (store *executionStore) CompleteClaimed(_ context.Context, _ string, workerID string, result moderation.Result) (moderation.Operation, error) {
	if workerID != "agent-1" {
		return moderation.Operation{}, moderation.ErrLeaseLost
	}
	store.operation.Status = moderation.StatusCompleted
	store.operation.Result = &result
	return store.operation, nil
}

// Claim returns ErrNoOperation when the store is empty; otherwise it marks and returns the current operation as running.
func (store *executionStore) Claim(context.Context, string, time.Duration) (moderation.Operation, error) {
	if store.operation.ID == "" {
		return moderation.Operation{}, moderation.ErrNoOperation
	}
	store.operation.Status = moderation.StatusRunning
	return store.operation, nil
}

// Fail increments the failure count and returns the current operation to pending, ignoring the supplied failure details and retry delay.
func (store *executionStore) Fail(context.Context, string, string, error, time.Duration) error {
	store.failures++
	store.operation.Status = moderation.StatusPending
	return nil
}

type invalidResultEvaluator struct{}

// Evaluate returns an approval result with an out-of-range confidence value to exercise invalid-result handling.
func (invalidResultEvaluator) Evaluate(context.Context, moderation.ReviewRequest) (moderation.Result, error) {
	return moderation.Result{Verdict: moderation.VerdictApprove, Confidence: 2}, nil
}

type countingEvaluator struct {
	calls int
}

// Evaluate increments the call count and returns an error so tests can detect any unexpected evaluation.
func (evaluator *countingEvaluator) Evaluate(context.Context, moderation.ReviewRequest) (moderation.Result, error) {
	evaluator.calls++
	return moderation.Result{}, errors.New("unexpected evaluator call")
}
