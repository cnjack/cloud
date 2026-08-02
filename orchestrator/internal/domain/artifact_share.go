package domain

import (
	"strconv"
	"time"
)

const (
	ArtifactShareProtocolV1 = "jcode-artifact-share-v1"

	ArtifactSharePending   = "pending"
	ArtifactShareUploading = "uploading"
	ArtifactShareUploaded  = "uploaded"
	ArtifactShareComplete  = "complete"
	ArtifactShareRevoked   = "revoked"
)

// EncryptedArtifactMetadata is an opaque AES-GCM envelope. Ciphertext includes
// the 16-byte authentication tag; nonce and ciphertext use base64url without
// padding. Cloud stores these fields but never decrypts them.
type EncryptedArtifactMetadata struct {
	Nonce           string `json:"nonce"`
	Ciphertext      string `json:"ciphertext"`
	PlaintextLength int64  `json:"plaintext_length"`
}

// ArtifactShare is one immutable, encrypted artifact snapshot. Content-bearing
// fields are ciphertext only; local task ids, paths, titles and share keys are
// deliberately absent.
type ArtifactShare struct {
	ID         string `json:"share_id"`
	UserID     string `json:"-"`
	DeviceID   string `json:"device_id,omitempty"`
	ArtifactID string `json:"artifact_id"`
	Revision   int    `json:"revision"`
	Protocol   string `json:"protocol"`
	State      string `json:"state"`

	ObjectKey        string `json:"-"`
	UploadGeneration int    `json:"-"`
	UploadClaimID    string `json:"-"`
	CiphertextSize   int64  `json:"ciphertext_size"`
	CiphertextSHA256 string `json:"ciphertext_sha256,omitempty"`

	EncryptedMetadata    *EncryptedArtifactMetadata `json:"encrypted_metadata,omitempty"`
	IntentExpiresAt      time.Time                  `json:"-"`
	UploadClaimedAt      *time.Time                 `json:"-"`
	UploadLeaseExpiresAt *time.Time                 `json:"-"`
	ExpiresAt            time.Time                  `json:"expires_at"`
	UploadedAt           *time.Time                 `json:"uploaded_at,omitempty"`
	CompletedAt          *time.Time                 `json:"completed_at,omitempty"`
	RevokedAt            *time.Time                 `json:"revoked_at,omitempty"`
	ObjectDeletedAt      *time.Time                 `json:"-"`
	CreatedAt            time.Time                  `json:"created_at"`
}

// ObjectKeys returns every generation-specific object key that may need
// deletion. Keys are deterministic and never serialized by an API.
func (s ArtifactShare) ObjectKeys() []string {
	keys := make([]string, 0, s.UploadGeneration)
	for generation := 1; generation <= s.UploadGeneration; generation++ {
		keys = append(keys, "artifact-shares/"+s.ID+"/"+strconv.Itoa(generation))
	}
	return keys
}
