// Package auth provides bearer-token helpers: constant-time comparison for the
// static console token and generation/hashing of per-run runner tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateRunToken returns a cryptographically-random opaque token (URL-safe
// hex) suitable for injecting into a runner Job's RUN_TOKEN env var.
func GenerateRunToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate run token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the lowercase hex SHA-256 of a token. Only the hash is
// persisted (on Run.TokenHash); the plaintext lives only in the Job env.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two secrets without leaking length/content timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// BearerToken extracts the token from an "Authorization: Bearer <t>" header
// value, returning ("", false) if the header is missing or malformed.
func BearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}
