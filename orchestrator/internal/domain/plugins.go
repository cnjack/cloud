package domain

import "time"

// ProviderKind identifies a project plugin. It deliberately includes jtype,
// unlike GitProvider, because Kanban is a first-class project plugin too.
type ProviderKind string

const (
	PluginGitHub ProviderKind = "github"
	PluginGitLab ProviderKind = "gitlab"
	PluginGitea  ProviderKind = "gitea"
	PluginJType  ProviderKind = "jtype"
)

func ValidProviderKind(v ProviderKind) bool {
	switch v {
	case PluginGitHub, PluginGitLab, PluginGitea, PluginJType:
		return true
	}
	return false
}

// PluginStatus is the externally visible lifecycle state of a project plugin.
// Secrets are never represented by a status or exposed by the API.
type PluginStatus string

const (
	PluginStatusConnecting     PluginStatus = "connecting"
	PluginStatusEnabled        PluginStatus = "enabled"
	PluginStatusDisabled       PluginStatus = "disabled"
	PluginStatusActionRequired PluginStatus = "action_required"
	PluginStatusUninstalling   PluginStatus = "uninstalling"
	PluginStatusError          PluginStatus = "error"
)

func ValidPluginStatus(v PluginStatus) bool {
	switch v {
	case PluginStatusConnecting, PluginStatusEnabled, PluginStatusDisabled,
		PluginStatusActionRequired, PluginStatusUninstalling, PluginStatusError:
		return true
	}
	return false
}

