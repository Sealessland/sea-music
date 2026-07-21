package grpcadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	moderationv1 "github.com/sealessland/sea-music/internal/gen/moderation/v1"
	"github.com/sealessland/sea-music/internal/moderation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Client struct {
	client  moderationv1.ModerationServiceClient
	timeout time.Duration
}

// NewClient returns a moderation client that applies timeout to each gRPC call.
func NewClient(client moderationv1.ModerationServiceClient, timeout time.Duration) *Client {
	return &Client{client: client, timeout: timeout}
}

// StartReview submits the request under the client's timeout and converts the initial operation, rejecting an unusable client and mapping recognized gRPC errors to moderation errors.
func (client *Client) StartReview(ctx context.Context, request moderation.ReviewRequest) (moderation.Operation, error) {
	if client == nil || client.client == nil || client.timeout <= 0 {
		return moderation.Operation{}, errors.New("invalid moderation gRPC client")
	}
	assets := make([]*moderationv1.MediaAsset, 0, len(request.Assets))
	for _, asset := range request.Assets {
		assets = append(assets, &moderationv1.MediaAsset{Kind: asset.Kind, Uri: asset.URI, Sha256: asset.SHA256, MediaType: asset.MediaType})
	}
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	operation, err := client.client.StartReview(callContext, &moderationv1.StartReviewRequest{
		RequestId: request.RequestID, VideoId: request.VideoID, VideoVersion: request.VideoVersion,
		PolicyVersion: request.PolicyVersion, Mode: toProtoMode(request.Mode), Title: request.Title,
		Description: request.Description, Assets: assets,
	})
	if err != nil {
		return moderation.Operation{}, clientError(err)
	}
	return fromProtoOperation(operation.GetOperation())
}

// GetReview fetches and converts an operation under the client's timeout, rejecting an unusable client or blank ID and mapping recognized gRPC errors to moderation errors.
func (client *Client) GetReview(ctx context.Context, operationID string) (moderation.Operation, error) {
	if client == nil || client.client == nil || client.timeout <= 0 || strings.TrimSpace(operationID) == "" {
		return moderation.Operation{}, moderation.ErrInvalidRequest
	}
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	operation, err := client.client.GetReview(callContext, &moderationv1.GetReviewRequest{OperationId: operationID})
	if err != nil {
		return moderation.Operation{}, clientError(err)
	}
	return fromProtoOperation(operation.GetOperation())
}

// toProtoMode maps supported moderation modes to their protobuf equivalents and returns UNSPECIFIED for unknown values.
func toProtoMode(mode moderation.Mode) moderationv1.ModerationMode {
	switch mode {
	case moderation.ModeShadow:
		return moderationv1.ModerationMode_MODERATION_MODE_SHADOW
	case moderation.ModeEnforce:
		return moderationv1.ModerationMode_MODERATION_MODE_ENFORCE
	default:
		return moderationv1.ModerationMode_MODERATION_MODE_UNSPECIFIED
	}
}

// fromProtoOperation converts a protobuf review operation, skipping nil nested entries and rejecting a missing ID, unknown status, or invalid result.
func fromProtoOperation(value *moderationv1.ReviewOperation) (moderation.Operation, error) {
	if value == nil || strings.TrimSpace(value.GetOperationId()) == "" {
		return moderation.Operation{}, errors.New("invalid moderation gRPC response")
	}
	operation := moderation.Operation{ID: value.GetOperationId(), Status: fromProtoStatus(value.GetStatus()), Error: value.GetError()}
	operation.Request.RequestID = value.GetRequestId()
	if value.Result != nil {
		findings := fromProtoFindings(value.Result.Findings)
		votes := make([]moderation.ReviewVote, 0, len(value.Result.Votes))
		for _, vote := range value.Result.Votes {
			if vote == nil {
				continue
			}
			votes = append(votes, moderation.ReviewVote{
				Stage: vote.Stage, Verdict: fromProtoVerdict(vote.Verdict), Confidence: vote.Confidence,
				Summary: vote.Summary, Findings: fromProtoFindings(vote.Findings), Provider: vote.Provider, Model: vote.Model, ModelVersion: vote.ModelVersion,
			})
		}
		checks := make([]moderation.PolicyCheck, 0, len(value.Result.Checks))
		for _, check := range value.Result.Checks {
			if check != nil {
				checks = append(checks, moderation.PolicyCheck{Code: check.Code, Passed: check.Passed, Detail: check.Detail})
			}
		}
		operation.Result = &moderation.Result{
			Verdict: fromProtoVerdict(value.Result.Verdict), Confidence: value.Result.Confidence, Summary: value.Result.Summary,
			Findings: findings, Provider: value.Result.Provider, Model: value.Result.Model,
			ModelVersion: value.Result.ModelVersion, PolicyVersion: value.Result.PolicyVersion, CanPublish: false,
			Strategy: value.Result.Strategy, Votes: votes, Checks: checks,
		}
		if err := operation.Result.Validate(); err != nil {
			return moderation.Operation{}, fmt.Errorf("invalid moderation gRPC result: %w", err)
		}
	}
	if operation.Status == "" {
		return moderation.Operation{}, errors.New("invalid moderation gRPC response status")
	}
	return operation, nil
}

// fromProtoFindings converts protobuf policy findings while omitting nil entries.
func fromProtoFindings(values []*moderationv1.PolicyFinding) []moderation.Finding {
	findings := make([]moderation.Finding, 0, len(values))
	for _, finding := range values {
		if finding != nil {
			findings = append(findings, moderation.Finding{Code: finding.Code, Category: finding.Category, Score: finding.Score, TimestampMS: finding.TimestampMs})
		}
	}
	return findings
}

// fromProtoStatus maps recognized protobuf review statuses to domain statuses and returns an empty status for unknown values.
func fromProtoStatus(value moderationv1.ReviewStatus) moderation.Status {
	switch value {
	case moderationv1.ReviewStatus_REVIEW_STATUS_PENDING:
		return moderation.StatusPending
	case moderationv1.ReviewStatus_REVIEW_STATUS_RUNNING:
		return moderation.StatusRunning
	case moderationv1.ReviewStatus_REVIEW_STATUS_COMPLETED:
		return moderation.StatusCompleted
	case moderationv1.ReviewStatus_REVIEW_STATUS_FAILED:
		return moderation.StatusFailed
	case moderationv1.ReviewStatus_REVIEW_STATUS_CANCELLED:
		return moderation.StatusCancelled
	default:
		return ""
	}
}

// fromProtoVerdict maps recognized protobuf review verdicts to domain verdicts and returns an empty verdict for unknown values.
func fromProtoVerdict(value moderationv1.ReviewVerdict) moderation.Verdict {
	switch value {
	case moderationv1.ReviewVerdict_REVIEW_VERDICT_APPROVE:
		return moderation.VerdictApprove
	case moderationv1.ReviewVerdict_REVIEW_VERDICT_REJECT:
		return moderation.VerdictReject
	case moderationv1.ReviewVerdict_REVIEW_VERDICT_ESCALATE:
		return moderation.VerdictEscalate
	default:
		return ""
	}
}

// clientError wraps recognized gRPC status errors with their corresponding moderation sentinel error and otherwise returns the original error.
func clientError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return errors.Join(moderation.ErrInvalidRequest, err)
	case codes.AlreadyExists:
		return errors.Join(moderation.ErrIdempotencyConflict, err)
	case codes.NotFound:
		return errors.Join(moderation.ErrOperationNotFound, err)
	default:
		return err
	}
}
