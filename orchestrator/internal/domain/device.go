package domain

import "time"

// Device is one local jcode installation registered with the cloud
// (docs/17-jcode-device-relay). It is created when its device token is issued
// (the RFC 8628 token poll) and enriched by the first /device/register call
// (hostname, version, pubkey). LastSeenAt is stamped by register + the 30s
// heartbeat; the online signal (>90s stale = offline) is derived from it by
// readers, not stored.
type Device struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// Name is user-editable; it defaults to the CLI's client_name at issuance.
	Name         string `json:"name"`
	Hostname     string `json:"hostname,omitempty"`
	JcodeVersion string `json:"jcode_version,omitempty"`
	// Pubkey is the device's X25519 public key (base64), published at register
	// time for the E2EE key exchange (docs/17 §6). Empty until the first
	// register call — the row predates it (created at token issuance).
	Pubkey string `json:"pubkey,omitempty"`
	// KeyGen is the current CEK generation (docs/17 §6.1). 1 until E2EE lands.
	KeyGen     int        `json:"key_gen"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// DeviceToken is a device principal credential (docs/17 §3.2). Only TokenHash
// (SHA-256) is ever persisted; the plaintext is returned exactly once at
// issuance and never read back (same discipline as APIKey/Session tokens).
type DeviceToken struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`
	// UserID is the owning user, joined from devices (it is NOT a column on
	// device_tokens) so principal resolution can carry it without a second
	// lookup.
	UserID string `json:"user_id"`
	// TokenHash is the sha256 hex of the plaintext token. Never serialized.
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	// RevokedAt, once set, makes the token unresolvable on the VERY NEXT
	// lookup (GetDeviceTokenByHash excludes revoked rows, and joins devices to
	// exclude revoked devices) — revocation is effective immediately, no cache
	// to invalidate.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
