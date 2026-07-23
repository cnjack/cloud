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

func TestDeviceCloudModelsAccountGrantInheritanceAndRevocation(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	fx := setupDevice(t)
	firstToken, _ := fx.redeemFlow(t)
	secondToken, _ := fx.redeemFlow(t) // future Desktop under the same Account
	firstPrincipal, err := fx.st.GetDeviceTokenByHash(t.Context(), auth.HashToken(firstToken))
	if err != nil {
		t.Fatal(err)
	}
	provider := &domain.ModelProvider{
		ID: domain.NewID(), Name: "Cluster Plan", Kind: "zhipuai-coding-plan",
		BaseURL: upstream.URL + "/v1", AuthType: domain.ModelProviderAuthNone,
		CatalogMode: domain.ModelProviderCatalogDisabled,
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModelProvider(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	model := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, Name: "GLM-5.2",
		ModelName: provider.Kind + "/glm-5.2", ModelID: "glm-5.2",
		BaseURL: provider.BaseURL, Source: "custom", CreatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateModel(t.Context(), model); err != nil {
		t.Fatal(err)
	}
	if err := fx.st.GrantModelToAccount(
		t.Context(), model.ID, firstPrincipal.UserID, firstPrincipal.UserID,
	); err != nil {
		t.Fatal(err)
	}

	// A browser/session credential cannot use the device catalog even when it
	// belongs to the granted Account; the Cloud key is only reachable through a
	// live device principal.
	sessionToken := mkSession(t, fx.st, firstPrincipal.UserID)
	resp := do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/cloud-models", sessionToken, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session catalog status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Both existing and newly authenticated Desktops inherit the Account grant.
	for _, token := range []string{firstToken, secondToken} {
		resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/cloud-models", token, nil)
		var catalog struct {
			Models []deviceCloudModelView `json:"models"`
		}
		decode(t, resp, &catalog)
		if len(catalog.Models) != 1 || catalog.Models[0].ModelID != model.ID ||
			catalog.Models[0].Scope != "account" || catalog.Models[0].ScopeID != firstPrincipal.UserID {
			t.Fatalf("account catalog=%+v", catalog.Models)
		}
	}

	// A different Account cannot enumerate or proxy the model.
	other := mkUser(t, fx.st, "other")
	otherDevice := &domain.Device{
		ID: domain.NewID(), UserID: other.ID, Name: "other-desktop", CreatedAt: time.Now().UTC(),
	}
	if err := fx.st.CreateDevice(t.Context(), otherDevice); err != nil {
		t.Fatal(err)
	}
	otherToken := auth.DeviceTokenPrefix + "other-token"
	if err := fx.st.CreateDeviceToken(t.Context(), &domain.DeviceToken{
		ID: domain.NewID(), DeviceID: otherDevice.ID, TokenHash: auth.HashToken(otherToken),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/cloud-models", otherToken, nil)
	var otherCatalog struct {
		Models []deviceCloudModelView `json:"models"`
	}
	decode(t, resp, &otherCatalog)
	if len(otherCatalog.Models) != 0 {
		t.Fatalf("other account saw models: %+v", otherCatalog.Models)
	}
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/"+model.ID+"/llm/v1/chat/completions",
		otherToken, map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other account proxy status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// The granted Account can proxy, then a revoke removes both catalog and
	// proxy access immediately for every Desktop.
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/"+model.ID+"/llm/v1/chat/completions",
		firstToken, map[string]any{})
	if resp.StatusCode != http.StatusOK || upstreamCalls != 1 {
		t.Fatalf("granted proxy status=%d calls=%d", resp.StatusCode, upstreamCalls)
	}
	resp.Body.Close()
	if err := fx.st.RevokeModelFromAccount(t.Context(), model.ID, firstPrincipal.UserID); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/cloud-models", secondToken, nil)
	var revokedCatalog struct {
		Models []deviceCloudModelView `json:"models"`
	}
	decode(t, resp, &revokedCatalog)
	if len(revokedCatalog.Models) != 0 {
		t.Fatalf("revoked account still saw models: %+v", revokedCatalog.Models)
	}
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/cloud-models/"+model.ID+"/llm/v1/chat/completions",
		secondToken, map[string]any{})
	if resp.StatusCode != http.StatusNotFound || upstreamCalls != 1 {
		t.Fatalf("revoked proxy status=%d calls=%d want 404/1", resp.StatusCode, upstreamCalls)
	}
	resp.Body.Close()
}
