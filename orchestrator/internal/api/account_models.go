package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/cnjack/jcloud/internal/domain"
)

type accountModelView = modelMemberView

// Provider labels disambiguate identical upstream model names without exposing
// credentials, endpoints, or an unrelated account's provider metadata.
func (s *Server) executionModelViews(ctx context.Context, models []domain.Model) ([]modelMemberView, error) {
	providers, err := s.st.ListModelProviders(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.ModelProvider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	out := make([]modelMemberView, 0, len(models))
	for _, model := range models {
		provider := byID[model.ProviderID]
		authorizationState := ""
		if provider.AuthType == domain.ModelProviderAuthOAuth {
			status, err := s.modelOAuth.Status(ctx, provider.ID)
			if err != nil {
				return nil, err
			}
			authorizationState = status.State
		}
		out = append(out, modelMemberView{AuthorizationState: authorizationState, ID: model.ID, Name: model.Name, ModelName: model.ModelName, Capabilities: model.Capabilities, ProviderID: model.ProviderID, ProviderName: provider.Name, ProviderKind: provider.Kind})
	}
	return out, nil
}

func (s *Server) handleListAccountModels(w http.ResponseWriter, r *http.Request) {
	userID := principalFrom(r.Context()).userID()
	if userID == "" {
		writeError(w, http.StatusForbidden, "account_required", "an Account is required to list execution models")
		return
	}
	models, err := s.st.ListModelsForAccount(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load account models")
		return
	}
	out, err := s.executionModelViews(r.Context(), models)
	if err != nil {
		writeError(w, 500, "internal", "could not load model providers")
		return
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}
