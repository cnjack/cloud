package api

import (
	"net/http"
	"sort"

	"github.com/cnjack/jcloud/internal/domain"
)

type deviceCloudModelView struct {
	ModelID         string                   `json:"model_id"`
	ProviderID      string                   `json:"provider_id"`
	Kind            string                   `json:"kind"`
	ProviderName    string                   `json:"provider_name"`
	ModelName       string                   `json:"model_name"`
	UpstreamModelID string                   `json:"upstream_model_id"`
	Scope           string                   `json:"scope"`
	ScopeID         string                   `json:"scope_id"`
	ScopeName       string                   `json:"scope_name,omitempty"`
	Capabilities    domain.ModelCapabilities `json:"capabilities"`
	ContextWindow   int                      `json:"context_window"`
}

// accessibleDeviceCloudModels is the single authorization definition shared
// by catalog listing and proxying. A model is visible when it is granted to the
// Account directly, belongs to one of the user's projects and is enabled,
// or is a cluster model granted to one of those projects. Direct Account grants
// win display metadata when the same model is also reachable through a Project.
// The stable model id remains the authorization identity.
func (s *Server) accessibleDeviceCloudModels(r *http.Request, userID string) (map[string]deviceCloudModelView, error) {
	out := make(map[string]deviceCloudModelView)
	providers := make(map[string]*domain.ModelProvider)
	accountModels, err := s.st.ListModelsForAccount(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	for i := range accountModels {
		model := &accountModels[i]
		provider := providers[model.ProviderID]
		if provider == nil {
			provider, err = s.st.GetModelProvider(r.Context(), model.ProviderID)
			if err != nil {
				return nil, err
			}
			providers[model.ProviderID] = provider
		}
		out[model.ID] = deviceCloudModelView{
			ModelID: model.ID, ProviderID: model.ProviderID,
			Kind: provider.Kind, ProviderName: provider.Name,
			ModelName: model.Name, UpstreamModelID: model.ModelID,
			Scope: "account", ScopeID: userID, ScopeName: "Account",
			Capabilities: model.Capabilities, ContextWindow: model.ContextWindow,
		}
	}
	projects, err := s.st.ListProjectsForUser(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		models, err := s.st.ListModelsForProject(r.Context(), project.ID)
		if err != nil {
			return nil, err
		}
		for i := range models {
			model := &models[i]
			if _, exists := out[model.ID]; exists {
				continue
			}
			provider := providers[model.ProviderID]
			if provider == nil {
				provider, err = s.st.GetModelProvider(r.Context(), model.ProviderID)
				if err != nil {
					return nil, err
				}
				providers[model.ProviderID] = provider
			}
			scope, scopeID, scopeName := "cluster", "cluster", "Cluster"
			if model.ProjectID != "" {
				scope, scopeID, scopeName = "project", model.ProjectID, project.Name
			}
			out[model.ID] = deviceCloudModelView{
				ModelID: model.ID, ProviderID: model.ProviderID,
				Kind: provider.Kind, ProviderName: provider.Name,
				ModelName: model.Name, UpstreamModelID: model.ModelID,
				Scope: scope, ScopeID: scopeID, ScopeName: scopeName,
				Capabilities: model.Capabilities, ContextWindow: model.ContextWindow,
			}
		}
	}
	return out, nil
}

func (s *Server) handleListDeviceCloudModels(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	available, err := s.accessibleDeviceCloudModels(r, p.deviceUserID)
	if err != nil {
		s.log.Error("list device cloud models", "user", p.deviceUserID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load Cloud models")
		return
	}
	out := make([]deviceCloudModelView, 0, len(available))
	for _, model := range available {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderName != out[j].ProviderName {
			return out[i].ProviderName < out[j].ProviderName
		}
		if out[i].ModelName != out[j].ModelName {
			return out[i].ModelName < out[j].ModelName
		}
		return out[i].ModelID < out[j].ModelID
	})
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (s *Server) handleDeviceCloudModelProxy(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	modelID := r.PathValue("model_id")
	if modelID == "" {
		writeError(w, http.StatusNotFound, "cloud_model_not_found", "Cloud model not found")
		return
	}
	available, err := s.accessibleDeviceCloudModels(r, p.deviceUserID)
	if err != nil {
		s.log.Error("authorize device cloud model", "user", p.deviceUserID, "model", modelID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "cloud_model_unavailable", "Cloud model access could not be verified")
		return
	}
	authorized, ok := available[modelID]
	if !ok {
		// Existing-but-ungranted and unknown ids intentionally share the same
		// response so a device cannot probe model resources outside its account.
		// Desktop marks the exact Cloud selection unavailable and never falls
		// back to a same-named local model.
		writeError(w, http.StatusNotFound, "cloud_model_not_found", "Cloud model not found")
		return
	}
	resolved, err := s.models.ResolveModel(r.Context(), modelID)
	if err != nil {
		s.log.Error("resolve device cloud model", "device", p.deviceID, "model", modelID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "cloud_model_unavailable", "the selected Cloud model is unavailable")
		return
	}
	// Attribute every accepted Cloud-model request without logging prompts,
	// credentials, custom headers or upstream URLs.
	s.log.Info("device cloud model proxy",
		"user", p.deviceUserID,
		"device", p.deviceID,
		"model", modelID,
		"scope", authorized.Scope,
		"scope_id", authorized.ScopeID)
	s.proxyResolvedModel(w, r, resolved, "device", p.deviceID)
}
