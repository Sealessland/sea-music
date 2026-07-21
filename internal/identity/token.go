package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidAccessToken = errors.New("invalid access token")

type AccessClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Role      string `json:"role"`
	SessionID string `json:"sid"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type TokenManager struct {
	key       []byte
	issuer    string
	accessTTL time.Duration
}

// NewTokenManager creates a token manager that owns a copy of key; Issue rejects keys shorter than 32 bytes, empty issuers, and nonpositive access lifetimes.
func NewTokenManager(key []byte, issuer string, accessTTL time.Duration) *TokenManager {
	keyCopy := append([]byte(nil), key...)
	return &TokenManager{key: keyCopy, issuer: issuer, accessTTL: accessTTL}
}

// Issue creates an HS256 JWT for user and sessionID at now, returning its UTC-based expiration or an error if the manager configuration or JSON encoding is invalid.
func (m *TokenManager) Issue(user User, sessionID string, now time.Time) (string, time.Time, error) {
	if len(m.key) < 32 || m.issuer == "" || m.accessTTL <= 0 {
		return "", time.Time{}, errors.New("invalid token manager configuration")
	}
	now = now.UTC()
	expiresAt := now.Add(m.accessTTL)
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode token header: %w", err)
	}
	claims, err := json.Marshal(AccessClaims{
		Issuer:    m.issuer,
		Subject:   user.ID,
		Role:      user.Role,
		SessionID: sessionID,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode token claims: %w", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature := m.sign(unsigned)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

// Verify authenticates and decodes an HS256 JWT, returning ErrInvalidAccessToken for malformed or tampered tokens, issuer or required-claim mismatches, expired claims, or issue times more than 30 seconds in the future.
func (m *TokenManager) Verify(token string, now time.Time) (AccessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, m.sign(unsigned)) {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	var claims AccessClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	nowUnix := now.UTC().Unix()
	if claims.Issuer != m.issuer || claims.Subject == "" || claims.Role == "" || claims.SessionID == "" || claims.ExpiresAt <= nowUnix || claims.IssuedAt > nowUnix+30 {
		return AccessClaims{}, ErrInvalidAccessToken
	}
	return claims, nil
}

// sign computes the HMAC-SHA256 authentication tag for value using the manager's private key copy.
func (m *TokenManager) sign(value string) []byte {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
