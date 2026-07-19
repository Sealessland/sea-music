package moderation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sealessland/sea-music/internal/moderation"
)

func TestStartReviewIsIdempotentForTheSameRequest(t *testing.T) {
	store := newMemoryStore()
	service := moderation.NewService(store)
	request := validRequest()

	first, err := service.StartReview(context.Background(), request)
	if err != nil {
		t.Fatalf("first StartReview() error = %v", err)
	}
	second, err := service.StartReview(context.Background(), request)
	if err != nil {
		t.Fatalf("second StartReview() error = %v", err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("operation IDs = (%q, %q), want one stable ID", first.ID, second.ID)
	}
	if second.Status != moderation.StatusPending {
		t.Fatalf("second status = %q, want pending", second.Status)
	}
}

func TestStartReviewRejectsRequestIDReuseWithDifferentInput(t *testing.T) {
	service := moderation.NewService(newMemoryStore())
	request := validRequest()
	if _, err := service.StartReview(context.Background(), request); err != nil {
		t.Fatalf("first StartReview() error = %v", err)
	}
	request.VideoVersion++

	_, err := service.StartReview(context.Background(), request)
	if !errors.Is(err, moderation.ErrIdempotencyConflict) {
		t.Fatalf("reused StartReview() error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCompleteShadowReviewPersistsEvidenceWithoutPublishingAuthority(t *testing.T) {
	store := newMemoryStore()
	service := moderation.NewService(store)
	operation, err := service.StartReview(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("StartReview() error = %v", err)
	}

	completed, err := service.CompleteReview(context.Background(), operation.ID, moderation.Result{
		Verdict:    moderation.VerdictEscalate,
		Confidence: 0.72,
		Summary:    "requires a human policy decision",
		Findings: []moderation.Finding{{
			Code: "ambiguous_context", Category: "context", Score: 0.72,
		}},
		Provider: "manual-fallback", Model: "none", PolicyVersion: "ugc-v1",
	})
	if err != nil {
		t.Fatalf("CompleteReview() error = %v", err)
	}
	if completed.Status != moderation.StatusCompleted || completed.Result == nil {
		t.Fatalf("completed operation = %+v", completed)
	}
	if completed.Result.Verdict != moderation.VerdictEscalate {
		t.Fatalf("verdict = %q, want escalate", completed.Result.Verdict)
	}
	if completed.Result.CanPublish {
		t.Fatal("shadow moderation result unexpectedly has publishing authority")
	}
}

func validRequest() moderation.ReviewRequest {
	return moderation.ReviewRequest{
		RequestID: "video-1-v4-ugc-v1", VideoID: "video-1", VideoVersion: 4,
		PolicyVersion: "ugc-v1", Mode: moderation.ModeShadow,
		Title: "A test video", Description: "review this upload",
		Assets: []moderation.Asset{{Kind: "cover", URI: "https://media.example/cover.jpg", SHA256: "abc123"}},
	}
}

type memoryStore struct {
	byID      map[string]moderation.Operation
	byRequest map[string]string
	next      int
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
	store.next++
	id := "operation-" + string(rune('0'+store.next))
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

func (store *memoryStore) Complete(_ context.Context, operationID string, result moderation.Result) (moderation.Operation, error) {
	operation, ok := store.byID[operationID]
	if !ok {
		return moderation.Operation{}, moderation.ErrOperationNotFound
	}
	operation.Status = moderation.StatusCompleted
	operation.Result = &result
	store.byID[operationID] = operation
	return operation, nil
}
