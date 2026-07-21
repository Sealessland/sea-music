package grpcadapter

import (
	"context"
	"errors"

	moderationv1 "github.com/sealessland/sea-music/internal/gen/moderation/v1"
	"github.com/sealessland/sea-music/internal/moderation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	moderationv1.UnimplementedModerationServiceServer
	service *moderation.Service
}

// NewServer creates a gRPC moderation server backed by service.
func NewServer(service *moderation.Service) *Server {
	return &Server{service: service}
}

// StartReview validates and converts the request, starts a moderation review, and returns its initial operation state with domain failures mapped to gRPC status errors.
func (server *Server) StartReview(ctx context.Context, request *moderationv1.StartReviewRequest) (*moderationv1.StartReviewResponse, error) {
	if request == nil || server == nil || server.service == nil {
		return nil, status.Error(codes.InvalidArgument, "moderation request is required")
	}
	assets := make([]moderation.Asset, 0, len(request.GetAssets()))
	for _, asset := range request.GetAssets() {
		if asset == nil {
			return nil, status.Error(codes.InvalidArgument, "moderation asset is required")
		}
		assets = append(assets, moderation.Asset{Kind: asset.GetKind(), URI: asset.GetUri(), SHA256: asset.GetSha256(), MediaType: asset.GetMediaType()})
	}
	operation, err := server.service.StartReview(ctx, moderation.ReviewRequest{
		RequestID: request.GetRequestId(), VideoID: request.GetVideoId(), VideoVersion: request.GetVideoVersion(),
		PolicyVersion: request.GetPolicyVersion(), Mode: fromProtoMode(request.GetMode()),
		Title: request.GetTitle(), Description: request.GetDescription(), Assets: assets,
	})
	if err != nil {
		return nil, domainError(err)
	}
	return &moderationv1.StartReviewResponse{Operation: toProtoOperation(operation)}, nil
}

// GetReview retrieves an operation by ID and returns its protobuf representation, mapping domain failures to gRPC status errors.
func (server *Server) GetReview(ctx context.Context, request *moderationv1.GetReviewRequest) (*moderationv1.GetReviewResponse, error) {
	if request == nil || server == nil || server.service == nil {
		return nil, status.Error(codes.InvalidArgument, "operation id is required")
	}
	operation, err := server.service.GetReview(ctx, request.GetOperationId())
	if err != nil {
		return nil, domainError(err)
	}
	return &moderationv1.GetReviewResponse{Operation: toProtoOperation(operation)}, nil
}

// domainError maps known moderation errors to corresponding gRPC status codes and hides unrecognized failures behind a generic Internal error.
func domainError(err error) error {
	switch {
	case errors.Is(err, moderation.ErrInvalidRequest), errors.Is(err, moderation.ErrInvalidResult):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, moderation.ErrIdempotencyConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, moderation.ErrOperationNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, "moderation operation failed")
	}
}

// fromProtoMode converts supported protobuf moderation modes to domain modes and returns the empty mode for unspecified or unknown values.
func fromProtoMode(mode moderationv1.ModerationMode) moderation.Mode {
	switch mode {
	case moderationv1.ModerationMode_MODERATION_MODE_SHADOW:
		return moderation.ModeShadow
	case moderationv1.ModerationMode_MODERATION_MODE_ENFORCE:
		return moderation.ModeEnforce
	default:
		return ""
	}
}

// toProtoOperation converts a domain operation and its optional nested result, findings, votes, and checks to protobuf messages.
func toProtoOperation(operation moderation.Operation) *moderationv1.ReviewOperation {
	result := &moderationv1.ReviewOperation{
		OperationId: operation.ID,
		RequestId:   operation.Request.RequestID,
		Status:      toProtoStatus(operation.Status),
		Error:       operation.Error,
	}
	if operation.Result != nil {
		findings := make([]*moderationv1.PolicyFinding, 0, len(operation.Result.Findings))
		for _, finding := range operation.Result.Findings {
			findings = append(findings, toProtoFinding(finding))
		}
		votes := make([]*moderationv1.ReviewVote, 0, len(operation.Result.Votes))
		for _, vote := range operation.Result.Votes {
			voteFindings := make([]*moderationv1.PolicyFinding, 0, len(vote.Findings))
			for _, finding := range vote.Findings {
				voteFindings = append(voteFindings, toProtoFinding(finding))
			}
			votes = append(votes, &moderationv1.ReviewVote{
				Stage: vote.Stage, Verdict: toProtoVerdict(vote.Verdict), Confidence: vote.Confidence,
				Summary: vote.Summary, Findings: voteFindings, Provider: vote.Provider, Model: vote.Model, ModelVersion: vote.ModelVersion,
			})
		}
		checks := make([]*moderationv1.PolicyCheck, 0, len(operation.Result.Checks))
		for _, check := range operation.Result.Checks {
			checks = append(checks, &moderationv1.PolicyCheck{Code: check.Code, Passed: check.Passed, Detail: check.Detail})
		}
		result.Result = &moderationv1.ReviewResult{
			Verdict: toProtoVerdict(operation.Result.Verdict), Confidence: operation.Result.Confidence,
			Summary: operation.Result.Summary, Findings: findings, Provider: operation.Result.Provider,
			Model: operation.Result.Model, ModelVersion: operation.Result.ModelVersion, PolicyVersion: operation.Result.PolicyVersion,
			Strategy: operation.Result.Strategy, Votes: votes, Checks: checks,
		}
	}
	return result
}

// toProtoFinding converts a domain policy finding to its protobuf representation.
func toProtoFinding(finding moderation.Finding) *moderationv1.PolicyFinding {
	return &moderationv1.PolicyFinding{
		Code: finding.Code, Category: finding.Category, Score: finding.Score, TimestampMs: finding.TimestampMS,
	}
}

// toProtoStatus converts a domain review status to its protobuf enum, falling back to REVIEW_STATUS_UNSPECIFIED for unknown values.
func toProtoStatus(value moderation.Status) moderationv1.ReviewStatus {
	switch value {
	case moderation.StatusPending:
		return moderationv1.ReviewStatus_REVIEW_STATUS_PENDING
	case moderation.StatusRunning:
		return moderationv1.ReviewStatus_REVIEW_STATUS_RUNNING
	case moderation.StatusCompleted:
		return moderationv1.ReviewStatus_REVIEW_STATUS_COMPLETED
	case moderation.StatusFailed:
		return moderationv1.ReviewStatus_REVIEW_STATUS_FAILED
	case moderation.StatusCancelled:
		return moderationv1.ReviewStatus_REVIEW_STATUS_CANCELLED
	default:
		return moderationv1.ReviewStatus_REVIEW_STATUS_UNSPECIFIED
	}
}

// toProtoVerdict converts a domain verdict to its protobuf enum, falling back to REVIEW_VERDICT_UNSPECIFIED for unknown values.
func toProtoVerdict(value moderation.Verdict) moderationv1.ReviewVerdict {
	switch value {
	case moderation.VerdictApprove:
		return moderationv1.ReviewVerdict_REVIEW_VERDICT_APPROVE
	case moderation.VerdictReject:
		return moderationv1.ReviewVerdict_REVIEW_VERDICT_REJECT
	case moderation.VerdictEscalate:
		return moderationv1.ReviewVerdict_REVIEW_VERDICT_ESCALATE
	default:
		return moderationv1.ReviewVerdict_REVIEW_VERDICT_UNSPECIFIED
	}
}
