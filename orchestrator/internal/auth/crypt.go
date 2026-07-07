package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts/decrypts provider tokens for storage in user_identities
// (access_token_enc / refresh_token_enc). It is AES-256-GCM with a random 96-bit
// nonce prepended to each ciphertext, keyed by AUTH_TOKEN_KEY (32 bytes, base64).
//
// GCM is authenticated: Decrypt fails closed on any tampering (wrong key, flipped
// bit, truncated blob), which is what the roundtrip/tamper unit tests assert.
type Cipher struct {
	aead cipher.AEAD
}

// ErrCipherNotConfigured is returned by Cipher methods when the cipher is nil —
// i.e. no AUTH_TOKEN_KEY was configured. Callers only reach encryption once at
// least one OAuth provider is configured, at which point config.Load has already
// required a valid key, so this is a defensive guard.
var ErrCipherNotConfigured = errors.New("token cipher not configured (AUTH_TOKEN_KEY unset)")

// DecodeTokenKey base64-decodes an AUTH_TOKEN_KEY and validates it is exactly 32
// bytes (AES-256). It is exported so config.Load can validate the key at startup
// without constructing a Cipher.
func DecodeTokenKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("AUTH_TOKEN_KEY is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("AUTH_TOKEN_KEY must decode to 32 bytes for AES-256, got %d", len(key))
	}
	return key, nil
}

// NewCipher builds a Cipher from a base64-encoded 32-byte key. An empty key
// returns (nil, nil) so callers can run token-less (no OAuth providers) without
// special-casing.
func NewCipher(b64Key string) (*Cipher, error) {
	if b64Key == "" {
		return nil, nil
	}
	key, err := DecodeTokenKey(b64Key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext, returning nonce||ciphertext. A nil/empty plaintext
// still produces a (small) authenticated blob so an empty refresh token is
// distinguishable from a missing column by the caller (which stores nil for a
// genuinely absent token).
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrCipherNotConfigured
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ct.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// EncryptString is a convenience wrapper over Encrypt.
func (c *Cipher) EncryptString(s string) ([]byte, error) { return c.Encrypt([]byte(s)) }

// Decrypt opens a nonce||ciphertext blob produced by Encrypt. It returns an
// error on any authentication failure (tampering, wrong key, truncation).
func (c *Cipher) Decrypt(blob []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrCipherNotConfigured
	}
	ns := c.aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := blob[:ns], blob[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}

// DecryptString decrypts a blob to a string.
func (c *Cipher) DecryptString(blob []byte) (string, error) {
	pt, err := c.Decrypt(blob)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
