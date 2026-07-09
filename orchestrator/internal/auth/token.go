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

// APIKeyTokenPrefix tags a project-scoped API key's plaintext (F12 / D24) so
// the principal resolver can recognise one before doing a DB lookup, without
// confusing it with an opaque session token.
const APIKeyTokenPrefix = "jck_"

// apiKeyDisplayPrefixLen is how many leading characters of the plaintext
// (including APIKeyTokenPrefix) are retained in the clear — see
// APIKeyDisplayPrefix.
const apiKeyDisplayPrefixLen = 8 // "jck_" + 4 hex chars, e.g. "jck_a1b2"

// GenerateAPIKey returns a fresh project-scoped API key: APIKeyTokenPrefix +
// 32 cryptographically-random bytes, hex-encoded. Only its SHA-256 (HashToken)
// and a short display prefix (APIKeyDisplayPrefix) are ever persisted — the
// plaintext returned here is shown to the caller exactly once, at creation.
// There is no read-back path afterward (CLAUDE.md fail-visible credential
// discipline / D24).
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return APIKeyTokenPrefix + hex.EncodeToString(b), nil
}

// APIKeyDisplayPrefix returns the plaintext key's first few characters (e.g.
// "jck_a1b2") for list-identification only. It is stored alongside the hash so
// an owner can recognise which key is which — it is deliberately short and
// NEVER sufficient on its own to authenticate.
func APIKeyDisplayPrefix(plaintext string) string {
	if len(plaintext) <= apiKeyDisplayPrefixLen {
		return plaintext
	}
	return plaintext[:apiKeyDisplayPrefixLen]
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
