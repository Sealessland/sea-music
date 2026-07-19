package moderation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sealessland/sea-music/internal/moderation"
)

func TestAgentEscalatesReviewerCriticDisagreementWithAuditTrail(t *testing.T) {
	agent, err := moderation.NewAgentEvaluator(
		staticEvaluator{result: candidate(moderation.VerdictApprove, 0.99, "reviewer found no violation")},
		staticCritic{result: candidate(moderation.VerdictReject, 0.98, "critic found targeted hate")},
		moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
	)
	if err != nil {
		t.Fatalf("NewAgentEvaluator() error = %v", err)
	}

	result, err := agent.Evaluate(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.Verdict != moderation.VerdictEscalate {
		t.Fatalf("Verdict = %q, want escalate", result.Verdict)
	}
	if result.Strategy != "reviewer-critic-v1" || len(result.Votes) != 2 {
		t.Fatalf("audit trail = %+v", result)
	}
	if check, ok := findCheck(result.Checks, "verdict_consensus"); !ok || check.Passed {
		t.Fatalf("consensus check = %+v, found=%v", check, ok)
	}
	if result.CanPublish {
		t.Fatal("agent result unexpectedly has publication authority")
	}
}

func TestAgentEscalatesUnanimousApproveBelowPolicyThreshold(t *testing.T) {
	agent, err := moderation.NewAgentEvaluator(
		staticEvaluator{result: candidate(moderation.VerdictApprove, 0.89, "likely safe")},
		staticCritic{result: candidate(moderation.VerdictApprove, 0.93, "no violation found")},
		moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Evaluate(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != moderation.VerdictEscalate || result.Confidence != 0.89 {
		t.Fatalf("result = %+v", result)
	}
	if check, ok := findCheck(result.Checks, "confidence_threshold"); !ok || check.Passed {
		t.Fatalf("threshold check = %+v, found=%v", check, ok)
	}
}

func TestAgentRejectsOnlyUnanimousHighConfidenceEvidence(t *testing.T) {
	reviewer := candidate(moderation.VerdictReject, 0.98, "targeted hate")
	reviewer.Findings = []moderation.Finding{{Code: "hate_targeted", Category: "hate", Score: 0.98}}
	critic := candidate(moderation.VerdictReject, 0.96, "evidence supports targeted hate")
	critic.Findings = []moderation.Finding{{Code: "hate_targeted", Category: "hate", Score: 0.96}}
	agent, err := moderation.NewAgentEvaluator(
		staticEvaluator{result: reviewer}, staticCritic{result: critic},
		moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Evaluate(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != moderation.VerdictReject || result.Confidence != 0.96 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Findings) != 1 || result.Findings[0].Score != 0.98 {
		t.Fatalf("merged findings = %+v", result.Findings)
	}
}

func TestAgentPropagatesCriticFailureForDurableRetry(t *testing.T) {
	want := errors.New("critic unavailable")
	agent, err := moderation.NewAgentEvaluator(
		staticEvaluator{result: candidate(moderation.VerdictApprove, 0.99, "safe")},
		staticCritic{err: want},
		moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Evaluate(context.Background(), validRequest()); !errors.Is(err, want) {
		t.Fatalf("Evaluate() error = %v, want critic error", err)
	}
}

func candidate(verdict moderation.Verdict, confidence float64, summary string) moderation.Result {
	return moderation.Result{
		Verdict: verdict, Confidence: confidence, Summary: summary,
		Provider: "openai", Model: "test-model", PolicyVersion: "ugc-v1",
	}
}

func findCheck(checks []moderation.PolicyCheck, code string) (moderation.PolicyCheck, bool) {
	for _, check := range checks {
		if check.Code == code {
			return check, true
		}
	}
	return moderation.PolicyCheck{}, false
}

type staticEvaluator struct {
	result moderation.Result
	err    error
}

func (e staticEvaluator) Evaluate(context.Context, moderation.ReviewRequest) (moderation.Result, error) {
	return e.result, e.err
}

type staticCritic struct {
	result moderation.Result
	err    error
}

func (c staticCritic) Critique(context.Context, moderation.ReviewRequest, moderation.Result) (moderation.Result, error) {
	return c.result, c.err
}
