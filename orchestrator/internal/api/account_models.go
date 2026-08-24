package api

import (
	"net/http"
	"sort"

	"github.com/cnjack/jcloud/internal/domain"
)

type accountModelView struct {
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	ModelName    string                   `json:"model_name"`
	Capabilities domain.ModelCapabilities `json:"capabilities"`
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
	out := make([]accountModelView, 0, len(models))
	for i := range models {
		out = append(out, accountModelView{
			ID: models[i].ID, Name: models[i].Name, ModelName: models[i].ModelName,
			Capabilities: models[i].Capabilities,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}
