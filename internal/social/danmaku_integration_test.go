package social_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/social"
)

func TestDanmakuIsSanitizedRateLimitedAndWindowPaginated(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	authorID, _, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	positions := []int{500, 1000, 1500, 2000, 2500}
	for index, position := range positions {
		body := "message"
		if index == 0 {
			body = "<script>alert(1)</script>"
		}
		message, err := repository.CreateDanmaku(ctx, authorID, videoID, position, body)
		if err != nil {
			t.Fatalf("CreateDanmaku(%d): %v", position, err)
		}
		if strings.Contains(message.Body, "<script>") {
			t.Fatalf("danmaku body was not sanitized: %q", message.Body)
		}
	}
	if _, err := repository.CreateDanmaku(ctx, authorID, videoID, 3000, "too fast"); !errors.Is(err, social.ErrDanmakuRateLimited) {
		t.Fatalf("sixth CreateDanmaku() error = %v, want ErrDanmakuRateLimited", err)
	}
	first, err := repository.ListDanmaku(ctx, videoID, 750, 2600, "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].PositionMS != 1000 || first.NextCursor == "" {
		t.Fatalf("first danmaku page = (%+v, %v)", first, err)
	}
	second, err := repository.ListDanmaku(ctx, videoID, 750, 2600, first.NextCursor, 2)
	if err != nil || len(second.Items) != 2 || second.Items[0].PositionMS != 2000 {
		t.Fatalf("second danmaku page = (%+v, %v)", second, err)
	}
}
