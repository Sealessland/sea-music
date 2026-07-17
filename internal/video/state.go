package video

import (
	"errors"
	"fmt"
	"time"
)

type State string

const (
	StateDraft      State = "draft"
	StateUploaded   State = "uploaded"
	StateProcessing State = "processing"
	StateReview     State = "review"
	StatePublished  State = "published"
	StateFailed     State = "failed"
	StateWithdrawn  State = "withdrawn"
)

var (
	ErrInvalidTransition = errors.New("invalid video state transition")
	ErrVersionConflict   = errors.New("video version conflict")
	ErrVideoNotFound     = errors.New("video not found")
)

var allowedTransitions = map[State]map[State]bool{
	StateDraft:      {StateUploaded: true, StateWithdrawn: true},
	StateUploaded:   {StateProcessing: true, StateFailed: true, StateWithdrawn: true},
	StateProcessing: {StateReview: true, StateFailed: true, StateWithdrawn: true},
	StateReview:     {StatePublished: true, StateFailed: true, StateWithdrawn: true},
	StatePublished:  {StateWithdrawn: true},
	StateFailed:     {StateProcessing: true, StateWithdrawn: true},
	StateWithdrawn:  {},
}

type Video struct {
	ID          string     `json:"id"`
	CreatorID   string     `json:"creator_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       State      `json:"state"`
	Version     int64      `json:"version"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (video Video) Transition(expectedVersion int64, target State) (Video, error) {
	if video.Version != expectedVersion {
		return Video{}, ErrVersionConflict
	}
	if !allowedTransitions[video.State][target] {
		return Video{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, video.State, target)
	}
	video.State = target
	video.Version++
	return video, nil
}
