package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const maxAccountSettingsEnvelope = 64 << 10

type accountSettingsView struct {
	Version   int64           `json:"version"`
	Envelope  json.RawMessage `json:"envelope"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
}

type putAccountSettingsReq struct {
	BaseVersion int64           `json:"base_version"`
	Envelope    json.RawMessage `json:"envelope"`
}

func accountSettingsPrincipalUserID(p *principal) string {
	if p == nil {
		return ""
	}
	if p.isDevice() {
		return p.deviceUserID
	}
	return p.userID()
}

func (s *Server) accountSettingsUserID(w http.ResponseWriter, r *http.Request) string {
	userID := accountSettingsPrincipalUserID(principalFrom(r.Context()))
	if userID == "" {
		writeError(w, http.StatusForbidden, "forbidden", "account settings require a user or device identity")
	}
	return userID
}

func accountSettingsResponse(settings *domain.AccountSettings) accountSettingsView {
	if settings == nil {
		return accountSettingsView{Version: 0, Envelope: nil}
	}
	updatedAt := settings.UpdatedAt
	return accountSettingsView{
		Version: settings.Version, Envelope: json.RawMessage(settings.Envelope), UpdatedAt: &updatedAt,
	}
}

// validateAccountSettingsEnvelope validates only the encryption container. It
// deliberately never opens or logs ciphertext; whitelist validation happens
// in the clients before sealing.
func validateAccountSettingsEnvelope(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > maxAccountSettingsEnvelope {
		return fmt.Errorf("envelope must be between 1 byte and 64 KiB")
	}
	var envelope struct {
		Enc    string `json:"enc"`
		KeyGen int    `json:"key_gen"`
		Nonce  string `json:"nonce"`
		CT     string `json:"ct"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("invalid encrypted envelope: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid encrypted envelope: trailing JSON")
	}
	if envelope.Enc != "aes-256-gcm" || envelope.KeyGen < 1 {
		return fmt.Errorf("envelope must use aes-256-gcm with key_gen >= 1")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != 12 {
		return fmt.Errorf("envelope nonce must be 12 bytes of base64")
	}
	ct, err := base64.StdEncoding.DecodeString(envelope.CT)
	if err != nil || len(ct) < 16 {
		return fmt.Errorf("envelope ciphertext must be authenticated base64")
	}
	return nil
}

func (s *Server) handleGetAccountSettings(w http.ResponseWriter, r *http.Request) {
	userID := s.accountSettingsUserID(w, r)
	if userID == "" {
		return
	}
	settings, err := s.st.GetAccountSettings(r.Context(), userID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, accountSettingsResponse(nil))
		return
	}
	if err != nil {
		s.log.Error("get account settings", "user", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load account settings")
		return
	}
	writeJSON(w, http.StatusOK, accountSettingsResponse(settings))
}

func (s *Server) handlePutAccountSettings(w http.ResponseWriter, r *http.Request) {
	userID := s.accountSettingsUserID(w, r)
	if userID == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountSettingsEnvelope+4096)
	var req putAccountSettingsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.BaseVersion < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "base_version must be non-negative")
		return
	}
	if err := validateAccountSettingsEnvelope(req.Envelope); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings_envelope", err.Error())
		return
	}
	settings, err := s.st.PutAccountSettings(r.Context(), userID, req.BaseVersion, []byte(req.Envelope), time.Now().UTC())
	if errors.Is(err, store.ErrConflict) {
		currentVersion := int64(0)
		if current, getErr := s.st.GetAccountSettings(r.Context(), userID); getErr == nil {
			currentVersion = current.Version
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":           errorDetail{Code: "settings_conflict", Message: "account settings changed; fetch and merge before retrying"},
			"current_version": currentVersion,
		})
		return
	}
	if err != nil {
		s.log.Error("put account settings", "user", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not save account settings")
		return
	}
	writeJSON(w, http.StatusOK, accountSettingsResponse(settings))
}
