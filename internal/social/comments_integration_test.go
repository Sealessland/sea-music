package social_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/social"
)

// TestCommentRepliesAreOneLevelAndDeletedParentBecomesTombstone verifies that a reply cannot itself be replied to (returning ErrInvalidCommentParent), and that deleting a parent which has a reply replaces it with a deleted, empty-body tombstone that still lists the existing reply.
func TestCommentRepliesAreOneLevelAndDeletedParentBecomesTombstone(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	authorID, creatorID, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	parent, err := repository.CreateComment(ctx, authorID, videoID, "", "top-level body")
	if err != nil {
		t.Fatalf("CreateComment(parent): %v", err)
	}
	reply, err := repository.CreateComment(ctx, creatorID, videoID, parent.ID, "reply body")
	if err != nil {
		t.Fatalf("CreateComment(reply): %v", err)
	}
	if _, err := repository.CreateComment(ctx, authorID, videoID, reply.ID, "nested reply"); !errors.Is(err, social.ErrInvalidCommentParent) {
		t.Fatalf("nested reply error = %v, want ErrInvalidCommentParent", err)
	}
	if err := repository.DeleteComment(ctx, parent.ID, social.Actor{UserID: creatorID, Role: "member"}); err != nil {
		t.Fatalf("DeleteComment(): %v", err)
	}
	page, err := repository.ListComments(ctx, videoID, "", 20)
	if err != nil {
		t.Fatalf("ListComments(): %v", err)
	}
	if len(page.Items) != 1 || !page.Items[0].Deleted || page.Items[0].Body != "" || len(page.Items[0].Replies) != 1 || page.Items[0].Replies[0].ID != reply.ID {
		t.Fatalf("tombstone thread = %+v", page.Items)
	}
}

// TestCommentCursorIsStableAcrossNewInsertions verifies that cursor pagination remains stable when a new comment is inserted between page fetches: the cursor snapshot excludes the interloping comment, and the second page returns only the remaining original comments without duplication or skipping.
func TestCommentCursorIsStableAcrossNewInsertions(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	authorID, _, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	for _, body := range []string{"first", "second", "third"} {
		if _, err := repository.CreateComment(ctx, authorID, videoID, "", body); err != nil {
			t.Fatalf("CreateComment(%s): %v", body, err)
		}
		time.Sleep(time.Millisecond)
	}
	firstPage, err := repository.ListComments(ctx, videoID, "", 2)
	if err != nil || len(firstPage.Items) != 2 || firstPage.NextCursor == "" {
		t.Fatalf("first page = (%+v, %v)", firstPage, err)
	}
	if _, err := repository.CreateComment(ctx, authorID, videoID, "", "inserted between pages"); err != nil {
		t.Fatalf("insert between pages: %v", err)
	}
	secondPage, err := repository.ListComments(ctx, videoID, firstPage.NextCursor, 2)
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].Body != "first" {
		t.Fatalf("second page = (%+v, %v)", secondPage, err)
	}
}
