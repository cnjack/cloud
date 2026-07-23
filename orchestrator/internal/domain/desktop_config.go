package domain

import "time"

// AccountSyncKey records only the current ASK generation. The key itself never
// reaches the control plane; approved per-device wraps live separately.
type AccountSyncKey struct {
	UserID    string    `json:"-"`
	KeyGen    int       `json:"key_gen"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AccountSyncKeyStatus string

const (
	AccountSyncKeyPending  AccountSyncKeyStatus = "pending"
	AccountSyncKeyApproved AccountSyncKeyStatus = "approved"
	AccountSyncKeyDenied   AccountSyncKeyStatus = "denied"
)

// AccountSyncKeyWrap is an opaque X25519 envelope for one Desktop. DeviceName
// and Pubkey are joined response metadata and are not stored on this row.
type AccountSyncKeyWrap struct {
	UserID             string               `json:"-"`
	DeviceID           string               `json:"device_id"`
	DeviceName         string               `json:"device_name,omitempty"`
	Pubkey             string               `json:"pubkey,omitempty"`
	KeyGen             int                  `json:"key_gen"`
	Status             AccountSyncKeyStatus `json:"status"`
	Wrap               []byte               `json:"-"`
	ApprovedByDeviceID string               `json:"approved_by_device_id,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	ResolvedAt         *time.Time           `json:"resolved_at,omitempty"`
}

// AccountProviderConfig is one ASK-encrypted Desktop provider configuration.
// Per-provider CAS and tombstones avoid whole-document lost updates.
type AccountProviderConfig struct {
	UserID     string    `json:"-"`
	ProviderID string    `json:"provider_id"`
	Version    int64     `json:"version"`
	Envelope   []byte    `json:"-"`
	Deleted    bool      `json:"deleted"`
	UpdatedAt  time.Time `json:"updated_at"`
}
