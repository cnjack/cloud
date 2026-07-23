package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
)

func TestDeviceCloudModelsCatalogAndProxy(t *testing.T) {
	var upstreamAuthorization string
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuthorization = r.Header.Get("Authorization")
		upstreamPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	fx := setupDevice(t)
	deviceToken, deviceID := fx.redeemFlow(t)
	dt, err := fx.st.GetDeviceTokenByHash(t.Context(), auth.HashToken(deviceToken))
	if err != nil {
		t.Fatal(err)
	}
	project := &domain.Project{
		ID: domain.NewID(), Name: "Desktop Cloud", OwnerUserID: dt.UserID, CreatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if err := fx.st.UpsertMember(t.Context(), &domain.ProjectMember{
		ProjectID: project.ID, UserID: dt.UserID, Role: domain.RoleOwner, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	provider := &domain.ModelProvider{
		ID: domain.NewID(), ProjectID: project.ID, Name: "ZhipuAI Coding Plan",
		Kind: "zhipuai", BaseURL: upstream.URL + "/v1",
		AuthType: domain.ModelProviderAuthNone, CatalogMode: domain.ModelProviderCatalogDisabled,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModelProvider(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	model := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, ProjectID: project.ID,
		Name: "GLM-5.2", ModelName: "glm-5.2", ModelID: "glm-5.2",
		BaseURL: provider.BaseURL, ContextWindow: 1_000_000,
		Capabilities: domain.ModelCapabilities{Reasoning: true, Tools: true, Image: true},
		Source:       "custom", Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModel(t.Context(), model); err != nil {
		t.Fatal(err)
	}
	otherProject := &domain.Project{
		ID: domain.NewID(), Name: "Hidden Project", OwnerUserID: "another-user", CreatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateProject(t.Context(), otherProject); err != nil {
		t.Fatal(err)
	}
	otherProvider := &domain.ModelProvider{
		ID: domain.NewID(), ProjectID: otherProject.ID, Name: "Hidden Provider",
		Kind: "openai", BaseURL: upstream.URL + "/v1",
		AuthType: domain.ModelProviderAuthNone, CatalogMode: domain.ModelProviderCatalogDisabled,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModelProvider(t.Context(), otherProvider); err != nil {
		t.Fatal(err)
	}
	hiddenModel := &domain.Model{
		ID: domain.NewID(), ProviderID: otherProvider.ID, ProjectID: otherProject.ID,
		Name: "Hidden Model", ModelName: "hidden-model", ModelID: "hidden-model",
		BaseURL: otherProvider.BaseURL, Source: "custom", Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModel(t.Context(), hiddenModel); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/cloud-models", deviceToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("catalog status=%d want 200", resp.StatusCode)
	}
	var catalog struct {
		Models []deviceCloudModelView `json:"models"`
	}
	decode(t, resp, &catalog)
	if len(catalog.Models) != 1 {
		t.Fatalf("catalog=%+v", catalog.Models)
	}
	got := catalog.Models[0]
	if got.Kind != "zhipuai" || got.ProviderName != provider.Name || got.Scope != "project" ||
		got.ScopeID != project.ID || got.UpstreamModelID != "glm-5.2" || !got.Capabilities.Tools {
		t.Fatalf("catalog model=%+v", got)
	}

	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/"+model.ID+"/llm/v1/chat/completions",
		deviceToken, map[string]any{"model": "glm-5.2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if upstreamAuthorization != "" {
		t.Fatalf("device token leaked upstream as Authorization=%q", upstreamAuthorization)
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q want /v1/chat/completions", upstreamPath)
	}

	// A valid device cannot proxy an unknown model and gets a typed 404.
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/missing/llm/v1/chat/completions",
		deviceToken, map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing proxy status=%d want 404 (device=%s)", resp.StatusCode, deviceID)
	}
	resp.Body.Close()

	// An existing model outside this account is indistinguishable from an
	// unknown id; the device cannot use the endpoint as a resource oracle.
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/"+hiddenModel.ID+"/llm/v1/chat/completions",
		deviceToken, map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden proxy status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
