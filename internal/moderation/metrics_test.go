package moderation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sealessland/sea-music/internal/moderation"
)

func TestInstrumentedEvaluatorExportsVerdictAndFailedPolicyChecks(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := moderation.NewAgentMetrics(registry)
	agent, err := moderation.NewAgentEvaluator(
		staticEvaluator{result: candidate(moderation.VerdictApprove, 0.99, "safe")},
		staticCritic{result: candidate(moderation.VerdictReject, 0.98, "unsafe")},
		moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
	)
	if err != nil {
		t.Fatal(err)
	}
	evaluator := moderation.InstrumentEvaluator(agent, metrics)
	if _, err := evaluator.Evaluate(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}

	want := `
# HELP sea_music_moderation_agent_evaluations_total Completed agent evaluations by verdict and strategy.
# TYPE sea_music_moderation_agent_evaluations_total counter
sea_music_moderation_agent_evaluations_total{strategy="reviewer-critic-v1",verdict="escalate"} 1
# HELP sea_music_moderation_agent_policy_check_failures_total Failed deterministic policy checks.
# TYPE sea_music_moderation_agent_policy_check_failures_total counter
sea_music_moderation_agent_policy_check_failures_total{check="confidence_threshold"} 1
sea_music_moderation_agent_policy_check_failures_total{check="verdict_consensus"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want),
		"sea_music_moderation_agent_evaluations_total",
		"sea_music_moderation_agent_policy_check_failures_total",
	); err != nil {
		t.Fatal(err)
	}
}
