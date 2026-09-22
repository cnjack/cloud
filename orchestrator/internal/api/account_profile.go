package api

import (
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cnjack/jcloud/internal/domain"
)

func (s *Server) handleGetAccountProfile(w http.ResponseWriter, r *http.Request) {
	userID := principalFrom(r.Context()).userID()
	if userID == "" {
		writeError(w, 403, "account_required", "sign in with an account to manage preferences")
		return
	}
	settings, err := s.st.GetAccountProfile(r.Context(), userID)
	if err != nil {
		writeError(w, 500, "internal", "could not load account profile")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateAccountProfile(w http.ResponseWriter, r *http.Request) {
	userID := principalFrom(r.Context()).userID()
	if userID == "" {
		writeError(w, 403, "account_required", "sign in with an account to manage preferences")
		return
	}
	var settings domain.AccountProfile
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, 400, "bad_request", "invalid account profile")
		return
	}
	settings.DisplayName = strings.TrimSpace(settings.DisplayName)
	p := settings.Preferences
	if settings.DisplayName == "" || utf8.RuneCountInString(settings.DisplayName) > 100 {
		writeError(w, 400, "invalid_display_name", "display name must contain 1 to 100 characters")
		return
	}
	if !slices.Contains([]string{"", "approval", "auto", "plan"}, p.PermissionMode) ||
		!slices.Contains([]string{"", "auto", "low", "medium", "high"}, p.Effort) ||
		!slices.Contains([]string{"", "enter", "mod_enter"}, p.SendKey) ||
		!slices.Contains([]string{"", "en", "zh-Hans", "zh-Hant", "ja", "ko"}, p.Language) ||
		!slices.Contains([]string{"", "light", "dark"}, p.Theme) {
		writeError(w, 400, "invalid_preferences", "unsupported task or interface preference")
		return
	}
	if p.DefaultModelID != "" {
		models, err := s.st.ListModelsForAccount(r.Context(), userID)
		if err != nil {
			writeError(w, 500, "internal", "could not verify model access")
			return
		}
		available := slices.ContainsFunc(models, func(m domain.Model) bool { return m.ID == p.DefaultModelID && m.Capabilities.Tools })
		if !available {
			writeError(w, 409, "model_not_authorized", "choose an available account model in model settings")
			return
		}
	}
	if err := s.st.UpdateAccountProfile(r.Context(), userID, settings); err != nil {
		writeError(w, 500, "internal", "could not save account profile")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
