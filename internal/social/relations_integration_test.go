package social_test

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/platform/migrate"
	"github.com/sealessland/sea-music/internal/social"
)

func TestLikeFollowAndFavoriteAreIdempotentAndEmitOnlyChanges(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	firstUser, secondUser, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	first, err := repository.SetLike(ctx, firstUser, videoID, true)
	if err != nil || !first.Changed || !first.Exists {
		t.Fatalf("first SetLike() = (%+v, %v)", first, err)
	}
	repeated, err := repository.SetLike(ctx, firstUser, videoID, true)
	if err != nil || repeated.Changed || !repeated.Exists {
		t.Fatalf("repeated SetLike() = (%+v, %v)", repeated, err)
	}
	if result, err := repository.SetFollow(ctx, firstUser, secondUser, true); err != nil || !result.Changed {
		t.Fatalf("SetFollow() = (%+v, %v)", result, err)
	}
	if result, err := repository.SetFavorite(ctx, firstUser, videoID, true); err != nil || !result.Changed {
		t.Fatalf("SetFavorite() = (%+v, %v)", result, err)
	}
	var likes, eventsCount int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM social.video_likes), (SELECT count(*) FROM eventing.outbox)`).Scan(&likes, &eventsCount); err != nil {
		t.Fatalf("count social results: %v", err)
	}
	if likes != 1 || eventsCount != 3 {
		t.Fatalf("social results = likes %d events %d, want 1/3", likes, eventsCount)
	}
}

func TestConcurrentLikeAndUnlikePreserveUniqueAuthoritativeRelation(t *testing.T) {
	database := socialTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID, _, videoID := insertSocialFixture(t, ctx, database)
	repository := socialRepository(database)
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 20)
	for index := range 20 {
		wait.Add(1)
		go func(enabled bool) {
			defer wait.Done()
			_, err := repository.SetLike(ctx, userID, videoID, enabled)
			errorsChannel <- err
		}(index%2 == 0)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent SetLike(): %v", err)
		}
	}
	var relations int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM social.video_likes WHERE user_id = $1 AND video_id = $2`, userID, videoID).Scan(&relations); err != nil {
		t.Fatalf("count concurrent relation: %v", err)
	}
	if relations < 0 || relations > 1 {
		t.Fatalf("authoritative relation count = %d, want 0 or 1", relations)
	}
}

func socialRepository(database *sql.DB) *social.PostgresRepository {
	eventRepository := events.NewPostgresRepository(database)
	writer := social.OutboxWriterFunc(func(ctx context.Context, transaction *sql.Tx, event social.DomainEvent) (string, error) {
		envelope, err := eventRepository.EnqueueTx(ctx, transaction, events.NewEvent{
			Topic: event.Topic, Type: event.Type, Version: event.Version,
			AggregateType: event.AggregateType, AggregateID: event.AggregateID,
			AggregateVersion: event.AggregateVersion, OccurredAt: time.Now().UTC(), Data: event.Data,
		})
		return envelope.ID, err
	})
	return social.NewPostgresRepository(database).WithOutbox(writer)
}

func socialTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("SEA_SOCIAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SEA_SOCIAL_TEST_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open social database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	migrations, err := migrate.Bundled()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := migrate.Apply(ctx, database, migrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE social.counter_reconciliations, social.video_counters, social.danmaku, social.comments, social.relation_versions, social.video_favorites, social.video_likes, social.follows, eventing.dead_letters, eventing.inbox, eventing.outbox, video.state_transitions, video.processing_jobs, video.renditions, video.source_assets, video.videos, identity.sessions, identity.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate social database: %v", err)
	}
	return database
}

func insertSocialFixture(t *testing.T, ctx context.Context, database *sql.DB) (string, string, string) {
	t.Helper()
	var first, second, videoID string
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('social_one', 'social-one@example.com', 'hash') RETURNING id::text`).Scan(&first); err != nil {
		t.Fatalf("insert first social user: %v", err)
	}
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('social_two', 'social-two@example.com', 'hash') RETURNING id::text`).Scan(&second); err != nil {
		t.Fatalf("insert second social user: %v", err)
	}
	if err := database.QueryRowContext(ctx, `INSERT INTO video.videos (creator_id, title, state, version, published_at) VALUES ($1, 'social video', 'published', 1, now()) RETURNING id::text`, second).Scan(&videoID); err != nil {
		t.Fatalf("insert social video: %v", err)
	}
	return first, second, videoID
}
