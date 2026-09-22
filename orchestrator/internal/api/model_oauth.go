package api

import (
	"encoding/json"
	"fmt"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modeloauth"
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleCreateChatGPTProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderManager(w, r) {
		return
	}
	if s.cipher == nil {
		writeError(w, 409, "encryption_not_configured", "contact an administrator to configure model credential encryption")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid authorization request")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 120 {
		writeError(w, 400, "bad_request", "provide a connection name of at most 120 characters")
		return
	}
	p := &domain.ModelProvider{ID: domain.NewID(), OwnerUserID: principalFrom(r.Context()).userID(), Name: name, Kind: modeloauth.Kind, BaseURL: modeloauth.BaseURL, AuthType: domain.ModelProviderAuthOAuth, CatalogMode: domain.ModelProviderCatalogAuto, UpdatedBy: principalFrom(r.Context()).userID()}
	if err := s.st.CreateModelProvider(r.Context(), p); err != nil {
		writeError(w, 409, "provider_create_failed", "could not create connection; choose a unique name and retry")
		return
	}
	view, err := s.modelProviderView(r.Context(), *p)
	if err != nil {
		writeError(w, 500, "internal", "connection created but could not be loaded; refresh settings")
		return
	}
	s.models.Invalidate()
	writeJSON(w, 201, view)
}
func (s *Server) handleModelAuthorization(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadManagedProvider(w, r)
	if !ok {
		return
	}
	if p.AuthType != domain.ModelProviderAuthOAuth || p.Kind != modeloauth.Kind || p.OwnerUserID == "" {
		writeError(w, 409, "unsupported_auth", "this connection does not use ChatGPT authorization")
		return
	}
	var status modeloauth.Status
	var err error
	switch r.Method {
	case http.MethodDelete:
		err = s.modelOAuth.Cancel(r.Context(), p.ID)
	case http.MethodGet:
		status, err = s.modelOAuth.Status(r.Context(), p.ID)
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/poll") {
			status, err = s.modelOAuth.Poll(r.Context(), p.ID)
		} else {
			status, err = s.modelOAuth.Start(r.Context(), p.ID)
		}
	}
	s.models.Invalidate()
	if err != nil {
		writeError(w, 409, "model_authorization_failed", err.Error())
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, status)
}

// Codex catalogs use slug/display_name rather than OpenAI's data[].id.
// Capability flags come from the response, not a model-name heuristic.
func decodeChatGPTCatalog(resp *http.Response) ([]catalogModelView, error) {
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Slug       string            `json:"slug"`
			ID         string            `json:"id"`
			Name       string            `json:"display_name"`
			Context    int               `json:"context_window"`
			Reasoning  []json.RawMessage `json:"supported_reasoning_levels"`
			Tools      bool              `json:"supports_tool_calls"`
			Modalities []string          `json:"input_modalities"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]catalogModelView, 0, len(body.Models))
	seen := map[string]bool{}
	for _, m := range body.Models {
		id := strings.TrimSpace(m.Slug)
		if id == "" {
			id = strings.TrimSpace(m.ID)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		image := false
		for _, v := range m.Modalities {
			if v == "image" {
				image = true
			}
		}
		out = append(out, catalogModelView{ID: id, Name: m.Name, ContextWindow: m.Context, Capabilities: domain.ModelCapabilities{Reasoning: len(m.Reasoning) > 0, Tools: m.Tools, Image: image}, MetadataSource: "provider"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("provider returned an empty model catalog")
	}
	return out, nil
}
