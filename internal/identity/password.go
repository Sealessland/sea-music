package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

type PasswordParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltBytes   uint32
	KeyBytes    uint32
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 1,
		SaltBytes:   16,
		KeyBytes:    32,
	}
}

type PasswordHasher struct {
	params PasswordParams
}

func NewPasswordHasher(params PasswordParams) *PasswordHasher {
	return &PasswordHasher{params: params}
}

func (h *PasswordHasher) Hash(password string) (string, error) {
	if err := validatePasswordParams(h.params); err != nil {
		return "", err
	}
	salt := make([]byte, h.params.SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.MemoryKiB,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func (h *PasswordHasher) Verify(password, encoded string) (bool, error) {
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyBytes)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parsePasswordHash(encoded string) (PasswordParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return PasswordParams{}, nil, nil, errors.New("invalid password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return PasswordParams{}, nil, nil, errors.New("unsupported password hash version")
	}
	var params PasswordParams
	var parallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.MemoryKiB, &params.Iterations, &parallelism); err != nil || parallelism > 255 {
		return PasswordParams{}, nil, nil, errors.New("invalid password hash parameters")
	}
	params.Parallelism = uint8(parallelism)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return PasswordParams{}, nil, nil, errors.New("invalid password hash salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return PasswordParams{}, nil, nil, errors.New("invalid password hash key")
	}
	params.SaltBytes = uint32(len(salt))
	params.KeyBytes = uint32(len(expected))
	if err := validatePasswordParams(params); err != nil {
		return PasswordParams{}, nil, nil, err
	}
	return params, salt, expected, nil
}

func validatePasswordParams(params PasswordParams) error {
	if params.MemoryKiB < 19*1024 || params.MemoryKiB > 1024*1024 || params.Iterations < 2 || params.Iterations > 10 || params.Parallelism < 1 || params.Parallelism > 16 || params.SaltBytes < 16 || params.SaltBytes > 64 || params.KeyBytes < 32 || params.KeyBytes > 64 {
		return errors.New("password hash parameters outside safety bounds")
	}
	return nil
}
