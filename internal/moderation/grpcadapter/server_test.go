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

func TestDomainClientRejectsInvalidRemoteEvidence(t *testing.T) {
	client := grpcadapter.NewClient(invalidEvidenceClient{}, time.Second)
	if _, err := client.GetReview(context.Background(), "operation-1"); !errors.Is(err, moderation.ErrInvalidResult) {
		t.Fatalf("GetReview() error = %v, want ErrInvalidResult", err)
	}
}

type invalidEvidenceClient struct{}

func (invalidEvidenceClient) StartReview(context.Context, *moderationv1.StartReviewRequest, ...grpc.CallOption) (*moderationv1.StartReviewResponse, error) {
	return nil, errors.New("not used")
}

func (invalidEvidenceClient) GetReview(context.Context, *moderationv1.GetReviewRequest, ...grpc.CallOption) (*moderationv1.GetReviewResponse, error) {
	return &moderationv1.GetReviewResponse{Operation: &moderationv1.ReviewOperation{
		OperationId: "operation-1", Status: moderationv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		Result: &moderationv1.ReviewResult{Verdict: moderationv1.ReviewVerdict_REVIEW_VERDICT_APPROVE, Confidence: 2},
	}}, nil
}

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

func validProtoRequest() *moderationv1.StartReviewRequest {
	return &moderationv1.StartReviewRequest{
		RequestId: "video-1-v4-ugc-v1", VideoId: "video-1", VideoVersion: 4,
		PolicyVersion: "ugc-v1", Mode: moderationv1.ModerationMode_MODERATION_MODE_SHADOW,
		Title: "A test video", Description: "review this upload",
		Assets: []*moderationv1.MediaAsset{{Kind: "cover", Uri: "https://media.example/cover.jpg", Sha256: "abc123"}},
	}
}

func cloneRequest(request *moderationv1.StartReviewRequest) *moderationv1.StartReviewRequest {
	return proto.Clone(request).(*moderationv1.StartReviewRequest)
}

type memoryStore struct {
	byID      map[string]moderation.Operation
	byRequest map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{byID: map[string]moderation.Operation{}, byRequest: map[string]string{}}
}

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

func (store *memoryStore) Get(_ context.Context, operationID string) (moderation.Operation, error) {
	operation, ok := store.byID[operationID]
	if !ok {
		return moderation.Operation{}, moderation.ErrOperationNotFound
	}
	return operation, nil
}

func (store *memoryStore) Complete(context.Context, string, moderation.Result) (moderation.Operation, error) {
	return moderation.Operation{}, errors.New("not implemented in gRPC contract test")
}
