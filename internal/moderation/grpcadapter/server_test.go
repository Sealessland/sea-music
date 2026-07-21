package grpcadapter_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	moderationv1 "github.com/sealessland/sea-music/internal/gen/moderation/v1"
	"github.com/sealessland/sea-music/internal/moderation"
	"github.com/sealessland/sea-music/internal/moderation/grpcadapter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// TestStartReviewIsIdempotentOverGRPC verifies that replaying an identical request returns the same nonempty operation ID and leaves the review pending.
func TestStartReviewIsIdempotentOverGRPC(t *testing.T) {
	client := newTestClient(t)
	request := validProtoRequest()

	first, err := client.StartReview(context.Background(), request)
	if err != nil {
		t.Fatalf("first StartReview() error = %v", err)
	}
	second, err := client.StartReview(context.Background(), request)
	if err != nil {
		t.Fatalf("second StartReview() error = %v", err)
	}
	if first.GetOperation().GetOperationId() == "" || second.GetOperation().GetOperationId() != first.GetOperation().GetOperationId() {
		t.Fatalf("operation IDs = (%q, %q), want stable ID", first.GetOperation().GetOperationId(), second.GetOperation().GetOperationId())
	}
	if first.GetOperation().GetStatus() != moderationv1.ReviewStatus_REVIEW_STATUS_PENDING {
		t.Fatalf("status = %s, want pending", first.GetOperation().GetStatus())
	}
}

// TestStartReviewMapsDomainErrorsToStableStatusCodes verifies that conflicts, invalid requests, and missing operations map to AlreadyExists, InvalidArgument, and NotFound.
func TestStartReviewMapsDomainErrorsToStableStatusCodes(t *testing.T) {
	client := newTestClient(t)
	request := validProtoRequest()
	if _, err := client.StartReview(context.Background(), request); err != nil {
		t.Fatalf("first StartReview() error = %v", err)
	}
	conflict := cloneRequest(request)
	conflict.VideoVersion++
	if _, err := client.StartReview(context.Background(), conflict); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("conflicting StartReview() code = %s, want AlreadyExists", status.Code(err))
	}
	if _, err := client.StartReview(context.Background(), &moderationv1.StartReviewRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid StartReview() code = %s, want InvalidArgument", status.Code(err))
	}
	if _, err := client.GetReview(context.Background(), &moderationv1.GetReviewRequest{OperationId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing GetReview() code = %s, want NotFound", status.Code(err))
	}
}

// TestDomainClientRoundTripsReviewAndMapsErrors verifies domain-to-protobuf request conversion, pending-operation decoding, and translation of remote conflicts and missing operations to domain sentinel errors.
func TestDomainClientRoundTripsReviewAndMapsErrors(t *testing.T) {
	raw := newTestClient(t)
	client := grpcadapter.NewClient(raw, time.Second)
	request := moderation.ReviewRequest{
		RequestID: "video-1-v4-ugc-v1", VideoID: "video-1", VideoVersion: 4,
		PolicyVersion: "ugc-v1", Mode: moderation.ModeShadow, Title: "A test video",
		Description: "review this upload", Assets: []moderation.Asset{{Kind: "cover", URI: "https://media.example/cover.jpg", SHA256: "abc123"}},
	}
	operation, err := client.StartReview(context.Background(), request)
	if err != nil || operation.ID == "" || operation.Status != moderation.StatusPending {
		t.Fatalf("StartReview() = (%+v, %v)", operation, err)
	}
	conflict := request
	conflict.VideoVersion++
	if _, err := client.StartReview(context.Background(), conflict); !errors.Is(err, moderation.ErrIdempotencyConflict) {
		t.Fatalf("conflicting StartReview() error = %v", err)
	}
	if _, err := client.GetReview(context.Background(), "missing"); !errors.Is(err, moderation.ErrOperationNotFound) {
		t.Fatalf("missing GetReview() error = %v", err)
	}
}

// TestDomainClientRejectsInvalidRemoteEvidence verifies that malformed completed-review evidence from the remote service is rejected with ErrInvalidResult.
func TestDomainClientRejectsInvalidRemoteEvidence(t *testing.T) {
	client := grpcadapter.NewClient(invalidEvidenceClient{}, time.Second)
	if _, err := client.GetReview(context.Background(), "operation-1"); !errors.Is(err, moderation.ErrInvalidResult) {
		t.Fatalf("GetReview() error = %v, want ErrInvalidResult", err)
	}
}

// TestDomainClientPreservesAgentDecisionAuditTrail verifies that result strategy, staged votes, and failed policy checks survive protobuf-to-domain conversion.
func TestDomainClientPreservesAgentDecisionAuditTrail(t *testing.T) {
	client := grpcadapter.NewClient(auditedEvidenceClient{}, time.Second)
	operation, err := client.GetReview(context.Background(), "operation-1")
	if err != nil {
		t.Fatalf("GetReview() error = %v", err)
	}
	if operation.Result == nil || operation.Result.Strategy != "reviewer-critic-v1" || len(operation.Result.Votes) != 2 || len(operation.Result.Checks) != 1 {
		t.Fatalf("result = %+v", operation.Result)
	}
	if operation.Result.Votes[1].Stage != "critic" || operation.Result.Checks[0].Passed {
		t.Fatalf("audit trail = %+v", operation.Result)
	}
}

type invalidEvidenceClient struct{}

type auditedEvidenceClient struct{}

