package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPersonalProviderOwnerIsolationAndEncryptedCredentials(t *testing.T) {
	ts, st := catalogServer(t, true)
	admin := mkUser(t, st, "provider-admin")
	alice, bob := mkUser(t, st, "personal-alice"), mkUser(t, st, "personal-bob")
	token, other := mkSession(t, st, alice.ID), mkSession(t, st, bob.ID)
	input := map[string]any{"name": "Personal OpenAI", "kind": "openai", "base_url": "https://models.example/v1", "auth_type": "api_key", "api_key": "personal-test-secret", "catalog_mode": "disabled"}
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers", token, input)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var provider modelProviderViewT
	decode(t, resp, &provider)
	stored, err := st.GetModelProvider(context.Background(), provider.ID)
	if err != nil || stored.OwnerUserID != alice.ID || len(stored.APIKeyEnc) == 0 || strings.Contains(string(stored.APIKeyEnc), "personal-test-secret") {
		t.Fatalf("owner/encryption invariant: err=%v", err)
	}
	// Same provider name in another account is independent.
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers", other, input)
	if resp.StatusCode != 201 {
		t.Fatalf("other create same name: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/account/model-providers", token, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "personal-test-secret") {
		t.Fatal("plaintext key in response")
	}
	for _, tok := range []string{other, mkSession(t, st, admin.ID)} {
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			resp = do(t, method, ts.URL+"/api/v1/account/model-providers/"+provider.ID, tok, map[string]any{"name": "stolen"})
			if resp.StatusCode != 404 {
				t.Fatalf("cross-account %s: %d", method, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		resp = do(t, method, ts.URL+"/api/v1/system/model-providers/"+provider.ID, consoleToken, map[string]any{"name": "cluster overwrite"})
		if resp.StatusCode != 404 {
			t.Fatalf("cluster %s: %d", method, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers/"+provider.ID+"/models", token, map[string]any{"name": "Personal model", "model_id": "agent", "capabilities": map[string]any{"tools": true}})
	if resp.StatusCode != 201 {
		t.Fatalf("model create: %d", resp.StatusCode)
	}
	var model providerModelViewT
	decode(t, resp, &model)
	own, _ := st.ListModelsForAccount(context.Background(), alice.ID)
	others, _ := st.ListModelsForAccount(context.Background(), bob.ID)
	if len(own) != 1 || own[0].ID != model.ID || len(others) != 0 {
		t.Fatal("account model entitlement leaked")
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		resp = do(t, method, ts.URL+"/api/v1/system/models/"+model.ID, consoleToken, map[string]any{"name": "cluster overwrite"})
		if resp.StatusCode != 404 {
			t.Fatalf("cluster model %s: %d", method, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/account/model-providers/"+provider.ID+"/models/"+model.ID, token, map[string]any{"enabled": false})
	if resp.StatusCode != 200 {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	resp.Body.Close()
	own, _ = st.ListModelsForAccount(context.Background(), alice.ID)
	if len(own) != 0 {
		t.Fatal("disabled personal model remains authorized")
	}
	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/account/model-providers/"+provider.ID, token, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPersonalProviderRejectsServicePrincipalAndServiceIdentity(t *testing.T) {
	ts, st := catalogServer(t, true)
	token := mkSession(t, st, mkUser(t, st, "personal-auth").ID)
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/account/model-providers", consoleToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("service principal: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers", token, map[string]any{"name": "invalid", "kind": "openai", "base_url": "https://models.example/v1", "auth_type": "service_identity", "catalog_mode": "auto"})
	if resp.StatusCode != 400 {
		t.Fatalf("service identity: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPersonalModelFlowsIntoAccountTaskAndRepositoryPicker(t *testing.T) {
	ts, _, token, _, _ := newAccountTaskServer(t)
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers", token, map[string]any{
		"name": "My model account", "kind": "openai", "base_url": "https://models.example/v1", "auth_type": "none", "catalog_mode": "disabled",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("provider: %d", resp.StatusCode)
	}
	var provider modelProviderViewT
	decode(t, resp, &provider)
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/model-providers/"+provider.ID+"/models", token, map[string]any{"name": "My agent", "model_id": "agent", "capabilities": map[string]any{"tools": true}})
	if resp.StatusCode != 201 {
		t.Fatalf("model: %d", resp.StatusCode)
	}
	var model providerModelViewT
	decode(t, resp, &model)
	input := map[string]any{"provider": "gitea", "provider_repo_id": "42", "prompt": "Inspect the repository", "model_id": model.ID}
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, input)
	if resp.StatusCode != 201 {
		t.Fatalf("task: %d", resp.StatusCode)
	}
	var task accountTaskResponse
	decode(t, resp, &task)
	if task.Run.ModelID == nil || *task.Run.ModelID != model.ID {
		t.Fatal("personal model selection lost")
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+task.Run.ProjectID+"/models", token, nil)
	var options projectModelsView
	decode(t, resp, &options)
	found := false
	for _, option := range options.Models {
		if option.ID == model.ID {
			found = true
			if option.ProviderID != provider.ID || option.ProviderName != "My model account" {
				t.Fatalf("provider identity lost: %+v", option)
			}
		}
	}
	if !found {
		t.Fatal("personal model missing from conversation retry picker")
	}
	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/account/model-providers/"+provider.ID+"/models/"+model.ID, token, map[string]any{"enabled": false})
	resp.Body.Close()
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, input)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled model dispatched: %d", resp.StatusCode)
	}
}
