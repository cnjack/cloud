package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a short random hex identifier (16 bytes / 32 hex chars),
// collision-safe for this scale and URL-friendly. Panics only if the OS CSPRNG
// fails, which is unrecoverable.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("domain: entropy source failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