// ClusterSettings holds the bootstrap-complete state and the canonical public
// URL used in OAuth callbacks and webhooks. It is a one-row aggregate.
type ClusterSettings struct {
	PublicURL     string    `json:"public_url"`
	SetupComplete bool      `json:"setup_complete"`
	UpdatedBy     string    `json:"updated_by,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ProviderConfig is the cluster-wide configuration for exactly one provider.
// All secret fields are AES-GCM ciphertext and never serialized. ClientID is
// intentionally public metadata; it is stored separately from secret material.
type ProviderConfig struct {
	Provider            ProviderKind `json:"provider"`
	BaseURL             string       `json:"base_url"`
	LoginEnabled        bool         `json:"login_enabled"`
	PluginEnabled       bool         `json:"plugin_enabled"`
	ClientID            string       `json:"client_id,omitempty"`
	ClientSecretEnc     []byte       `json:"-"`
	AppID               string       `json:"app_id,omitempty"`
	AppPrivateKeyEnc    []byte       `json:"-"`
	WebhookSecretEnc    []byte       `json:"-"`
	CapabilityVersion   string       `json:"capability_version,omitempty"`
	Capabilities        []string     `json:"capabilities,omitempty"`
	ConfigRevision      int64        `json:"config_revision"`
	LastHealthError     string       `json:"last_health_error,omitempty"`
	LastCapabilityCheck *time.Time   `json:"last_capability_check,omitempty"`
	UpdatedBy           string       `json:"updated_by,omitempty"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

// PluginInstallation is the one project-owned authorization for a provider.
// Token ciphertext is only consumed by orchestrator-side credential code; API
// views expose TokenSet and never include either encrypted blob.
type PluginInstallation struct {
	ID                string       `json:"id"`
	ProjectID         string       `json:"project_id"`
	Provider          ProviderKind `json:"provider"`
	Status            PluginStatus `json:"status"`
	ExternalAccountID string       `json:"external_account_id,omitempty"`
	ExternalAccount   string       `json:"external_account,omitempty"`
	GitHubInstallID   string       `json:"github_installation_id,omitempty"`
	WorkspaceID       string       `json:"workspace_id,omitempty"`
	Scopes            []string     `json:"scopes,omitempty"`
	AccessTokenEnc    []byte       `json:"-"`
	RefreshTokenEnc   []byte       `json:"-"`
	TokenExpiresAt    *time.Time   `json:"token_expires_at,omitempty"`
	// CredentialVersionID points to an append-only encrypted grant record. It
	// lets running task snapshots retain the launch grant when a reconnect or
	// refresh later replaces this Installation's live credential fields.
	CredentialVersionID string     `json:"-"`
	ConsentVersion      string     `json:"consent_version"`
	ConsentedBy         string     `json:"consented_by,omitempty"`
	ConsentedAt         time.Time  `json:"consented_at"`
	ConfigRevision      int64      `json:"config_revision"`
	LastHealthError     string     `json:"last_health_error,omitempty"`
	LastHealthyAt       *time.Time `json:"last_healthy_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (p PluginInstallation) TokenSet() bool { return len(p.AccessTokenEnc) > 0 }

// PluginCredentialVersion is an encrypted launch-grant identity record. It is
// never serialized over the public API. Reconnect/identity replacement creates
// a new row; OAuth refresh-token rotation updates ciphertext only within the
// same identity so durable runs retain the valid refresh chain.
type PluginCredentialVersion struct {
	ID              string       `json:"-"`
	InstallationID  string       `json:"-"`
	Provider        ProviderKind `json:"-"`
	GitHubInstallID string       `json:"-"`
	AccessTokenEnc  []byte       `json:"-"`
	RefreshTokenEnc []byte       `json:"-"`
	TokenExpiresAt  *time.Time   `json:"-"`
	CreatedAt       time.Time    `json:"-"`
}

// ServiceRepositoryBinding makes a Service's SCM repository explicitly owned
// by a project plugin installation. ProviderRepoID is a stable provider ID,
// so renames do not change the binding.
type ServiceRepositoryBinding struct {
	ServiceID      string    `json:"service_id"`
	InstallationID string    `json:"installation_id"`
	ProviderRepoID string    `json:"provider_repo_id"`
	RepositoryPath string    `json:"repository_path"`
	CloneURL       string    `json:"clone_url"`
	DefaultBranch  string    `json:"default_branch"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PluginAutomation is the unified automation aggregate. Trigger-specific data
// lives in one of the strongly typed trigger tables rather than an unvalidated
// JSON blob.
type PluginAutomation struct {
	ID              string     `json:"id"`
	ServiceID       string     `json:"service_id"`
	InstallationID  string     `json:"installation_id,omitempty"`
	Name            string     `json:"name"`
	TriggerKind     string     `json:"trigger_kind"` // scm | kanban | cron
	PromptTemplate  string     `json:"prompt_template"`
	ModelID         string     `json:"model_id,omitempty"`
	ModelEffort     string     `json:"model_effort,omitempty"`
	Enabled         bool       `json:"enabled"`
	IgnoreJCode     bool       `json:"ignore_jcode"`
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	LastRunID       string     `json:"last_run_id,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedBy       string     `json:"created_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// PluginAutomationSpec is the aggregate returned by the Automation API. Exactly
// one trigger pointer is populated, matching Automation.TriggerKind. Keeping the
// typed children together at the API/store boundary prevents callers from
// having to reconstruct configuration from unvalidated JSON.
type PluginAutomationSpec struct {
	Automation PluginAutomation `json:"automation"`
	SCM        *SCMTrigger      `json:"scm,omitempty"`
	Actions    []SCMAction      `json:"actions,omitempty"`
	Kanban     *KanbanTrigger   `json:"kanban,omitempty"`
	Cron       *CronTrigger     `json:"cron,omitempty"`
}

func ValidPluginAutomationTrigger(v string) bool {
	return v == "scm" || v == "kanban" || v == "cron"
}

type SCMTrigger struct {
	AutomationID string `json:"automation_id"`
	Branch       string `json:"branch,omitempty"`
	PathPattern  string `json:"path_pattern,omitempty"`
	Conclusion   string `json:"conclusion,omitempty"`
}

type SCMAction struct {
	AutomationID string `json:"automation_id"`
	ServiceID    string `json:"service_id"`
	EventFamily  string `json:"event_family"`
	Action       string `json:"action"`
}

type KanbanTrigger struct {
	AutomationID   string `json:"automation_id"`
	InstallationID string `json:"installation_id"`
	BoardRef       string `json:"board_ref"`
	TriggerColumn  string `json:"trigger_column"`
	DoneColumn     string `json:"done_column,omitempty"`
}

type CronTrigger struct {
	AutomationID string     `json:"automation_id"`
	CronExpr     string     `json:"cron_expr"`
	LastFiredAt  *time.Time `json:"last_fired_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// WebhookReceipt contains only the normalized and whitelisted delivery facts;
// raw provider payloads and request headers are intentionally never persisted.
type WebhookReceipt struct {
	ID         string       `json:"id"`
	Provider   ProviderKind `json:"provider"`
	DeliveryID string       `json:"delivery_id"`
	// PayloadDigest is a SHA-256 derivation of an already-verified GitHub/Gitea
	// body HMAC. It prevents replay by changing the unsigned delivery-id header,
	// scopes Gitea duplicates to their per-binding secret, and never retains the
	// Provider's raw payload or signature.
	PayloadDigest       string    `json:"-"`
	InstallationID      string    `json:"installation_id,omitempty"`
	EventFamily         string    `json:"event_family"`
	Action              string    `json:"action"`
	ExternalActorID     string    `json:"external_actor_id,omitempty"`
	ExternalActor       string    `json:"external_actor,omitempty"`
	ObjectRef           string    `json:"object_ref,omitempty"`
	Status              string    `json:"status"`
	MatchedAutomationID string    `json:"matched_automation_id,omitempty"`
	Error               string    `json:"error,omitempty"`
	ReceivedAt          time.Time `json:"received_at"`
}

type RunPluginSnapshot struct {
	RunID          string `json:"run_id"`
	InstallationID string `json:"installation_id"`

	// These are references to append-only Provider and credential versions. The
	// version material is joined only in the control plane and intentionally has
	// no JSON representation: an in-flight run must never send its old grant to
	// a newly configured provider URL (or inherit a later reconnect's identity).
	// Task pods get a short-lived access token through the run-scoped endpoint.
	Provider               ProviderKind `json:"-"`
	ProviderConfigRevision int64        `json:"-"`
	CredentialVersionID    string       `json:"-"`

	// The following fields are hydrated from the referenced immutable version
	// rows by Store.ListRunPluginSnapshots. They are never persisted in
	// run_plugin_snapshots itself and never serialized to callers.
	ProviderBaseURL          string     `json:"-"`
	ProviderClientID         string     `json:"-"`
	ProviderClientSecretEnc  []byte     `json:"-"`
	ProviderAppID            string     `json:"-"`
	ProviderAppPrivateKeyEnc []byte     `json:"-"`
	GitHubInstallID          string     `json:"-"`
	AccessTokenEnc           []byte     `json:"-"`
	RefreshTokenEnc          []byte     `json:"-"`
	TokenExpiresAt           *time.Time `json:"-"`
	CreatedAt                time.Time  `json:"created_at"`
}

// FrozenInstallation returns only the launch-time authorization material used
// by the credential issuer. It deliberately omits project/account metadata so
// callers must separately verify the still-present installation belongs to the
// requesting run's project before using this snapshot.
func (s RunPluginSnapshot) FrozenInstallation() *PluginInstallation {
	return &PluginInstallation{
		ID:              s.InstallationID,
		Provider:        s.Provider,
		Status:          PluginStatusEnabled,
		GitHubInstallID: s.GitHubInstallID,
		AccessTokenEnc:  append([]byte(nil), s.AccessTokenEnc...),
		RefreshTokenEnc: append([]byte(nil), s.RefreshTokenEnc...),
		TokenExpiresAt:  clonePluginSnapshotTime(s.TokenExpiresAt),
		ConfigRevision:  s.ProviderConfigRevision,
	}
}

// FrozenProviderConfig returns the configuration that was current when the
// run launched. PluginEnabled is true by construction: later disablement blocks
// new snapshots but must not invalidate a durable run snapshot.
func (s RunPluginSnapshot) FrozenProviderConfig() *ProviderConfig {
	return &ProviderConfig{
		Provider:         s.Provider,
		BaseURL:          s.ProviderBaseURL,
		PluginEnabled:    true,
		ClientID:         s.ProviderClientID,
		ClientSecretEnc:  append([]byte(nil), s.ProviderClientSecretEnc...),
		AppID:            s.ProviderAppID,
		AppPrivateKeyEnc: append([]byte(nil), s.ProviderAppPrivateKeyEnc...),
		ConfigRevision:   s.ProviderConfigRevision,
	}
}

func (s RunPluginSnapshot) HasFrozenRuntimeMaterial() bool {
	if !ValidProviderKind(s.Provider) || s.ProviderConfigRevision <= 0 || s.ProviderBaseURL == "" {
		return false
	}
	if s.Provider == PluginGitHub {
		return s.GitHubInstallID != "" && s.ProviderAppID != "" && len(s.ProviderAppPrivateKeyEnc) > 0
	}
	return len(s.AccessTokenEnc) > 0
}

func clonePluginSnapshotTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}

type PluginAuditEvent struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	InstallationID string    `json:"installation_id,omitempty"`
	ActorUserID    string    `json:"actor_user_id,omitempty"`
	EventType      string    `json:"event_type"`
	Detail         string    `json:"detail,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type PluginKanbanClaim struct {
	AutomationID   string     `json:"automation_id"`
	InstallationID string     `json:"installation_id"`
	DocumentID     string     `json:"document_id"`
	DocumentPath   string     `json:"document_path"`
	WorkspaceID    string     `json:"workspace_id"`
	DoneColumn     string     `json:"done_column,omitempty"`
	RunID          string     `json:"run_id,omitempty"`
	WritebackAt    *time.Time `json:"writeback_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}
