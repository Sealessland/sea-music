package moderation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/moderation"
)

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

func newExecutionStore(request moderation.ReviewRequest) *executionStore {
	return &executionStore{operation: moderation.Operation{ID: "operation-1", Request: request, Status: moderation.StatusPending}}
}

func (store *executionStore) Create(context.Context, moderation.ReviewRequest, string) (moderation.Operation, error) {
	return moderation.Operation{}, errors.New("not used")
}

func (store *executionStore) Get(context.Context, string) (moderation.Operation, error) {
	return store.operation, nil
}

func (store *executionStore) Complete(_ context.Context, _ string, result moderation.Result) (moderation.Operation, error) {
	store.operation.Status = moderation.StatusCompleted
	store.operation.Result = &result
	return store.operation, nil
}

func (store *executionStore) CompleteClaimed(_ context.Context, _ string, workerID string, result moderation.Result) (moderation.Operation, error) {
	if workerID != "agent-1" {
		return moderation.Operation{}, moderation.ErrLeaseLost
	}
	store.operation.Status = moderation.StatusCompleted
	store.operation.Result = &result
	return store.operation, nil
}

func (store *executionStore) Claim(context.Context, string, time.Duration) (moderation.Operation, error) {
	if store.operation.ID == "" {
		return moderation.Operation{}, moderation.ErrNoOperation
	}
	store.operation.Status = moderation.StatusRunning
	return store.operation, nil
}

func (store *executionStore) Fail(context.Context, string, string, error, time.Duration) error {
	store.failures++
	store.operation.Status = moderation.StatusPending
	return nil
}

type invalidResultEvaluator struct{}

func (invalidResultEvaluator) Evaluate(context.Context, moderation.ReviewRequest) (moderation.Result, error) {
	return moderation.Result{Verdict: moderation.VerdictApprove, Confidence: 2}, nil
}

type countingEvaluator struct {
	calls int
}

func (evaluator *countingEvaluator) Evaluate(context.Context, moderation.ReviewRequest) (moderation.Result, error) {
	evaluator.calls++
	return moderation.Result{}, errors.New("unexpected evaluator call")
}
