package moderation

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type AgentMetrics struct {
	evaluations        *prometheus.CounterVec
	errors             *prometheus.CounterVec
	policyCheckFailure *prometheus.CounterVec
	duration           *prometheus.HistogramVec
}

func NewAgentMetrics(registerer prometheus.Registerer) *AgentMetrics {
	metrics := &AgentMetrics{
		evaluations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_moderation_agent_evaluations_total",
			Help: "Completed agent evaluations by verdict and strategy.",
		}, []string{"verdict", "strategy"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_moderation_agent_errors_total",
			Help: "Agent evaluation failures by bounded error kind.",
		}, []string{"kind"}),
		policyCheckFailure: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_moderation_agent_policy_check_failures_total",
			Help: "Failed deterministic policy checks.",
		}, []string{"check"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sea_music_moderation_agent_evaluation_duration_seconds",
			Help:    "End-to-end agent evaluation latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome", "strategy"}),
	}
	registerer.MustRegister(metrics.evaluations, metrics.errors, metrics.policyCheckFailure, metrics.duration)
	return metrics
}

type instrumentedEvaluator struct {
	next    Evaluator
	metrics *AgentMetrics
}

func InstrumentEvaluator(next Evaluator, metrics *AgentMetrics) Evaluator {
	return &instrumentedEvaluator{next: next, metrics: metrics}
}

func (evaluator *instrumentedEvaluator) Evaluate(ctx context.Context, request ReviewRequest) (Result, error) {
	started := time.Now()
	result, err := evaluator.next.Evaluate(ctx, request)
	if err != nil {
		if evaluator.metrics != nil {
			evaluator.metrics.errors.WithLabelValues(agentErrorKind(err)).Inc()
			evaluator.metrics.duration.WithLabelValues("error", "unknown").Observe(time.Since(started).Seconds())
		}
		return Result{}, err
	}
	strategy := result.Strategy
	if strategy == "" {
		strategy = "single-pass"
	}
	if evaluator.metrics != nil {
		evaluator.metrics.evaluations.WithLabelValues(string(result.Verdict), strategy).Inc()
		for _, check := range result.Checks {
			if !check.Passed {
				evaluator.metrics.policyCheckFailure.WithLabelValues(check.Code).Inc()
			}
		}
		evaluator.metrics.duration.WithLabelValues("success", strategy).Observe(time.Since(started).Seconds())
	}
	return result, nil
}

func agentErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrInvalidResult):
		return "invalid_result"
	default:
		return "provider"
	}
}
