package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

// TestRegistrationPersistsArgon2HashAndRejectsDuplicateIdentity verifies that registration normalizes identity fields, assigns the member role, stores a verifiable Argon2id password hash, and rejects case-insensitive username or email conflicts.
func TestRegistrationPersistsArgon2HashAndRejectsDuplicateIdentity(t *testing.T) {
	database := identityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hasher := identity.NewPasswordHasher(identity.DefaultPasswordParams())
	service := identity.NewService(identity.NewPostgresRepository(database), hasher)
	const password = "correct horse battery staple"

	user, err := service.Register(ctx, identity.RegisterInput{
		Username: "Creator_One",
		Email:    "Creator@Example.com",
		Password: password,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if user.ID == "" || user.Username != "creator_one" || user.Email != "creator@example.com" || user.Role != "member" {
		t.Fatalf("registered user = %+v", user)
	}

	var storedHash string
	if err := database.QueryRowContext(ctx,
		`SELECT password_hash FROM identity.users WHERE id = $1`, user.ID,
	).Scan(&storedHash); err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	if strings.Contains(storedHash, password) || !strings.HasPrefix(storedHash, "$argon2id$") {
		t.Fatalf("stored credential is not an Argon2id hash")
	}
	verified, err := hasher.Verify(password, storedHash)
	if err != nil || !verified {
		t.Fatalf("Verify() = (%v, %v), want (true, nil)", verified, err)
	}

	_, err = service.Register(ctx, identity.RegisterInput{
		Username: "creator_one",
		Email:    "another@example.com",
		Password: password,
	})
	if !errors.Is(err, identity.ErrIdentityConflict) {
		t.Fatalf("duplicate username error = %v, want ErrIdentityConflict", err)
	}
	_, err = service.Register(ctx, identity.RegisterInput{
		Username: "another_creator",
		Email:    "CREATOR@example.com",
		Password: password,
	})
	if !errors.Is(err, identity.ErrIdentityConflict) {
		t.Fatalf("duplicate email error = %v, want ErrIdentityConflict", err)
	}
}

// TestRegistrationRejectsInvalidInputBeforePersistence verifies that malformed registration data (short username, invalid email, weak password) returns ErrInvalidRegistration and that no user row is persisted to the database.
func TestRegistrationRejectsInvalidInputBeforePersistence(t *testing.T) {
	database := identityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	service := identity.NewService(identity.NewPostgresRepository(database), identity.NewPasswordHasher(identity.DefaultPasswordParams()))

	_, err := service.Register(ctx, identity.RegisterInput{
		Username: "x",
		Email:    "not-an-email",
		Password: "short",
	})
	if !errors.Is(err, identity.ErrInvalidRegistration) {
		t.Fatalf("Register() error = %v, want ErrInvalidRegistration", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM identity.users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("persisted user count = %d, want 0", count)
	}
}

// TestLoginRotatesRefreshTokensAndRevokesFamilyOnReplay verifies case-insensitive login, valid access claims, that the refresh token is stored only as a hash (not raw), token rotation on refresh, and complete session-family revocation — returning ErrRefreshReplay — when a revoked refresh token is reused.
func TestLoginRotatesRefreshTokensAndRevokesFamilyOnReplay(t *testing.T) {
	database := identityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	passwords := identity.NewPasswordHasher(identity.DefaultPasswordParams())
	tokens := identity.NewTokenManager([]byte("0123456789abcdef0123456789abcdef"), "sea-music", 15*time.Minute)
	service := identity.NewService(identity.NewPostgresRepository(database), passwords).WithSessions(tokens, 30*24*time.Hour)
	const password = "correct horse battery staple"
	user, err := service.Register(ctx, identity.RegisterInput{Username: "session_user", Email: "session@example.com", Password: password})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pair, err := service.Login(ctx, identity.LoginInput{Identity: "SESSION@example.com", Password: password})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresIn <= 0 {
		t.Fatalf("Login() token pair is incomplete: %+v", pair)
	}
	claims, err := tokens.Verify(pair.AccessToken, time.Now())
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	if claims.Subject != user.ID || claims.Role != "member" || claims.SessionID == "" {
		t.Fatalf("access claims = %+v", claims)
	}
	var rawTokenStored bool
	if err := database.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM identity.sessions WHERE token_hash = $1)`, []byte(pair.RefreshToken),
	).Scan(&rawTokenStored); err != nil {
		t.Fatalf("inspect refresh storage: %v", err)
	}
	if rawTokenStored {
		t.Fatal("raw refresh token was stored")
	}

	rotated, err := service.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if rotated.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	_, err = service.Refresh(ctx, pair.RefreshToken)
	if !errors.Is(err, identity.ErrRefreshReplay) {
		t.Fatalf("reused refresh error = %v, want ErrRefreshReplay", err)
	}
	_, err = service.Refresh(ctx, rotated.RefreshToken)
	if !errors.Is(err, identity.ErrInvalidRefresh) && !errors.Is(err, identity.ErrRefreshReplay) {
		t.Fatalf("replacement after family revocation error = %v, want invalid/replay", err)
	}
	var active int
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM identity.sessions WHERE family_id = (SELECT family_id FROM identity.sessions WHERE token_hash = digest($1, 'sha256')) AND revoked_at IS NULL`,
		pair.RefreshToken,
	).Scan(&active); err != nil {
		t.Fatalf("count active family sessions: %v", err)
	}
	if active != 0 {
		t.Fatalf("active family sessions = %d, want 0", active)
	}
}

// TestLoginRejectsInvalidCredentials verifies that login for an unknown identity return ErrInvalidCredentials, distinguishing this from persistence or token failures.
func TestLoginRejectsInvalidCredentials(t *testing.T) {
	database := identityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	passwords := identity.NewPasswordHasher(identity.DefaultPasswordParams())
	service := identity.NewService(identity.NewPostgresRepository(database), passwords).WithSessions(
		identity.NewTokenManager([]byte("0123456789abcdef0123456789abcdef"), "sea-music", 15*time.Minute),
		30*24*time.Hour,
	)
	_, err := service.Login(ctx, identity.LoginInput{Identity: "missing@example.com", Password: "wrong password value"})
	if !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

// identityTestDatabase opens the configured PostgreSQL integration database, applies bundled migrations, clears identity data, registers cleanup, and skips the test when no database URL is set.
func identityTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SEA_IDENTITY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_IDENTITY_TEST_DATABASE_URL is required for the PostgreSQL integration test")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
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
	if _, err := database.ExecContext(ctx, `DELETE FROM identity.sessions; DELETE FROM identity.users`); err != nil {
		t.Fatalf("truncate users: %v", err)
	}
	return database
}
