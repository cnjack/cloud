package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const (
	maxAccountSyncKeyWrap      = 16 << 10
	maxAccountProviderEnvelope = 256 << 10
)

var accountProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type accountSyncKeyView struct {
	State     string          `json:"state"`
	KeyGen    int             `json:"key_gen,omitempty"`
	Status    string          `json:"status,omitempty"`
	Wrap      json.RawMessage `json:"wrap,omitempty"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
}

type accountSyncKeyRequestView struct {
	DeviceID           string          `json:"device_id"`
	DeviceName         string          `json:"device_name,omitempty"`
	Pubkey             string          `json:"pubkey,omitempty"`
	KeyGen             int             `json:"key_gen"`
	Status             string          `json:"status"`
	Wrap               json.RawMessage `json:"wrap,omitempty"`
	ApprovedByDeviceID string          `json:"approved_by_device_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	ResolvedAt         *time.Time      `json:"resolved_at,omitempty"`
}

type initializeAccountSyncKeyReq struct {
	KeyGen int             `json:"key_gen"`
	Wrap   json.RawMessage `json:"wrap"`
}

type respondAccountSyncKeyReq struct {
	Approve bool            `json:"approve"`
	KeyGen  int             `json:"key_gen"`
	Wrap    json.RawMessage `json:"wrap,omitempty"`
}

type accountProviderConfigView struct {
	ProviderID string          `json:"provider_id"`
	Version    int64           `json:"version"`
	Envelope   json.RawMessage `json:"envelope"`
	Deleted    bool            `json:"deleted"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type putAccountProviderConfigReq struct {
	BaseVersion int64           `json:"base_version"`
	Envelope    json.RawMessage `json:"envelope"`
	Deleted     bool            `json:"deleted"`
}

func accountSyncKeyRequestResponse(v *domain.AccountSyncKeyWrap) accountSyncKeyRequestView {
	out := accountSyncKeyRequestView{
		DeviceID: v.DeviceID, DeviceName: v.DeviceName, Pubkey: v.Pubkey,
		KeyGen: v.KeyGen, Status: string(v.Status),
		ApprovedByDeviceID: v.ApprovedByDeviceID,
		CreatedAt:          v.CreatedAt, ResolvedAt: v.ResolvedAt,
	}
	if len(v.Wrap) != 0 {
		out.Wrap = json.RawMessage(v.Wrap)
	}
	return out
}

func accountProviderConfigResponse(v *domain.AccountProviderConfig) accountProviderConfigView {
	return accountProviderConfigView{
		ProviderID: v.ProviderID, Version: v.Version,
		Envelope: json.RawMessage(v.Envelope), Deleted: v.Deleted, UpdatedAt: v.UpdatedAt,
	}
}

// validateX25519Wrap validates the transport container without deriving a shared
// secret or looking at the plaintext. The ASK remains client-only.
func validateX25519Wrap(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > maxAccountSyncKeyWrap {
		return fmt.Errorf("wrap must be between 1 byte and 16 KiB")
	}
	var wrap struct {
		EphemeralPubkey string `json:"ephemeral_pubkey"`
		Nonce           string `json:"nonce"`
		CT              string `json:"ct"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wrap); err != nil {
		return fmt.Errorf("invalid X25519 wrap: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid X25519 wrap: trailing JSON")
	}
	pub, err := base64.StdEncoding.DecodeString(wrap.EphemeralPubkey)
	if err != nil || len(pub) != 32 {
		return fmt.Errorf("ephemeral_pubkey must be a 32-byte X25519 key in base64")
	}
	nonce, err := base64.StdEncoding.DecodeString(wrap.Nonce)
	if err != nil || len(nonce) != 12 {
		return fmt.Errorf("wrap nonce must be 12 bytes of base64")
	}
	ct, err := base64.StdEncoding.DecodeString(wrap.CT)
	if err != nil || len(ct) < 16 {
		return fmt.Errorf("wrap ciphertext must be authenticated base64")
	}
	return nil
}

