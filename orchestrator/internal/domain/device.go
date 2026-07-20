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

// Device session routing states (docs/17 §5). Status is PLAINTEXT so list UIs
// can render it without the CEK; everything else about a session is opaque.
const (
	DeviceSessionIdle    = "idle"
	DeviceSessionRunning = "running"
)

// DeviceSession is the cloud mirror of one local jcode session's metadata
// (docs/17 §2.1). Meta is OPAQUE to the server: in the plaintext relay phase
// (M3) it is the SessionMeta JSON, from M5 it is the E2EE ciphertext — the
// server stores and returns the bytes verbatim and never parses them.
type DeviceSession struct {
	DeviceID  string    `json:"-"`
	SessionID string    `json:"session_id"`
	Status    string    `json:"status"`
	Meta      []byte    `json:"-"` // opaque JSON blob (json.RawMessage at the API edge)
	UpdatedAt time.Time `json:"updated_at"`
}

// DeviceEvent is one durable event in a device session's append-only log
// (docs/17 §4.4). (DeviceID, SessionID, Seq) is the idempotency key — a
// redelivered seq is skipped. Kind is PLAINTEXT (the server routes/renders the
// skeleton); Envelope is the OPAQUE payload blob (plaintext JSON in M3, E2EE
// ciphertext from M5) stored verbatim, never parsed.
type DeviceEvent struct {
	DeviceID  string    `json:"-"`
	SessionID string    `json:"session_id"`
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Envelope  []byte    `json:"-"` // opaque payload blob (json.RawMessage at the API edge)
	CreatedAt time.Time `json:"ts"`
}

// Downlink command kinds (orchestrator → device, docs/17 §4.2). The payload
// contract per kind lives in docs/17 and the wire spec; the server builds
// chat.* payloads itself and treats everything as opaque bytes afterwards.
const (
	DeviceCmdChatSend        = "chat.send"
	DeviceCmdChatStop        = "chat.stop"
	DeviceCmdApprovalRespond = "approval.respond"
	// DeviceCmdPairingRequest asks the device to approve/deny a client pairing
	// (docs/17 §6.3). Payload {pairing_id, label, kty, pubkey} — plaintext
	// routing metadata, never CEK-bearing.
	DeviceCmdPairingRequest = "pairing.request"
)

// DeviceCommand lifecycle states (docs/17 §5): pending (queued) → delivered
// (handed to the device by a poll) → acked (executed ok) / failed (executed
// with error).
const (
	DeviceCommandPending   = "pending"
	DeviceCommandDelivered = "delivered"
	DeviceCommandAcked     = "acked"
	DeviceCommandFailed    = "failed"
)

// DeviceCommand is one queued downlink instruction for a device (docs/17
// §4.2). SessionID is nil for a command that starts a NEW session (chat.send
// with a null session_id — the jcode connector allocates the local session id
// and mirrors it back via the sessions upsert). Envelope/Result are OPAQUE
// JSON blobs (E2EE ciphertext from M5); the server never parses them.
type DeviceCommand struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"-"`
	Kind      string     `json:"kind"`
	SessionID *string    `json:"session_id"`
	Envelope  []byte     `json:"-"` // opaque payload blob (json.RawMessage at the API edge)
	Status    string     `json:"status"`
	Result    []byte     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
}

// Device pairing lifecycle states (docs/17 §6.3): pending (awaiting the
// on-device approval) → approved (wrap stored) / denied; pending rows past the
// 10-minute window read as expired.
const (
	DevicePairingPending  = "pending"
	DevicePairingApproved = "approved"
	DevicePairingDenied   = "denied"
	DevicePairingExpired  = "expired"
)

// DevicePairingWindow is how long a pending pairing waits for the on-device
// approval before it reads as expired (docs/17 §6.3).
const DevicePairingWindow = 10 * time.Minute

// DevicePairing is one client's request for the device's CEK (docs/17 §6.3).
// Label/Pubkey are the requester's P-256 identity (SPKI, base64); Wrap is the
// approved device's ECIES-wrapped CEK blob — OPAQUE JSON to the server, stored
// and returned verbatim, unwrapped only by the requesting client.
type DevicePairing struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"-"`
	Label      string     `json:"label"`
	Pubkey     string     `json:"pubkey"`
	Status     string     `json:"status"`
	Wrap       []byte     `json:"-"` // opaque wrap blob (json.RawMessage at the API edge)
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}
