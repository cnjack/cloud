package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type modelCapabilitiesViewT struct {
	Reasoning bool `json:"reasoning"`
	Tools     bool `json:"tools"`
	Image     bool `json:"image"`
}

type providerModelViewT struct {
	ID               string                 `json:"id"`
	ProviderID       string                 `json:"provider_id"`
	Name             string                 `json:"name"`
	ModelID          string                 `json:"model_id"`
	RuntimeModelName string                 `json:"runtime_model_name"`
	ContextWindow    int                    `json:"context_window"`
	Capabilities     modelCapabilitiesViewT `json:"capabilities"`
	Source           string                 `json:"source"`
}

type modelProviderViewT struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Kind          string               `json:"kind"`
	BaseURL       string               `json:"base_url"`
	AuthType      string               `json:"auth_type"`
	APIKeySet     bool                 `json:"api_key_set"`
	CatalogMode   string               `json:"catalog_mode"`
	Models        []providerModelViewT `json:"models"`
	ProjectGrants int                  `json:"project_grants"`
}

func createProvider(t *testing.T, ts *httptest.Server, body map[string]any) modelProviderViewT {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/model-providers", consoleToken, body)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create provider: status=%d body=%s", resp.StatusCode, raw)
	}
	var view modelProviderViewT
	decode(t, resp, &view)
	return view
}