func (s *Server) handleGetAccountSyncKey(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	key, err := s.st.GetAccountSyncKey(r.Context(), p.deviceUserID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, accountSyncKeyView{State: "uninitialized"})
		return
	}
	if err != nil {
		s.log.Error("get account sync key", "user", p.deviceUserID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the account sync key")
		return
	}
	wrap, err := s.st.GetAccountSyncKeyWrap(r.Context(), p.deviceUserID, p.deviceID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, accountSyncKeyView{
			State: "request_required", KeyGen: key.KeyGen, UpdatedAt: &key.UpdatedAt,
		})
		return
	}
	if err != nil {
		s.log.Error("get account sync key wrap", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the device key state")
		return
	}
	state := "waiting"
	if wrap.Status == domain.AccountSyncKeyApproved && wrap.KeyGen == key.KeyGen {
		state = "ready"
	} else if wrap.Status == domain.AccountSyncKeyDenied {
		state = "denied"
	}
	view := accountSyncKeyView{
		State: state, KeyGen: key.KeyGen, Status: string(wrap.Status), UpdatedAt: &key.UpdatedAt,
	}
	if state == "ready" {
		view.Wrap = json.RawMessage(wrap.Wrap)
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleInitializeAccountSyncKey(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountSyncKeyWrap+4096)
	var req initializeAccountSyncKeyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.KeyGen != 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "the initial key_gen must be 1")
		return
	}
	if err := validateX25519Wrap(req.Wrap); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_key_wrap", err.Error())
		return
	}
	wrap, err := s.st.InitializeAccountSyncKey(
		r.Context(), p.deviceUserID, p.deviceID, req.KeyGen, []byte(req.Wrap), time.Now().UTC(),
	)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "sync_key_exists", "the account sync key is already initialized")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "device_key_required", "register a device identity key before initializing sync")
		return
	}
	if err != nil {
		s.log.Error("initialize account sync key", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not initialize account sync")
		return
	}
	writeJSON(w, http.StatusCreated, accountSyncKeyRequestResponse(wrap))
}

func (s *Server) handleRequestAccountSyncKey(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	wrap, err := s.st.RequestAccountSyncKey(r.Context(), p.deviceUserID, p.deviceID, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "sync_key_uninitialized", "initialize account sync on the first Desktop")
		return
	}
	if err != nil {
		s.log.Error("request account sync key", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not request account sync approval")
		return
	}
	writeJSON(w, http.StatusOK, accountSyncKeyRequestResponse(wrap))
}

func (s *Server) approvedAccountSyncKey(w http.ResponseWriter, r *http.Request, p *principal) (*domain.AccountSyncKeyWrap, bool) {
	key, err := s.st.GetAccountSyncKey(r.Context(), p.deviceUserID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "sync_key_uninitialized", "account sync is not initialized")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load account sync state")
		return nil, false
	}
	wrap, err := s.st.GetAccountSyncKeyWrap(r.Context(), p.deviceUserID, p.deviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (wrap.Status != domain.AccountSyncKeyApproved || wrap.KeyGen != key.KeyGen)) {
		writeError(w, http.StatusForbidden, "sync_key_approval_required", "this Desktop is not approved for account configuration sync")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load device sync approval")
		return nil, false
	}
	return wrap, true
}

