package moderation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const reviewerCriticStrategy = "reviewer-critic-v1"

var agentTracer = otel.Tracer("github.com/sealessland/sea-music/internal/moderation/agent")

// Critic independently challenges a reviewer's candidate using the original
// request. It must not mutate external state.
type Critic interface {
	Critique(context.Context, ReviewRequest, Result) (Result, error)
}

// DecisionPolicy is deliberately small and deterministic so a stored result
// can be replayed without calling a model again.
type DecisionPolicy struct {
	ApproveThreshold float64
	RejectThreshold  float64
}

func (policy DecisionPolicy) Validate() error {
	if policy.ApproveThreshold <= 0 || policy.ApproveThreshold > 1 ||
		policy.RejectThreshold <= 0 || policy.RejectThreshold > 1 {
		return errors.New("moderation decision thresholds must be within (0,1]")
	}
	return nil
}

// AgentEvaluator runs a reviewer and a separate critic, then applies policy
// gates. Models provide evidence; this type owns the final evidence verdict.
type AgentEvaluator struct {
	reviewer Evaluator
	critic   Critic
	policy   DecisionPolicy
}

func NewAgentEvaluator(reviewer Evaluator, critic Critic, policy DecisionPolicy) (*AgentEvaluator, error) {
	if reviewer == nil || critic == nil {
		return nil, errors.New("moderation reviewer and critic are required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &AgentEvaluator{reviewer: reviewer, critic: critic, policy: policy}, nil
}

func (agent *AgentEvaluator) Evaluate(ctx context.Context, request ReviewRequest) (Result, error) {
	if agent == nil || agent.reviewer == nil || agent.critic == nil {
		return Result{}, errors.New("moderation agent evaluator is required")
	}
	ctx, span := agentTracer.Start(ctx, "moderation.agent.evaluate")
	defer span.End()
	span.SetAttributes(
		attribute.String("moderation.strategy", reviewerCriticStrategy),
		attribute.String("moderation.mode", string(request.Mode)),
		attribute.String("moderation.policy_version", request.PolicyVersion),
	)
	reviewerContext, reviewerSpan := agentTracer.Start(ctx, "moderation.agent.reviewer")
	review, err := agent.reviewer.Evaluate(reviewerContext, request)
	if err != nil {
		reviewerSpan.RecordError(err)
		reviewerSpan.SetStatus(codes.Error, "reviewer failed")
		reviewerSpan.End()
		span.SetStatus(codes.Error, "reviewer failed")
		return Result{}, fmt.Errorf("reviewer stage: %w", err)
	}
	if err := review.Validate(); err != nil {
		reviewerSpan.RecordError(err)
		reviewerSpan.SetStatus(codes.Error, "reviewer returned invalid evidence")
		reviewerSpan.End()
		span.SetStatus(codes.Error, "reviewer returned invalid evidence")
		return Result{}, fmt.Errorf("reviewer stage: %w", err)
	}
	reviewerSpan.SetAttributes(attribute.String("moderation.verdict", string(review.Verdict)), attribute.Float64("moderation.confidence", review.Confidence))
	reviewerSpan.End()
	criticContext, criticSpan := agentTracer.Start(ctx, "moderation.agent.critic")
	critique, err := agent.critic.Critique(criticContext, request, review)
	if err != nil {
		criticSpan.RecordError(err)
		criticSpan.SetStatus(codes.Error, "critic failed")
		criticSpan.End()
		span.SetStatus(codes.Error, "critic failed")
		return Result{}, fmt.Errorf("critic stage: %w", err)
	}
	if err := critique.Validate(); err != nil {
		criticSpan.RecordError(err)
		criticSpan.SetStatus(codes.Error, "critic returned invalid evidence")
		criticSpan.End()
		span.SetStatus(codes.Error, "critic returned invalid evidence")
		return Result{}, fmt.Errorf("critic stage: %w", err)
	}
	criticSpan.SetAttributes(attribute.String("moderation.verdict", string(critique.Verdict)), attribute.Float64("moderation.confidence", critique.Confidence))
	criticSpan.End()
	result := agent.reconcile(request, review, critique)
	span.SetAttributes(
		attribute.String("moderation.verdict", string(result.Verdict)),
		attribute.Float64("moderation.confidence", result.Confidence),
		attribute.Bool("moderation.consensus", review.Verdict == critique.Verdict),
	)
	return result, nil
}

func (agent *AgentEvaluator) reconcile(request ReviewRequest, review, critique Result) Result {
	confidence := math.Min(review.Confidence, critique.Confidence)
	consensus := review.Verdict == critique.Verdict
	threshold := agent.policy.ApproveThreshold
	if review.Verdict == VerdictReject {
		threshold = agent.policy.RejectThreshold
	}
	thresholdPassed := consensus && review.Verdict != VerdictEscalate && confidence >= threshold
	verdict := VerdictEscalate
	if thresholdPassed {
		verdict = review.Verdict
	}

	summary := "reviewer and critic require human review"
	switch {
	case !consensus:
		summary = fmt.Sprintf("reviewer/critic disagreement (%s vs %s); human review required", review.Verdict, critique.Verdict)
	case review.Verdict == VerdictEscalate:
		summary = "reviewer and critic agree that context requires human review"
	case !thresholdPassed:
		summary = fmt.Sprintf("unanimous %s evidence is below %.2f confidence threshold; human review required", review.Verdict, threshold)
	default:
		summary = fmt.Sprintf("reviewer and critic unanimously recommend %s", verdict)
	}

	return Result{
		Verdict: verdict, Confidence: confidence, Summary: summary,
		Findings:      mergeFindings(review.Findings, critique.Findings),
		Provider:      combineIdentity(review.Provider, critique.Provider),
		Model:         combineIdentity(review.Model, critique.Model),
		ModelVersion:  combineIdentity(review.ModelVersion, critique.ModelVersion),
		PolicyVersion: request.PolicyVersion, CanPublish: false,
		Strategy: reviewerCriticStrategy,
		Votes:    []ReviewVote{voteFrom("reviewer", review), voteFrom("critic", critique)},
		Checks: []PolicyCheck{
			{Code: "verdict_consensus", Passed: consensus, Detail: fmt.Sprintf("reviewer=%s critic=%s", review.Verdict, critique.Verdict)},
			{Code: "confidence_threshold", Passed: thresholdPassed, Detail: fmt.Sprintf("confidence=%.4f threshold=%.4f", confidence, threshold)},
			{Code: "publication_authority", Passed: true, Detail: "agent evidence cannot publish content"},
		},
	}
}

func voteFrom(stage string, result Result) ReviewVote {
	return ReviewVote{
		Stage: stage, Verdict: result.Verdict, Confidence: result.Confidence,
		Summary: result.Summary, Findings: result.Findings,
		Provider: result.Provider, Model: result.Model, ModelVersion: result.ModelVersion,
	}
}

func mergeFindings(groups ...[]Finding) []Finding {
	merged := make([]Finding, 0)
	positions := make(map[string]int)
	for _, findings := range groups {
		for _, finding := range findings {
			key := finding.Code + "\x00" + finding.Category + fmt.Sprint("\x00", finding.TimestampMS)
			if position, ok := positions[key]; ok {
				if finding.Score > merged[position].Score {
					merged[position] = finding
				}
				continue
			}
			positions[key] = len(merged)
			merged = append(merged, finding)
		}
	}
	return merged
}

func combineIdentity(left, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == right || right == "" {
		return left
	}
	if left == "" {
		return right
	}
	return left + "+" + right
}
