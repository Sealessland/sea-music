package video_test

import (
	"errors"
	"testing"

	"github.com/sealessland/sea-music/internal/video"
)

// TestPublicationStateMachineAllowsOnlyDeclaredTransitions verifies that a draft advances to uploaded with an incremented version, undeclared transitions return ErrInvalidTransition, and a stale expected version returns ErrVersionConflict.
func TestPublicationStateMachineAllowsOnlyDeclaredTransitions(t *testing.T) {
	draft := video.Video{ID: "video", State: video.StateDraft, Version: 0}
	uploaded, err := draft.Transition(0, video.StateUploaded)
	if err != nil || uploaded.State != video.StateUploaded || uploaded.Version != 1 {
		t.Fatalf("draft -> uploaded = (%+v, %v)", uploaded, err)
	}
	if _, err := draft.Transition(0, video.StatePublished); !errors.Is(err, video.ErrInvalidTransition) {
		t.Fatalf("draft -> published error = %v, want ErrInvalidTransition", err)
	}
	if _, err := draft.Transition(1, video.StateUploaded); !errors.Is(err, video.ErrVersionConflict) {
		t.Fatalf("stale transition error = %v, want ErrVersionConflict", err)
	}
}

// TestPublishedVideoCanOnlyBeWithdrawn verifies that a published video rejects a transition to processing with ErrInvalidTransition but permits withdrawal to StateWithdrawn.
func TestPublishedVideoCanOnlyBeWithdrawn(t *testing.T) {
	published := video.Video{ID: "video", State: video.StatePublished, Version: 5}
	if _, err := published.Transition(5, video.StateProcessing); !errors.Is(err, video.ErrInvalidTransition) {
		t.Fatalf("published -> processing error = %v", err)
	}
	withdrawn, err := published.Transition(5, video.StateWithdrawn)
	if err != nil || withdrawn.State != video.StateWithdrawn {
		t.Fatalf("published -> withdrawn = (%+v, %v)", withdrawn, err)
	}
}
