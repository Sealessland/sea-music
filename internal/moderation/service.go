// Package moderation owns agent review operations and evidence. It deliberately
// does not own video publication state transitions.
package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidRequest      = errors.New("invalid moderation request")
	ErrInvalidResult       = errors.New("invalid moderation result")
	ErrIdempotencyConflict = errors.New("moderation request idempotency conflict")
	ErrOperationNotFound   = errors.New("moderation operation not found")
)

type Mode string

const (
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Verdict string

const (
	VerdictApprove  Verdict = "approve"
	VerdictReject   Verdict = "reject"
	VerdictEscalate Verdict = "escalate"
)

type Asset struct {
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type,omitempty"`
}

type ReviewRequest struct {
	RequestID     string  `json:"request_id"`
	VideoID       string  `json:"video_id"`
	VideoVersion  int64   `json:"video_version"`
	PolicyVersion string  `json:"policy_version"`
	Mode          Mode    `json:"mode"`
	Title         string  `json:"title"`
	Description   string  `json:"description"`
	Assets        []Asset `json:"assets"`
}

type Finding struct {
	Code        string  `json:"code"`
	Category    string  `json:"category"`
	Score       float64 `json:"score"`
	TimestampMS int64   `json:"timestamp_ms,omitempty"`
}

// ReviewVote preserves one model role's evidence before deterministic policy
// reconciliation. Votes are audit data, never publication authorization.
type ReviewVote struct {
	Stage        string    `json:"stage"`
	Verdict      Verdict   `json:"verdict"`
	Confidence   float64   `json:"confidence"`
	Summary      string    `json:"summary"`
	Findings     []Finding `json:"findings,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	ModelVersion string    `json:"model_version,omitempty"`
}

// PolicyCheck records a deterministic guardrail evaluated after model calls.
type PolicyCheck struct {
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Result struct {
	Verdict       Verdict       `json:"verdict"`
	Confidence    float64       `json:"confidence"`
	Summary       string        `json:"summary"`
	Findings      []Finding     `json:"findings"`
	Provider      string        `json:"provider"`
	Model         string        `json:"model"`
	ModelVersion  string        `json:"model_version,omitempty"`
	PolicyVersion string        `json:"policy_version"`
	CanPublish    bool          `json:"can_publish"`
	Strategy      string        `json:"strategy,omitempty"`
	Votes         []ReviewVote  `json:"votes,omitempty"`
	Checks        []PolicyCheck `json:"checks,omitempty"`
}

type Operation struct {
	ID        string        `json:"id"`
	Request   ReviewRequest `json:"request"`
	InputHash string        `json:"input_hash"`
	Status    Status        `json:"status"`
	Result    *Result       `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type Store interface {
	Create(context.Context, ReviewRequest, string) (Operation, error)
	Get(context.Context, string) (Operation, error)
	Complete(context.Context, string, Result) (Operation, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (service *Service) StartReview(ctx context.Context, request ReviewRequest) (Operation, error) {
	if service == nil || service.store == nil {
		return Operation{}, errors.New("moderation store is required")
	}
	if err := request.Validate(); err != nil {
		return Operation{}, err
	}
	inputHash, err := request.Hash()
	if err != nil {
		return Operation{}, err
	}
	return service.store.Create(ctx, request, inputHash)
}

func (service *Service) GetReview(ctx context.Context, operationID string) (Operation, error) {
	if service == nil || service.store == nil || strings.TrimSpace(operationID) == "" {
		return Operation{}, ErrInvalidRequest
	}
	return service.store.Get(ctx, operationID)
}

func (service *Service) CompleteReview(ctx context.Context, operationID string, result Result) (Operation, error) {
	if service == nil || service.store == nil || strings.TrimSpace(operationID) == "" {
		return Operation{}, ErrInvalidRequest
	}
	if err := result.Validate(); err != nil {
		return Operation{}, err
	}
	// A model response is evidence, not authorization. The video policy layer
	// decides whether a completed result may change publication state.
	result.CanPublish = false
	return service.store.Complete(ctx, operationID, result)
}

func (request ReviewRequest) Validate() error {
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.VideoID) == "" ||
		request.VideoVersion < 0 || strings.TrimSpace(request.PolicyVersion) == "" ||
		(request.Mode != ModeShadow && request.Mode != ModeEnforce) || strings.TrimSpace(request.Title) == "" || len(request.Assets) == 0 {
		return ErrInvalidRequest
	}
	for _, asset := range request.Assets {
		if strings.TrimSpace(asset.Kind) == "" || strings.TrimSpace(asset.URI) == "" || strings.TrimSpace(asset.SHA256) == "" {
			return ErrInvalidRequest
		}
	}
	return nil
}

func (request ReviewRequest) Hash() (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode moderation request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (result Result) Validate() error {
	if result.Verdict != VerdictApprove && result.Verdict != VerdictReject && result.Verdict != VerdictEscalate {
		return ErrInvalidResult
	}
	if result.Confidence < 0 || result.Confidence > 1 || strings.TrimSpace(result.Summary) == "" ||
		strings.TrimSpace(result.Provider) == "" || strings.TrimSpace(result.Model) == "" || strings.TrimSpace(result.PolicyVersion) == "" {
		return ErrInvalidResult
	}
	for _, finding := range result.Findings {
		if strings.TrimSpace(finding.Code) == "" || strings.TrimSpace(finding.Category) == "" || finding.Score < 0 || finding.Score > 1 || finding.TimestampMS < 0 {
			return ErrInvalidResult
		}
	}
	if len(result.Votes) > 8 || len(result.Checks) > 16 || (len(result.Votes) > 0 && strings.TrimSpace(result.Strategy) == "") {
		return ErrInvalidResult
	}
	for _, vote := range result.Votes {
		if strings.TrimSpace(vote.Stage) == "" || strings.TrimSpace(vote.Summary) == "" ||
			strings.TrimSpace(vote.Provider) == "" || strings.TrimSpace(vote.Model) == "" ||
			(vote.Verdict != VerdictApprove && vote.Verdict != VerdictReject && vote.Verdict != VerdictEscalate) ||
			vote.Confidence < 0 || vote.Confidence > 1 {
			return ErrInvalidResult
		}
		for _, finding := range vote.Findings {
			if strings.TrimSpace(finding.Code) == "" || strings.TrimSpace(finding.Category) == "" || finding.Score < 0 || finding.Score > 1 || finding.TimestampMS < 0 {
				return ErrInvalidResult
			}
		}
	}
	for _, check := range result.Checks {
		if strings.TrimSpace(check.Code) == "" || strings.TrimSpace(check.Detail) == "" {
			return ErrInvalidResult
		}
	}
	return nil
}
