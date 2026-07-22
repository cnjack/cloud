package domain

import "time"

// AccountSettings is one account-wide, client-encrypted preferences document.
// Envelope is opaque AES-256-GCM JSON; the service never decrypts or logs it.
type AccountSettings struct {
	UserID    string    `json:"-"`
	Version   int64     `json:"version"`
	Envelope  []byte    `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}
