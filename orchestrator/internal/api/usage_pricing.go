package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

var usageCurrencyPattern = regexp.MustCompile(`^[A-Z]{3,8}$`)

type createModelPricingRevisionRequest struct {
	Currency                   string     `json:"currency"`
	InputMicrosPerMillion      *int64     `json:"input_micros_per_million"`
	OutputMicrosPerMillion     *int64     `json:"output_micros_per_million"`
	CacheReadMicrosPerMillion  *int64     `json:"cache_read_micros_per_million"`
	CacheWriteMicrosPerMillion *int64     `json:"cache_write_micros_per_million"`
	EffectiveAt                *time.Time `json:"effective_at"`
}

func (s *Server) handleListModelPricingRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	values, err := s.st.ListModelPricingRevisions(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list model pricing revisions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pricing_revisions": values})
}

func (s *Server) handleCreateModelPricingRevision(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	var req createModelPricingRevisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if !usageCurrencyPattern.MatchString(currency) {
		writeError(w, http.StatusBadRequest, "bad_request", "currency must be 3 to 8 uppercase letters")
		return
	}
	if req.EffectiveAt == nil || req.EffectiveAt.IsZero() {
		writeError(w, http.StatusBadRequest, "bad_request", "effective_at is required")
		return
	}
	rates := []*int64{
		req.InputMicrosPerMillion, req.OutputMicrosPerMillion,
		req.CacheReadMicrosPerMillion, req.CacheWriteMicrosPerMillion,
	}
	hasRate := false
	for _, rate := range rates {
		if rate == nil {
			continue
		}
		hasRate = true
		if *rate < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "pricing rates must be non-negative")
			return
		}
	}
	if !hasRate {
		writeError(w, http.StatusBadRequest, "bad_request", "at least one pricing rate is required")
		return
	}
	model, err := s.st.GetModel(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model")
		return
	}
	providerName := ""
	if model.ProviderID != "" {
		provider, providerErr := s.st.GetModelProvider(r.Context(), model.ProviderID)
		if providerErr != nil && !errors.Is(providerErr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
			return
		}
		if provider != nil {
			providerName = provider.Name
		}
	}
	now := time.Now().UTC()
	revision := &domain.ModelPricingRevision{
		ID: domain.NewID(), ModelResourceID: model.ID,
		ProviderID: model.ProviderID, ProviderName: providerName, ModelName: model.ModelName,
		Currency:                   currency,
		InputMicrosPerMillion:      req.InputMicrosPerMillion,
		OutputMicrosPerMillion:     req.OutputMicrosPerMillion,
		CacheReadMicrosPerMillion:  req.CacheReadMicrosPerMillion,
		CacheWriteMicrosPerMillion: req.CacheWriteMicrosPerMillion,
		EffectiveAt:                req.EffectiveAt.UTC(), CreatedBy: principalFrom(r.Context()).userID(), CreatedAt: now,
	}
	if err = s.st.CreateModelPricingRevision(r.Context(), revision); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create model pricing revision")
		return
	}
	writeJSON(w, http.StatusCreated, revision)
}
