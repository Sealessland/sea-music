package moderation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNoOperation = errors.New("no moderation operation available")
	ErrLeaseLost   = errors.New("moderation operation lease lost")
)

type Evaluator interface {
	Evaluate(context.Context, ReviewRequest) (Result, error)
}

type ExecutionStore interface {
	Store
	Claim(context.Context, string, time.Duration) (Operation, error)
	CompleteClaimed(context.Context, string, string, Result) (Operation, error)
	Fail(context.Context, string, string, error, time.Duration) error
}

type Runner struct {
	store             ExecutionStore
	evaluator         Evaluator
	workerID          string
	leaseDuration     time.Duration
	evaluationTimeout time.Duration
}

func NewRunner(store ExecutionStore, evaluator Evaluator, workerID string, leaseDuration, evaluationTimeout time.Duration) *Runner {
	return &Runner{store: store, evaluator: evaluator, workerID: workerID, leaseDuration: leaseDuration, evaluationTimeout: evaluationTimeout}
}

func (runner *Runner) RunOnce(ctx context.Context) (Operation, error) {
	if runner == nil || runner.store == nil || runner.evaluator == nil || strings.TrimSpace(runner.workerID) == "" ||
		runner.leaseDuration <= 0 || runner.evaluationTimeout <= 0 || runner.evaluationTimeout >= runner.leaseDuration {
		return Operation{}, errors.New("invalid moderation runner configuration")
	}
	operation, err := runner.store.Claim(ctx, runner.workerID, runner.leaseDuration)
	if err != nil {
		return Operation{}, err
	}
	evaluationContext, cancel := context.WithTimeout(ctx, runner.evaluationTimeout)
	defer cancel()
	result, err := runner.evaluator.Evaluate(evaluationContext, operation.Request)
	if err != nil {
		failErr := runner.store.Fail(ctx, operation.ID, runner.workerID, err, time.Second)
		if failErr != nil {
			return Operation{}, errors.Join(err, failErr)
		}
		return Operation{}, fmt.Errorf("evaluate moderation operation: %w", err)
	}
	result.PolicyVersion = operation.Request.PolicyVersion
	if err := result.Validate(); err != nil {
		failErr := runner.store.Fail(ctx, operation.ID, runner.workerID, err, time.Second)
		if failErr != nil {
			return Operation{}, errors.Join(err, failErr)
		}
		return Operation{}, err
	}
	result.CanPublish = false
	completed, err := runner.store.CompleteClaimed(ctx, operation.ID, runner.workerID, result)
	if err != nil {
		return Operation{}, fmt.Errorf("complete moderation operation: %w", err)
	}
	return completed, nil
}

type ManualEscalationEvaluator struct{}

func NewManualEscalationEvaluator() ManualEscalationEvaluator {
	return ManualEscalationEvaluator{}
}

func (ManualEscalationEvaluator) Evaluate(_ context.Context, _ ReviewRequest) (Result, error) {
	return Result{
		Verdict: VerdictEscalate, Confidence: 0,
		Summary:  "automatic moderation provider is disabled; human review required",
		Findings: []Finding{{Code: "provider_disabled", Category: "system", Score: 0}},
		Provider: "manual-fallback", Model: "none",
	}, nil
}
