package domain

import "time"

type UsageSubjectKind string

const (
	UsageSubjectRun    UsageSubjectKind = "run"
	UsageSubjectDevice UsageSubjectKind = "device"
)

type UsageCaptureStatus string

const (
	UsageCaptureReported    UsageCaptureStatus = "reported"
	UsageCapturePartial     UsageCaptureStatus = "partial"
	UsageCaptureUnavailable UsageCaptureStatus = "unavailable"
	UsageCaptureParseError  UsageCaptureStatus = "parse_error"
)

// UsageEvent is an append-only normalized observation. Display dimensions are
// immutable snapshots and must never be used for authorization.
type UsageEvent struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`

	SubjectKind UsageSubjectKind `json:"subject_kind"`
	SubjectID   string           `json:"subject_id"`
	RunID       string           `json:"run_id,omitempty"`

	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
	ServiceName string `json:"service_name,omitempty"`

	AutomationID   string `json:"automation_id,omitempty"`
	AutomationName string `json:"automation_name,omitempty"`
	CardWorkspace  string `json:"card_workspace,omitempty"`
	CardDocumentID string `json:"card_document_id,omitempty"`
	CardPath       string `json:"card_path,omitempty"`

	AccountableUserID string `json:"accountable_user_id,omitempty"`
	AccountableLabel  string `json:"accountable_label,omitempty"`

	UserID         string `json:"user_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	GrantScope     string `json:"grant_scope,omitempty"`
	GrantScopeID   string `json:"grant_scope_id,omitempty"`
	GrantScopeName string `json:"grant_scope_name,omitempty"`

	ProviderID   string `json:"provider_id,omitempty"`
	ProviderKind string `json:"provider_kind,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	ModelID      string `json:"model_id,omitempty"`
	ModelName    string `json:"model_name,omitempty"`

	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`

	ReportedCostMicros       *int64 `json:"reported_cost_micros,omitempty"`
	ReportedCurrency         string `json:"reported_currency,omitempty"`
	PricingRevisionID        string `json:"pricing_revision_id,omitempty"`
	EstimatedCostMicros      *int64 `json:"estimated_cost_micros,omitempty"`
	EstimatedCurrency        string `json:"estimated_currency,omitempty"`
	UncostedInputTokens      int64  `json:"uncosted_input_tokens,omitempty"`
	UncostedOutputTokens     int64  `json:"uncosted_output_tokens,omitempty"`
	UncostedCacheReadTokens  int64  `json:"uncosted_cache_read_tokens,omitempty"`
	UncostedCacheWriteTokens int64  `json:"uncosted_cache_write_tokens,omitempty"`

	CaptureStatus UsageCaptureStatus `json:"capture_status"`
	ErrorCategory string             `json:"error_category,omitempty"`
	OccurredAt    time.Time          `json:"occurred_at"`
	CreatedAt     time.Time          `json:"created_at"`
	ReplacementOf string             `json:"replacement_of,omitempty"`
	Version       int                `json:"version"`
}

// ModelPricingRevision is immutable once created. Pointer rates distinguish a
// deliberately free category (0) from an unconfigured category (nil).
type ModelPricingRevision struct {
	ID                         string    `json:"id"`
	ModelResourceID            string    `json:"model_id"`
	ProviderID                 string    `json:"provider_id,omitempty"`
	ProviderName               string    `json:"provider_name,omitempty"`
	ModelName                  string    `json:"model_name"`
	Currency                   string    `json:"currency"`
	InputMicrosPerMillion      *int64    `json:"input_micros_per_million,omitempty"`
	OutputMicrosPerMillion     *int64    `json:"output_micros_per_million,omitempty"`
	CacheReadMicrosPerMillion  *int64    `json:"cache_read_micros_per_million,omitempty"`
	CacheWriteMicrosPerMillion *int64    `json:"cache_write_micros_per_million,omitempty"`
	EffectiveAt                time.Time `json:"effective_at"`
	CreatedBy                  string    `json:"created_by,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
}

type UsageCaptureCounts struct {
	Reported    int64 `json:"reported"`
	Partial     int64 `json:"partial"`
	Unavailable int64 `json:"unavailable"`
	ParseError  int64 `json:"parse_error"`
}

type UsageTokenTotals struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

type UsageMoneyTotal struct {
	Currency           string   `json:"currency"`
	Micros             int64    `json:"micros"`
	PricingRevisionIDs []string `json:"pricing_revision_ids,omitempty"`
}

type UsageUncostedTotal struct {
	Category string `json:"category"`
	Tokens   int64  `json:"tokens"`
}

type UsageCostTotals struct {
	Reported  []UsageMoneyTotal    `json:"reported"`
	Estimated []UsageMoneyTotal    `json:"estimated"`
	Uncosted  []UsageUncostedTotal `json:"uncosted"`
}

type UsageSummary struct {
	Availability string             `json:"availability"`
	Reason       string             `json:"reason,omitempty"`
	Requests     int64              `json:"requests"`
	Capture      UsageCaptureCounts `json:"capture"`
	Tokens       UsageTokenTotals   `json:"tokens"`
	Costs        UsageCostTotals    `json:"costs"`
	From         *time.Time         `json:"from,omitempty"`
	To           *time.Time         `json:"to,omitempty"`
}

type UsageSummaryQuery struct {
	SubjectKind   UsageSubjectKind
	SubjectID     string
	RunID         string
	ProjectID     string
	ServiceID     string
	AutomationID  string
	CardWorkspace string
	CardPath      string
	UserID        string
	DeviceID      string
	ModelID       string
	GrantScope    string
	GrantScopeID  string
	From          *time.Time
	To            *time.Time
}

type RunUsageDimensions struct {
	ProjectID         string
	ProjectName       string
	ServiceID         string
	ServiceName       string
	AutomationID      string
	AutomationName    string
	CardWorkspace     string
	CardDocumentID    string
	CardPath          string
	AccountableUserID string
	AccountableLabel  string
}

type UsageGroup struct {
	Kind    string       `json:"kind"`
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Summary UsageSummary `json:"summary"`
}