// StartReview always returns an error because the audited-evidence stub supports only GetReview.
func (auditedEvidenceClient) StartReview(context.Context, *moderationv1.StartReviewRequest, ...grpc.CallOption) (*moderationv1.StartReviewResponse, error) {
	return nil, errors.New("not used")
}

// GetReview returns a completed escalation containing reviewer and critic votes plus a failed consensus check for audit-trail conversion tests.
func (auditedEvidenceClient) GetReview(context.Context, *moderationv1.GetReviewRequest, ...grpc.CallOption) (*moderationv1.GetReviewResponse, error) {
	return &moderationv1.GetReviewResponse{Operation: &moderationv1.ReviewOperation{
		OperationId: "operation-1", RequestId: "request-1", Status: moderationv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		Result: &moderationv1.ReviewResult{
			Verdict: moderationv1.ReviewVerdict_REVIEW_VERDICT_ESCALATE, Confidence: 0.91,
			Summary: "reviewer/critic disagreement", Provider: "openai", Model: "test-model", PolicyVersion: "ugc-v1",
			Strategy: "reviewer-critic-v1",
			Votes: []*moderationv1.ReviewVote{
				{Stage: "reviewer", Verdict: moderationv1.ReviewVerdict_REVIEW_VERDICT_APPROVE, Confidence: 0.99, Summary: "safe", Provider: "openai", Model: "test-model"},
				{Stage: "critic", Verdict: moderationv1.ReviewVerdict_REVIEW_VERDICT_REJECT, Confidence: 0.91, Summary: "risk", Provider: "openai", Model: "test-model"},
			},
			Checks: []*moderationv1.PolicyCheck{{Code: "verdict_consensus", Passed: false, Detail: "reviewer=approve critic=reject"}},
		},
	}}, nil
}

// StartReview always returns an error because the invalid-evidence stub supports only GetReview.
func (invalidEvidenceClient) StartReview(context.Context, *moderationv1.StartReviewRequest, ...grpc.CallOption) (*moderationv1.StartReviewResponse, error) {
	return nil, errors.New("not used")
}

// GetReview returns a completed approval with an out-of-range confidence value to exercise remote-result validation.
func (invalidEvidenceClient) GetReview(context.Context, *moderationv1.GetReviewRequest, ...grpc.CallOption) (*moderationv1.GetReviewResponse, error) {
	return &moderationv1.GetReviewResponse{Operation: &moderationv1.ReviewOperation{
		OperationId: "operation-1", Status: moderationv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		Result: &moderationv1.ReviewResult{Verdict: moderationv1.ReviewVerdict_REVIEW_VERDICT_APPROVE, Confidence: 2},
	}}, nil
}

// newTestClient starts an in-memory gRPC moderation server, registers cleanup for the server and connection, and returns a client connected through bufconn.
func newTestClient(t *testing.T) moderationv1.ModerationServiceClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	moderationv1.RegisterModerationServiceServer(server, grpcadapter.NewServer(moderation.NewService(newMemoryStore())))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	connection, err := grpc.NewClient("passthrough:///moderation-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return moderationv1.NewModerationServiceClient(connection)
}

// validProtoRequest returns a complete shadow-mode review request with stable identifiers and one cover asset.
func validProtoRequest() *moderationv1.StartReviewRequest {
	return &moderationv1.StartReviewRequest{
		RequestId: "video-1-v4-ugc-v1", VideoId: "video-1", VideoVersion: 4,
		PolicyVersion: "ugc-v1", Mode: moderationv1.ModerationMode_MODERATION_MODE_SHADOW,
		Title: "A test video", Description: "review this upload",
		Assets: []*moderationv1.MediaAsset{{Kind: "cover", Uri: "https://media.example/cover.jpg", Sha256: "abc123"}},
	}
}

// cloneRequest returns a deep protobuf clone that can be mutated without changing the original request.
func cloneRequest(request *moderationv1.StartReviewRequest) *moderationv1.StartReviewRequest {
	return proto.Clone(request).(*moderationv1.StartReviewRequest)
}

type memoryStore struct {
	byID      map[string]moderation.Operation
	byRequest map[string]string
}

// newMemoryStore returns an empty in-memory operation store indexed by operation and request IDs.
func newMemoryStore() *memoryStore {
	return &memoryStore{byID: map[string]moderation.Operation{}, byRequest: map[string]string{}}
}

// Create persists a pending operation, returns the existing operation for an identical request hash, and reports ErrIdempotencyConflict when the request ID is reused with different input.
func (store *memoryStore) Create(_ context.Context, request moderation.ReviewRequest, inputHash string) (moderation.Operation, error) {
	if id, ok := store.byRequest[request.RequestID]; ok {
		operation := store.byID[id]
		if operation.InputHash != inputHash {
			return moderation.Operation{}, moderation.ErrIdempotencyConflict
		}
		return operation, nil
	}
	id := "operation-1"
	operation := moderation.Operation{ID: id, Request: request, InputHash: inputHash, Status: moderation.StatusPending}
	store.byID[id] = operation
	store.byRequest[request.RequestID] = id
	return operation, nil
}

// Get returns the stored operation or ErrOperationNotFound when its ID is absent.
func (store *memoryStore) Get(_ context.Context, operationID string) (moderation.Operation, error) {
	operation, ok := store.byID[operationID]
	if !ok {
		return moderation.Operation{}, moderation.ErrOperationNotFound
	}
	return operation, nil
}

// Complete always returns an error because completion is outside this gRPC contract test store's scope.
func (store *memoryStore) Complete(context.Context, string, moderation.Result) (moderation.Operation, error) {
	return moderation.Operation{}, errors.New("not implemented in gRPC contract test")
}
