package moderation_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/sealessland/sea-music/internal/moderation"
)

func TestDecisionPolicyEvalCorpus(t *testing.T) {
	encoded, err := os.ReadFile("testdata/decision_policy_eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name               string             `json:"name"`
		ReviewerVerdict    moderation.Verdict `json:"reviewer_verdict"`
		ReviewerConfidence float64            `json:"reviewer_confidence"`
		CriticVerdict      moderation.Verdict `json:"critic_verdict"`
		CriticConfidence   float64            `json:"critic_confidence"`
		Want               moderation.Verdict `json:"want"`
	}
	if err := json.Unmarshal(encoded, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6 {
		t.Fatalf("eval corpus has %d cases, want at least 6", len(cases))
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			agent, err := moderation.NewAgentEvaluator(
				staticEvaluator{result: candidate(test.ReviewerVerdict, test.ReviewerConfidence, "reviewer evidence")},
				staticCritic{result: candidate(test.CriticVerdict, test.CriticConfidence, "critic evidence")},
				moderation.DecisionPolicy{ApproveThreshold: 0.90, RejectThreshold: 0.95},
			)
			if err != nil {
				t.Fatal(err)
			}
			result, err := agent.Evaluate(context.Background(), validRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != test.Want {
				t.Fatalf("verdict = %q, want %q", result.Verdict, test.Want)
			}
		})
	}
}
