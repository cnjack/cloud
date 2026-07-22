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
	// Platform is the connector flavor ("desktop"|"cli"), reported at register
	// time. Empty until the first register call; unknown values pass through
	// so future platforms need no server change.
	Platform string `json:"platform,omitempty"`
	// Pubkey is the device's X25519 public key (base64), published at register
	// time for the E2EE key exchange (docs/17 §6). Empty until the first
	// register call — the row predates it (created at token issuance).
	Pubkey string `json:"pubkey,omitempty"`
	// KeyGen is the current CEK generation (docs/17 §6.1). 1 until E2EE lands.
	KeyGen int `json:"key_gen"`
	// Capabilities is the connector's compose-capability mirror (M12):
	// {projects:[{path,name}], models:[{provider,id,label}], efforts:[...]} as
	// reported on the sessions upsert. OPAQUE to the server (stored verbatim,
	// never parsed); nil until a connector new enough to send it upserts.
	Capabilities []byte `json:"-"` // opaque JSON blob (json.RawMessage at the API edge)
	// E2EE is the connector's reported encryption state (M13, migration 0035):
	// true means the device seals uplink with an active CEK and accepts only
	// {envelope} downlink command payloads (the pairing gate, docs/17 §6.7).
	E2EE bool `json:"e2ee"`
	// FingerprintHash is the sha256 hex of the device's stable machine
	// fingerprint (M16, migration 0036): the CLI hashes its hardware id (or a
	// persisted fallback) and only the hash ever leaves the machine. It is the
	// login dedup key — at most one non-revoked device per (user, hash).
	// Empty for devices minted before M16.
	FingerprintHash string     `json:"fingerprint_hash,omitempty"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
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
	// DeviceCmdWorkspaceBrowse asks the desktop to list one local directory.
	// The result is returned through the command ack and remains opaque to the
	// orchestrator when E2EE is active.
	DeviceCmdWorkspaceBrowse = "workspace.browse"
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
	DevicePairingRevoked  = "revoked"
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

// DevicePairingOfferWindow is how long a QR pairing offer stays claimable
// (docs/17 §6.3 — M11 scan-to-pair): the desktop shows the QR while the offer
// is live; after the window the claim endpoint answers 410.
const DevicePairingOfferWindow = 10 * time.Minute

// DevicePairingOffer is a device-issued, single-use ticket that lets a client
// in front of the device's QR code start a CEK pairing WITHOUT owning the
// device on the cloud account yet. The device mints it (secret returned once,
// rendered into the QR); only the SHA-256 hash is persisted. A successful
// claim stamps claimed_by/claimed_at, turning the offer single-use.
type DevicePairingOffer struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"-"`
	SecretHash string     `json:"-"`
	ClaimedBy  *string    `json:"-"`
	ClaimedAt  *time.Time `json:"claimed_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}
