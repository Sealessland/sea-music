package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidRegistration = errors.New("invalid registration")
	ErrIdentityConflict    = errors.New("identity already exists")
	ErrIdentityNotFound    = errors.New("identity not found")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRefresh      = errors.New("invalid refresh token")
	ErrRefreshReplay       = errors.New("refresh token replay")
	usernamePattern        = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
)

type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type RegisterInput struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type CreateUser struct {
	Username     string
	Email        string
	PasswordHash string
}

type Credential struct {
	User
	PasswordHash string
}

type LoginInput struct {
	Identity string `json:"identity"`
	Password string `json:"password"`
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type Repository interface {
	Create(context.Context, CreateUser) (User, error)
	FindUser(context.Context, string) (User, error)
	FindCredential(context.Context, string) (Credential, error)
	CreateSession(context.Context, string, []byte, time.Time) (string, error)
	RotateSession(context.Context, []byte, []byte, time.Time, time.Time) (User, string, error)
}

// CurrentUser retrieves the user identified by userID from the repository.
func (s *Service) CurrentUser(ctx context.Context, userID string) (User, error) {
	return s.repository.FindUser(ctx, userID)
}

type Passwords interface {
	Hash(string) (string, error)
	Verify(string, string) (bool, error)
}

type Service struct {
	repository Repository
	passwords  Passwords
	tokens     *TokenManager
	refreshTTL time.Duration
	dummyHash  string
}

// NewService creates an identity service with password hashing and a precomputed dummy hash that reduces login timing differences for unknown identities. The returned service has no token manager configured; call WithSessions before Login or Refresh.
func NewService(repository Repository, passwords Passwords) *Service {
	dummyHash, _ := passwords.Hash("dummy password value never accepted")
	return &Service{repository: repository, passwords: passwords, dummyHash: dummyHash}
}

// WithSessions enables session-based login and refresh using tokens and refreshTTL, mutates the service, and returns it for chaining.
func (s *Service) WithSessions(tokens *TokenManager, refreshTTL time.Duration) *Service {
	s.tokens = tokens
	s.refreshTTL = refreshTTL
	return s
}

// Register normalizes the username and email, validates registration fields, hashes the password, and persists the new user via the repository.
func (s *Service) Register(ctx context.Context, input RegisterInput) (User, error) {
	username := strings.ToLower(strings.TrimSpace(input.Username))
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if err := validateRegistration(username, email, input.Password); err != nil {
		return User{}, err
	}
	passwordHash, err := s.passwords.Hash(input.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	user, err := s.repository.Create(ctx, CreateUser{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// Login verifies normalized credentials, creates a refresh session, and returns a bearer token pair; unknown identities and password mismatches both yield ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, input LoginInput) (TokenPair, error) {
	if s.tokens == nil || s.refreshTTL <= 0 {
		return TokenPair{}, errors.New("session service is not configured")
	}
	loginIdentity := strings.ToLower(strings.TrimSpace(input.Identity))
	if loginIdentity == "" || len(input.Password) > 128 {
		return TokenPair{}, ErrInvalidCredentials
	}
	credential, err := s.repository.FindCredential(ctx, loginIdentity)
	if errors.Is(err, ErrIdentityNotFound) {
		if s.dummyHash != "" {
			_, _ = s.passwords.Verify(input.Password, s.dummyHash)
		}
		return TokenPair{}, ErrInvalidCredentials
	}
	if err != nil {
		return TokenPair{}, err
	}
	valid, err := s.passwords.Verify(input.Password, credential.PasswordHash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("verify password: %w", err)
	}
	if !valid {
		return TokenPair{}, ErrInvalidCredentials
	}
	rawRefresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	now := time.Now().UTC()
	sessionID, err := s.repository.CreateSession(ctx, credential.ID, refreshHash, now.Add(s.refreshTTL))
	if err != nil {
		return TokenPair{}, err
	}
	return s.issuePair(credential.User, sessionID, rawRefresh, now)
}

// Refresh rotates a valid refresh token to a newly generated token, extends the session expiry, and returns a new bearer token pair.
func (s *Service) Refresh(ctx context.Context, currentRefresh string) (TokenPair, error) {
	if s.tokens == nil || s.refreshTTL <= 0 || currentRefresh == "" || len(currentRefresh) > 512 {
		return TokenPair{}, ErrInvalidRefresh
	}
	rawRefresh, nextHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	currentHash := sha256.Sum256([]byte(currentRefresh))
	now := time.Now().UTC()
	user, sessionID, err := s.repository.RotateSession(ctx, currentHash[:], nextHash, now.Add(s.refreshTTL), now)
	if err != nil {
		return TokenPair{}, err
	}
	return s.issuePair(user, sessionID, rawRefresh, now)
}

// issuePair issues an access token for the user and session and packages it with the supplied refresh token and computed lifetime.
func (s *Service) issuePair(user User, sessionID, refreshToken string, now time.Time) (TokenPair, error) {
	accessToken, expiresAt, err := s.tokens.Issue(user, sessionID, now)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(expiresAt.Sub(now).Seconds()),
	}, nil
}

// newRefreshToken generates a cryptographically random URL-safe refresh token and returns both its raw value and SHA-256 hash.
func newRefreshToken() (string, []byte, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value[:])
	hash := sha256.Sum256([]byte(raw))
	return raw, hash[:], nil
}

// validateRegistration enforces the username format, canonical email syntax and length, and a password length of 12–128 bytes, wrapping failures with ErrInvalidRegistration.
func validateRegistration(username, email, password string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("%w: username must be 3-32 lowercase letters, digits, or underscores", ErrInvalidRegistration)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return fmt.Errorf("%w: email is invalid", ErrInvalidRegistration)
	}
	if len(password) < 12 || len(password) > 128 {
		return fmt.Errorf("%w: password must be 12-128 bytes", ErrInvalidRegistration)
	}
	return nil
}
