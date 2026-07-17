package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresRepository struct {
	database *sql.DB
}

func (r *PostgresRepository) FindUser(ctx context.Context, userID string) (User, error) {
	var user User
	err := r.database.QueryRowContext(ctx, `
		SELECT id::text, username::text, email::text, role, created_at
		FROM identity.users
		WHERE id = $1
	`, userID).Scan(&user.ID, &user.Username, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrIdentityNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find user: %w", err)
	}
	return user, nil
}

func (r *PostgresRepository) FindCredential(ctx context.Context, identity string) (Credential, error) {
	var credential Credential
	err := r.database.QueryRowContext(ctx, `
		SELECT id::text, username::text, email::text, role, created_at, password_hash
		FROM identity.users
		WHERE username = $1 OR email = $1
	`, identity).Scan(
		&credential.ID,
		&credential.Username,
		&credential.Email,
		&credential.Role,
		&credential.CreatedAt,
		&credential.PasswordHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrIdentityNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("find credential: %w", err)
	}
	return credential, nil
}

func (r *PostgresRepository) CreateSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) (string, error) {
	var sessionID string
	err := r.database.QueryRowContext(ctx, `
		INSERT INTO identity.sessions (family_id, user_id, token_hash, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id::text
	`, userID, tokenHash, expiresAt).Scan(&sessionID)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return sessionID, nil
}

func (r *PostgresRepository) RotateSession(ctx context.Context, currentHash, nextHash []byte, expiresAt, now time.Time) (User, string, error) {
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("begin refresh rotation: %w", err)
	}
	defer transaction.Rollback()

	var oldSessionID, familyID string
	var oldExpiresAt time.Time
	var rotatedAt, revokedAt sql.NullTime
	var user User
	err = transaction.QueryRowContext(ctx, `
		SELECT s.id::text, s.family_id::text, s.expires_at, s.rotated_at, s.revoked_at,
		       u.id::text, u.username::text, u.email::text, u.role, u.created_at
		FROM identity.sessions s
		JOIN identity.users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		FOR UPDATE OF s
	`, currentHash).Scan(
		&oldSessionID, &familyID, &oldExpiresAt, &rotatedAt, &revokedAt,
		&user.ID, &user.Username, &user.Email, &user.Role, &user.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrInvalidRefresh
	}
	if err != nil {
		return User{}, "", fmt.Errorf("lock refresh session: %w", err)
	}
	if rotatedAt.Valid || revokedAt.Valid {
		if _, err := transaction.ExecContext(ctx, `
			UPDATE identity.sessions
			SET revoked_at = COALESCE(revoked_at, $2)
			WHERE family_id = $1
		`, familyID, now); err != nil {
			return User{}, "", fmt.Errorf("revoke replayed session family: %w", err)
		}
		if err := transaction.Commit(); err != nil {
			return User{}, "", fmt.Errorf("commit replay revocation: %w", err)
		}
		return User{}, "", ErrRefreshReplay
	}
	if !oldExpiresAt.After(now) {
		if _, err := transaction.ExecContext(ctx, `UPDATE identity.sessions SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, now); err != nil {
			return User{}, "", fmt.Errorf("revoke expired session family: %w", err)
		}
		if err := transaction.Commit(); err != nil {
			return User{}, "", fmt.Errorf("commit expiry revocation: %w", err)
		}
		return User{}, "", ErrInvalidRefresh
	}

	var nextSessionID string
	if err := transaction.QueryRowContext(ctx, `
		INSERT INTO identity.sessions (family_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text
	`, familyID, user.ID, nextHash, expiresAt).Scan(&nextSessionID); err != nil {
		return User{}, "", fmt.Errorf("create rotated session: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE identity.sessions
		SET rotated_at = $2, replaced_by = $3
		WHERE id = $1
	`, oldSessionID, now, nextSessionID); err != nil {
		return User{}, "", fmt.Errorf("mark session rotated: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return User{}, "", fmt.Errorf("commit refresh rotation: %w", err)
	}
	return user, nextSessionID, nil
}

func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	return &PostgresRepository{database: database}
}

func (r *PostgresRepository) Create(ctx context.Context, input CreateUser) (User, error) {
	var user User
	err := r.database.QueryRowContext(ctx, `
		INSERT INTO identity.users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id::text, username::text, email::text, role, created_at
	`, input.Username, input.Email, input.PasswordHash).Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.Role,
		&user.CreatedAt,
	)
	if err == nil {
		return user, nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return User{}, ErrIdentityConflict
	}
	return User{}, fmt.Errorf("create user: %w", err)
}
