package domain

// AccountPreferences are private, persisted defaults for new tasks and clients.
// They never grant access: selected models are reauthorized when a task starts.
type AccountPreferences struct {
	DefaultModelID string `json:"default_model_id"`
	PermissionMode string `json:"permission_mode"`
	Effort         string `json:"effort"`
	SendKey        string `json:"send_key"`
	Language       string `json:"language"`
	Theme          string `json:"theme"`
}

type AccountProfile struct {
	DisplayName string             `json:"display_name"`
	Preferences AccountPreferences `json:"preferences"`
}
