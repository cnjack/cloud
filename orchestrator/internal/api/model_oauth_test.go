package api

import (
	"context"
	"encoding/json"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modeloauth"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPersonalOAuthOwnerAndPinnedEndpoint(t *testing.T) {
	ts, st := catalogServer(t, true)
	alice, bob := mkUser(t, st, "oauth-alice"), mkUser(t, st, "oauth-bob")
	owner, other := mkSession(t, st, alice.ID), mkSession(t, st, bob.ID)
	resp := do(t, "POST", ts.URL+"/api/v1/account/model-providers/chatgpt", owner, map[string]any{"name": "Personal ChatGPT"})
	if resp.StatusCode != 201 {
		t.Fatalf("create %d", resp.StatusCode)
	}
	var view modelProviderAdminView
	decode(t, resp, &view)
	if view.AuthType != domain.ModelProviderAuthOAuth || view.APIKeySet || view.Authorization.State != "requires_reauth" {
		t.Fatalf("initial OAuth view %+v", view)
	}
	stored, err := st.GetModelProvider(context.Background(), view.ID)
	if err != nil || stored.OwnerUserID != alice.ID || stored.BaseURL != modeloauth.BaseURL {
		t.Fatal("owner/endpoint not pinned")
	}
	base := ts.URL + "/api/v1/account/model-providers/" + view.ID
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, token := range []string{other, consoleToken} {
			resp = do(t, method, base+"/authorization", token, nil)
			want := 404
			if token == consoleToken {
				want = 403
			}
			if resp.StatusCode != want {
				t.Fatalf("unauthorized %s %d", method, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
	for _, field := range []string{"base_url", "api_key", "headers", "kind", "auth_type", "catalog_mode"} {
		var value any = "changed"
		if field == "headers" {
			value = map[string]string{"X-Test": "changed"}
		}
		resp = do(t, "PATCH", base, owner, map[string]any{field: value})
		if resp.StatusCode != 400 {
			t.Fatalf("mutable %s %d", field, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = do(t, "PATCH", base, owner, map[string]any{"name": "Renamed"})
	if resp.StatusCode != 200 {
		t.Fatalf("rename %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "GET", base+"/catalog", owner, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 409 || !strings.Contains(string(body), "model_reauthorization_required") {
		t.Fatalf("missing recovery %d %s", resp.StatusCode, body)
	}
	resp = do(t, "POST", base+"/models", owner, map[string]any{"name": "Fixture agent", "model_id": "fixture-agent", "capabilities": map[string]any{"tools": true}})
	if resp.StatusCode != 201 {
		t.Fatalf("create OAuth model %d", resp.StatusCode)
	}
	var model providerModelViewT
	decode(t, resp, &model)
	resp = do(t, "PATCH", base+"/models/"+model.ID, owner, map[string]any{"enabled": false})
	if resp.StatusCode != 200 {
		t.Fatalf("toggle OAuth model %d", resp.StatusCode)
	}
	resp.Body.Close()
	preserved, err := st.GetModelProvider(context.Background(), view.ID)
	if err != nil || preserved.AuthType != domain.ModelProviderAuthOAuth {
		t.Fatal("model edit downgraded OAuth authentication")
	}
	resp = do(t, "GET", base+"/authorization", owner, nil)
	var status modeloauth.Status
	decode(t, resp, &status)
	if status.State != "requires_reauth" {
		t.Fatal(status.State)
	}
}
func TestChatGPTCatalogReadsExplicitMetadata(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"fixture-agent","display_name":"Fixture","context_window":64000,"supported_reasoning_levels":[{"effort":"high"}],"supports_tool_calls":true,"input_modalities":["text","image"]}]}`))}
	models, err := decodeChatGPTCatalog(resp)
	if err != nil || len(models) != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(models)
	if models[0].ID != "fixture-agent" || !models[0].Capabilities.Reasoning || !models[0].Capabilities.Image || models[0].ContextWindow != 64000 {
		t.Fatalf("metadata %s", raw)
	}
}
