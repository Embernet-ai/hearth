package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters.
//
// These follow the OWASP recommendation of 19 MiB and one iteration with
// parallelism 1, which is the configuration chosen to be memory-hard rather than
// time-hard. They are encoded INTO the hash, so raising them later does not
// invalidate existing passwords: an old hash still verifies with its own
// parameters and can be transparently upgraded on next login.
const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 19 * 1024 // KiB
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	saltLen             = 16
)

var (
	ErrBadCredentials = errors.New("invalid email or password")
	ErrWeakPassword   = errors.New("password is too short")
)

// MinPasswordLength is 12, not 8, and there are no composition rules.
//
// Length beats character classes: "correct horse battery staple" is stronger
// and more memorable than "P@ssw0rd!", and composition rules mostly cause people
// to append "1!" to something they already reused.
const MinPasswordLength = 12

// HashPassword returns an encoded argon2id hash, salt and parameters included.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", fmt.Errorf("%w: minimum %d characters", ErrWeakPassword, MinPasswordLength)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether the password matches the encoded hash.
//
// Comparison is constant time. A byte-by-byte compare that returns early leaks
// how much of the hash matched, which is enough to reconstruct it one byte at a
// time given enough attempts.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unrecognised password hash format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("auth: parse hash version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("auth: parse hash parameters: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: decode hash: %w", err)
	}

	// Recomputed with the parameters stored in the hash, not the current
	// constants, so raising the cost does not lock everyone out.
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a hash was made with weaker parameters than the
// current settings, so it can be upgraded on next successful login.
func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return true
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return true
	}
	return memory < argonMemory || time < argonTime
}