func TestModelProviderCRUDCatalogAndCredentials(t *testing.T) {
	var catalogAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"glm-5.2"},{"id":"glm-4.5"}]}`)
	}))
	t.Cleanup(upstream.Close)

	ts, st := catalogServer(t, true)
	_ = mkUser(t, st, "admin")
	bob := mkUser(t, st, "bob")
	bobToken := mkSession(t, st, bob.ID)

	for _, path := range []string{
		"/api/v1/system/model-providers",
		"/api/v1/system/model-providers/missing/catalog",
	} {
		resp := do(t, http.MethodGet, ts.URL+path, bobToken, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("GET %s as non-admin: status=%d want 403", path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	const secret = "provider-super-secret"
	provider := createProvider(t, ts, map[string]any{
		"name": "Zhipu AI", "kind": "zhipu", "base_url": upstream.URL + "/v1",
		"auth_type": "api_key", "api_key": secret, "catalog_mode": "auto",
	})
	if provider.ID == "" || provider.Name != "Zhipu AI" || provider.Kind != "zhipu" || !provider.APIKeySet {
		t.Fatalf("provider view mismatch: %+v", provider)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/system/model-providers", consoleToken, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), secret) {
		t.Fatalf("provider list leaked plaintext credential: %s", raw)
	}
	if !strings.Contains(string(raw), `"api_key_set":true`) {
		t.Fatalf("provider list omitted credential state: %s", raw)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/system/model-providers/"+provider.ID+"/catalog", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("catalog: status=%d body=%s", resp.StatusCode, raw)
	}
	var catalog struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	decode(t, resp, &catalog)
	if len(catalog.Models) != 2 || catalog.Models[0].ID != "glm-5.2" {
		t.Fatalf("catalog mismatch: %+v", catalog.Models)
	}
	if catalogAuth != "Bearer "+secret {
		t.Fatalf("catalog authorization=%q want bearer credential", catalogAuth)
	}

	resp = do(t, http.MethodPost, ts.URL+"/api/v1/system/model-providers/"+provider.ID+"/verify", consoleToken, nil)
	var verified struct {
		Reachable        bool `json:"reachable"`
		CatalogAvailable bool `json:"catalog_available"`
	}
	decode(t, resp, &verified)
	if !verified.Reachable || !verified.CatalogAvailable {
		t.Fatalf("verify mismatch: %+v", verified)
	}

	resp = do(t, http.MethodPost, ts.URL+"/api/v1/system/model-providers/"+provider.ID+"/models", consoleToken, map[string]any{
		"name": "GLM 5.2", "model_id": "glm-5.2", "context_window": 200000,
		"capabilities": map[string]any{"reasoning": true, "tools": true, "image": false},
	})
	if resp.StatusCode != http.StatusCreated {
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create custom model: status=%d body=%s", resp.StatusCode, raw)
	}
	var model providerModelViewT
	decode(t, resp, &model)
	if model.ProviderID != provider.ID || model.ContextWindow != 200000 || !model.Capabilities.Reasoning || !model.Capabilities.Tools || model.Source != "custom" {
		t.Fatalf("custom model mismatch: %+v", model)
	}

	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/system/model-providers/"+provider.ID, consoleToken, map[string]any{
		"kind": "openai", "base_url": upstream.URL + "/compatible/v1",
	})
	if resp.StatusCode != http.StatusOK {
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update provider: status=%d body=%s", resp.StatusCode, raw)
	}
	resp.Body.Close()
	storedModel, err := st.GetModel(t.Context(), model.ID)
	if err != nil {
		t.Fatalf("get provider model after update: %v", err)
	}
	if storedModel.BaseURL != upstream.URL+"/compatible/v1" || storedModel.ModelName != "openai/glm-5.2" || !storedModel.APIKeySet() {
		t.Fatalf("provider update did not synchronize runtime model: %+v", storedModel)
	}

	project := createProject(t, ts)
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/system/models/"+model.ID+"/grants/"+project.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant provider model: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/system/model-providers/"+provider.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete provider: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/system/models", consoleToken, nil)
	var modelsEnvelope struct {
		Models []modelAdminViewT `json:"models"`
	}
	decode(t, resp, &modelsEnvelope)
	if len(modelsEnvelope.Models) != 0 {
		t.Fatalf("provider delete did not cascade models: %+v", modelsEnvelope.Models)
	}
}

func TestModelProviderCatalogUnavailableIsTyped(t *testing.T) {
	ts, st := catalogServer(t, true)
	_ = mkUser(t, st, "admin")
	provider := createProvider(t, ts, map[string]any{
		"name": "Coding Plan", "kind": "openai", "base_url": "https://coding-plan.internal/v1",
		"auth_type": "service_identity", "catalog_mode": "disabled",
	})
	if provider.APIKeySet || provider.AuthType != "service_identity" {
		t.Fatalf("service identity provider mismatch: %+v", provider)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/system/model-providers/"+provider.ID+"/catalog", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled catalog: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "catalog_unavailable" {
		t.Fatalf("disabled catalog code=%q want catalog_unavailable", body.Error.Code)
	}
}

func TestModelProviderValidation(t *testing.T) {
	ts, st := catalogServer(t, true)
	_ = mkUser(t, st, "admin")

	for name, body := range map[string]map[string]any{
		"bad auth":    {"name": "x", "base_url": "https://x.test/v1", "auth_type": "password", "catalog_mode": "auto"},
		"bad catalog": {"name": "x", "base_url": "https://x.test/v1", "auth_type": "none", "catalog_mode": "magic"},
		"bad url":     {"name": "x", "base_url": "ftp://x.test", "auth_type": "none", "catalog_mode": "auto"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/model-providers", consoleToken, body)
			if resp.StatusCode != http.StatusBadRequest {
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Fatalf("status=%d want 400 body=%s", resp.StatusCode, raw)
			}
			resp.Body.Close()
		})
	}
}

func TestModelProviderListEnvelopeShape(t *testing.T) {
	ts, st := catalogServer(t, true)
	_ = mkUser(t, st, "admin")
	_ = createProvider(t, ts, map[string]any{
		"name": "Keyless", "kind": "custom", "base_url": "https://models.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/system/model-providers", consoleToken, nil)
	var envelope struct {
		Providers []modelProviderViewT `json:"providers"`
	}
	decode(t, resp, &envelope)
	if len(envelope.Providers) != 1 || envelope.Providers[0].Models == nil {
		encoded, _ := json.Marshal(envelope)
		t.Fatalf("provider envelope mismatch: %s", encoded)
	}
}