func (s *Server) handleListAccountSyncKeyRequests(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, ok := s.approvedAccountSyncKey(w, r, p); !ok {
		return
	}
	status := domain.AccountSyncKeyStatus(r.URL.Query().Get("status"))
	if status == "" {
		status = domain.AccountSyncKeyPending
	}
	if status != domain.AccountSyncKeyPending && status != domain.AccountSyncKeyApproved && status != domain.AccountSyncKeyDenied {
		writeError(w, http.StatusBadRequest, "bad_request", "status must be pending, approved, or denied")
		return
	}
	wraps, err := s.st.ListAccountSyncKeyWraps(r.Context(), p.deviceUserID, status)
	if err != nil {
		s.log.Error("list account sync key requests", "user", p.deviceUserID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list sync approval requests")
		return
	}
	out := make([]accountSyncKeyRequestView, 0, len(wraps))
	for i := range wraps {
		out = append(out, accountSyncKeyRequestResponse(&wraps[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Server) handleRespondAccountSyncKeyRequest(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, ok := s.approvedAccountSyncKey(w, r, p); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountSyncKeyWrap+4096)
	var req respondAccountSyncKeyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.KeyGen < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "key_gen must be at least 1")
		return
	}
	if req.Approve {
		if err := validateX25519Wrap(req.Wrap); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_key_wrap", err.Error())
			return
		}
	} else if len(req.Wrap) != 0 && string(req.Wrap) != "null" {
		writeError(w, http.StatusBadRequest, "bad_request", "a denied request must not include a wrap")
		return
	}
	targetID := r.PathValue("device_id")
	if targetID == "" || targetID == p.deviceID {
		writeError(w, http.StatusBadRequest, "bad_request", "an approved Desktop must review a different device")
		return
	}
	result, err := s.st.RespondAccountSyncKeyRequest(
		r.Context(), p.deviceUserID, p.deviceID, targetID,
		req.Approve, req.KeyGen, []byte(req.Wrap), time.Now().UTC(),
	)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "sync approval request not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "sync_key_conflict", "the request or key generation changed; refresh before retrying")
		return
	}
	if err != nil {
		s.log.Error("respond account sync key request", "target", targetID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve the sync approval request")
		return
	}
	writeJSON(w, http.StatusOK, accountSyncKeyRequestResponse(result))
}

func (s *Server) handleRevokeAccountSyncKeyDevice(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	approver, ok := s.approvedAccountSyncKey(w, r, p)
	if !ok {
		return
	}
	targetID := r.PathValue("device_id")
	if targetID == "" || targetID == p.deviceID {
		writeError(w, http.StatusBadRequest, "bad_request", "an approved Desktop may only revoke a different device")
		return
	}
	result, err := s.st.RevokeAccountSyncKeyWrap(
		r.Context(), p.deviceUserID, p.deviceID, targetID,
		approver.KeyGen, time.Now().UTC(),
	)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "approved Desktop not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "sync_key_conflict", "the device is no longer approved; refresh before retrying")
		return
	}
	if err != nil {
		s.log.Error("revoke account sync device", "target", targetID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke Desktop configuration access")
		return
	}
	writeJSON(w, http.StatusOK, accountSyncKeyRequestResponse(result))
}

func (s *Server) handleListAccountProviderConfigs(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, ok := s.approvedAccountSyncKey(w, r, p); !ok {
		return
	}
	configs, err := s.st.ListAccountProviderConfigs(r.Context(), p.deviceUserID)
	if err != nil {
		s.log.Error("list account provider configs", "user", p.deviceUserID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load provider configurations")
		return
	}
	out := make([]accountProviderConfigView, 0, len(configs))
	for i := range configs {
		out = append(out, accountProviderConfigResponse(&configs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) handleGetAccountProviderConfig(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, ok := s.approvedAccountSyncKey(w, r, p); !ok {
		return
	}
	providerID := r.PathValue("provider_id")
	if !accountProviderIDPattern.MatchString(providerID) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid provider_id")
		return
	}
	config, err := s.st.GetAccountProviderConfig(r.Context(), p.deviceUserID, providerID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "provider configuration not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load provider configuration")
		return
	}
	writeJSON(w, http.StatusOK, accountProviderConfigResponse(config))
}

func (s *Server) handlePutAccountProviderConfig(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, ok := s.approvedAccountSyncKey(w, r, p); !ok {
		return
	}
	providerID := r.PathValue("provider_id")
	if !accountProviderIDPattern.MatchString(providerID) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid provider_id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountProviderEnvelope+4096)
	var req putAccountProviderConfigReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.BaseVersion < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "base_version must be non-negative")
		return
	}
	if err := validateEncryptedEnvelope(req.Envelope, maxAccountProviderEnvelope, "256 KiB"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_provider_envelope", err.Error())
		return
	}
	config, err := s.st.PutAccountProviderConfig(
		r.Context(), p.deviceUserID, providerID, req.BaseVersion,
		[]byte(req.Envelope), req.Deleted, time.Now().UTC(),
	)
	if errors.Is(err, store.ErrConflict) {
		currentVersion := int64(0)
		if current, getErr := s.st.GetAccountProviderConfig(r.Context(), p.deviceUserID, providerID); getErr == nil {
			currentVersion = current.Version
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": errorDetail{
				Code:    "provider_config_conflict",
				Message: "provider configuration changed; fetch and merge before retrying",
			},
			"current_version": currentVersion,
		})
		return
	}
	if err != nil {
		s.log.Error("put account provider config", "user", p.deviceUserID, "provider", providerID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not save provider configuration")
		return
	}
	writeJSON(w, http.StatusOK, accountProviderConfigResponse(config))
}
