package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// --- decode shapes for the project-owned provider/model views ----------------

type projProviderModelViewT struct {
	ID               string `json:"id"`
	ProviderID       string `json:"provider_id"`
	Name             string `json:"name"`
	ModelID          string `json:"model_id"`
	RuntimeModelName string `json:"runtime_model_name"`
	ContextWindow    int    `json:"context_window"`
	Source           string `json:"source"`
	Enabled          bool   `json:"enabled"`
}

type projProviderViewT struct {
	ID          string                   `json:"id"`
	ProjectID   string                   `json:"project_id"`
	Name        string                   `json:"name"`
	Kind        string                   `json:"kind"`
	BaseURL     string                   `json:"base_url"`
	AuthType    string                   `json:"auth_type"`
	APIKeySet   bool                     `json:"api_key_set"`
	HeadersSet  bool                     `json:"headers_set"`
	CatalogMode string                   `json:"catalog_mode"`
	Models      []projProviderModelViewT `json:"models"`
}

// mkProjectMember creates a plain (non-cluster-admin) user, a session, and a
// membership at the given role. The caller must have burned the first-user
// cluster-admin slot first (see the setup helpers).
func mkProjectMember(t *testing.T, st *store.MemStore, projectID, name string, role domain.Role) string {
	t.Helper()
	u := mkUser(t, st, name)
	tok := mkSession(t, st, u.ID)
	if err := st.UpsertMember(context.Background(), &domain.ProjectMember{
		ProjectID: projectID, UserID: u.ID, Role: role, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return tok
}

// projectRBACServer builds a catalog server with one project and owner/member/
// viewer session tokens for it. The first store user is a burned cluster-admin
// slot so the three principals are plain project members only.
func projectRBACServer(t *testing.T) (ts *httptest.Server, st *store.MemStore, proj projFixture, ownerTok, memberTok, viewerTok string) {
	t.Helper()
	ts, st = catalogServer(t, true)
	_ = mkUser(t, st, "seed-admin") // burn the first-user cluster-admin slot
	proj = createProject(t, ts)
	ownerTok = mkProjectMember(t, st, proj.ID, "owner", domain.RoleOwner)
	memberTok = mkProjectMember(t, st, proj.ID, "member", domain.RoleMember)
	viewerTok = mkProjectMember(t, st, proj.ID, "viewer", domain.RoleViewer)
	return ts, st, proj, ownerTok, memberTok, viewerTok
}

func createProjectProvider(t *testing.T, ts *httptest.Server, projectID, token string, body map[string]any) (projProviderViewT, int) {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/model-providers", token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var v projProviderViewT
		return v, resp.StatusCode
	}
	var v projProviderViewT
	decode(t, resp, &v)
	return v, resp.StatusCode
}

func addProjectModel(t *testing.T, ts *httptest.Server, projectID, providerID, token string, body map[string]any) (projProviderModelViewT, int) {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/model-providers/"+providerID+"/models", token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return projProviderModelViewT{}, resp.StatusCode
	}
	var v projProviderModelViewT
	decode(t, resp, &v)
	return v, resp.StatusCode
}

// Case 1: owner creates a project provider + model; GET returns it with the model
// nested; the plaintext api_key is NEVER echoed (only api_key_set).
func TestProjectProviderOwnerCreateAndList(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)

	const secret = "sk-project-secret"
	provider, status := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "Project OpenAI", "kind": "openai", "base_url": "https://models.internal/v1",
		"auth_type": "api_key", "api_key": secret, "catalog_mode": "disabled",
	})
	if status != http.StatusCreated {
		t.Fatalf("owner create provider: status=%d want 201", status)
	}
	if provider.ID == "" || provider.ProjectID != proj.ID || !provider.APIKeySet || provider.HeadersSet {
		t.Fatalf("provider view mismatch: %+v", provider)
	}

	model, status := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{
		"name": "O1", "model_id": "o1", "context_window": 200000,
		"capabilities": map[string]any{"reasoning": true, "tools": true, "image": false},
	})
	if status != http.StatusCreated {
		t.Fatalf("owner add model: status=%d want 201", status)
	}
	if model.ProviderID != provider.ID || model.RuntimeModelName != "openai/o1" || !model.Enabled || model.Source != "custom" {
		t.Fatalf("model view mismatch: %+v", model)
	}

	// GET as owner: provider present with its model; no plaintext key anywhere.
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", ownerTok, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), secret) {
		t.Fatalf("project provider list leaked the plaintext key: %s", raw)
	}
	if !strings.Contains(string(raw), `"api_key_set":true`) {
		t.Fatalf("project provider list omitted api_key_set: %s", raw)
	}
	var env struct {
		Providers []projProviderViewT `json:"providers"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(env.Providers) != 1 || len(env.Providers[0].Models) != 1 || env.Providers[0].Models[0].ID != model.ID {
		t.Fatalf("project provider envelope mismatch: %+v", env)
	}
}

// Case 2: a member may GET project providers but every mutation is 403.
func TestProjectProviderMemberIsReadOnly(t *testing.T) {
	ts, _, proj, ownerTok, memberTok, _ := projectRBACServer(t)
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})

	// Member can read.
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", memberTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member GET: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Member cannot create / update / delete.
	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/api/v1/projects/" + proj.ID + "/model-providers", map[string]any{"name": "Q", "kind": "openai", "base_url": "https://y.internal/v1", "auth_type": "none", "catalog_mode": "disabled"}},
		{http.MethodPatch, "/api/v1/projects/" + proj.ID + "/model-providers/" + provider.ID, map[string]any{"name": "renamed"}},
		{http.MethodDelete, "/api/v1/projects/" + proj.ID + "/model-providers/" + provider.ID, nil},
	} {
		resp := do(t, tc.method, ts.URL+tc.path, memberTok, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("member %s %s: status=%d want 403", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Case 3: a viewer is 403 on the project-providers list, but the existing member-
// facing GET /projects/{id}/models still works for a viewer.
func TestProjectProviderViewerForbiddenButModelsVisible(t *testing.T) {
	ts, _, proj, _, _, viewerTok := projectRBACServer(t)

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", viewerTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET providers: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/models", viewerTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET models: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// Case 4: a project-owned enabled model is in the project's usable set (GET
// /projects/{id}/models); disabling it removes it from both that endpoint and the
// provider view's enabled flag; re-enabling restores it.
func TestProjectOwnedModelEnabledToggle(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	model, _ := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "m1"})

	inProjectModels := func() bool {
		resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/models", ownerTok, nil)
		var pm projectModelsView
		decode(t, resp, &pm)
		for _, m := range pm.Models {
			if m.ID == model.ID {
				return true
			}
		}
		return false
	}

	if !inProjectModels() {
		t.Fatal("enabled project-owned model should appear in GET /projects/{id}/models")
	}

	// Disable → drops out of the usable set.
	resp := do(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID+"/models/"+model.ID, ownerTok, map[string]any{"enabled": false})
	var patched projProviderModelViewT
	decode(t, resp, &patched)
	if patched.Enabled {
		t.Fatalf("model should be disabled after patch: %+v", patched)
	}
	if inProjectModels() {
		t.Fatal("disabled project-owned model must not appear in GET /projects/{id}/models")
	}

	// Re-enable → restored.
	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID+"/models/"+model.ID, ownerTok, map[string]any{"enabled": true})
	resp.Body.Close()
	if !inProjectModels() {
		t.Fatal("re-enabled project-owned model should reappear in GET /projects/{id}/models")
	}
}

// Case 5: ownership isolation. Owner of project A gets a 404 patching/deleting
// project B's provider/model, and a 404 reaching a cluster-global (project_id
// NULL) provider through the project route.
func TestProjectProviderOwnershipIsolation(t *testing.T) {
	ts, st, projA, ownerATok, _, _ := projectRBACServer(t)

	// Project B with its own owner + provider/model.
	projB := createProject(t, ts)
	ownerBTok := mkProjectMember(t, st, projB.ID, "ownerB", domain.RoleOwner)
	provB, _ := createProjectProvider(t, ts, projB.ID, ownerBTok, map[string]any{
		"name": "PB", "kind": "openai", "base_url": "https://b.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	modB, _ := addProjectModel(t, ts, projB.ID, provB.ID, ownerBTok, map[string]any{"name": "MB", "model_id": "mb"})

	// A cluster-global provider (project_id NULL) via the system endpoint.
	clusterProv := createProvider(t, ts, map[string]any{
		"name": "Cluster", "kind": "openai", "base_url": "https://cluster.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})

	// Owner A cannot reach B's provider/model, or the cluster provider, via A's routes.
	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPatch, "/api/v1/projects/" + projA.ID + "/model-providers/" + provB.ID, map[string]any{"name": "hijack"}},
		{http.MethodDelete, "/api/v1/projects/" + projA.ID + "/model-providers/" + provB.ID, nil},
		{http.MethodPatch, "/api/v1/projects/" + projA.ID + "/model-providers/" + provB.ID + "/models/" + modB.ID, map[string]any{"name": "hijack"}},
		{http.MethodDelete, "/api/v1/projects/" + projA.ID + "/model-providers/" + provB.ID + "/models/" + modB.ID, nil},
		{http.MethodPatch, "/api/v1/projects/" + projA.ID + "/model-providers/" + clusterProv.ID, map[string]any{"name": "hijack"}},
		{http.MethodDelete, "/api/v1/projects/" + projA.ID + "/model-providers/" + clusterProv.ID, nil},
	} {
		resp := do(t, tc.method, ts.URL+tc.path, ownerATok, tc.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("owner A %s %s: status=%d want 404", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Owner A's own list never includes B's or the cluster provider.
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projA.ID+"/model-providers", ownerATok, nil)
	var env struct {
		Providers []projProviderViewT `json:"providers"`
	}
	decode(t, resp, &env)
	if len(env.Providers) != 0 {
		t.Fatalf("project A should own no providers yet, got %+v", env.Providers)
	}

	// The cross-project rows survived the failed attempts.
	if _, err := st.GetModelProvider(context.Background(), provB.ID); err != nil {
		t.Fatalf("project B provider should be untouched: %v", err)
	}
}

// Case 6: deleting a project provider cascades its models (subsequent GET empty).
func TestProjectProviderDeleteCascadesModels(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	_, _ = addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "m1"})

	resp := do(t, http.MethodDelete, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID, ownerTok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete provider: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", ownerTok, nil)
	var env struct {
		Providers []projProviderViewT `json:"providers"`
	}
	decode(t, resp, &env)
	if len(env.Providers) != 0 {
		t.Fatalf("provider delete should leave no providers/models: %+v", env.Providers)
	}
	// The project's usable model set is now empty too.
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/models", ownerTok, nil)
	var pm projectModelsView
	decode(t, resp, &pm)
	if len(pm.Models) != 0 {
		t.Fatalf("usable models should be empty after cascade: %+v", pm.Models)
	}
}

// Case 7: with exactly one project-owned model and no grants, a new run auto-
// selects it and stamps run.model_id/model_name.
func TestProjectOwnedModelDrivesRunSelection(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	model, _ := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "gpt-4o"})

	status, run, _ := createRunBody(t, ts, proj.ServiceID, map[string]any{"prompt": "hi"})
	if status != http.StatusCreated || run.ModelID == nil || *run.ModelID != model.ID {
		t.Fatalf("sole project-owned model: status=%d model_id=%v want %s", status, run.ModelID, model.ID)
	}
	if run.ModelName != "openai/gpt-4o" {
		t.Fatalf("run model_name=%q want openai/gpt-4o (audit snapshot)", run.ModelName)
	}
}

// Case 8: cluster grants coexist with project-owned models (union), and a non-
// empty usable set reports env_fallback=false even when the MODEL_* env is set.
func TestProjectOwnedUnionWithClusterGrant(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)

	// Project-owned model.
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	owned, _ := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "Owned", "model_id": "o1"})

	// Cluster model granted to the project.
	clusterModel := createModel(t, ts, "cluster-gpt", "https://cluster.internal/v1", "openai/gpt-4o", "")
	grant(t, ts, clusterModel.ID, proj.ID)

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/models", ownerTok, nil)
	var pm projectModelsView
	decode(t, resp, &pm)
	got := map[string]bool{}
	for _, m := range pm.Models {
		got[m.ID] = true
	}
	if len(pm.Models) != 2 || !got[owned.ID] || !got[clusterModel.ID] {
		t.Fatalf("union usable set=%+v want {owned, cluster}", pm.Models)
	}
	if pm.EnvFallback {
		t.Fatal("env_fallback must be false when the usable set is non-empty")
	}

	// A separate server WITH the MODEL_* env fallback: a project-owned model makes
	// CountModels()>0, which disables the env fallback signal.
	envTS, envSt, _ := newTestServer(t)
	_ = mkUser(t, envSt, "seed-admin")
	envProj := createProject(t, envTS)
	envOwner := mkProjectMember(t, envSt, envProj.ID, "owner", domain.RoleOwner)
	before := do(t, http.MethodGet, envTS.URL+"/api/v1/projects/"+envProj.ID+"/models", envOwner, nil)
	var pmBefore projectModelsView
	decode(t, before, &pmBefore)
	if !pmBefore.EnvFallback {
		t.Fatal("empty catalog + MODEL_* env should report env_fallback=true")
	}
	envProvider, st2 := createProjectProvider(t, envTS, envProj.ID, envOwner, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	if st2 != http.StatusCreated {
		t.Fatalf("env-server create provider: status=%d", st2)
	}
	_, _ = addProjectModel(t, envTS, envProj.ID, envProvider.ID, envOwner, map[string]any{"name": "M", "model_id": "m1"})
	after := do(t, http.MethodGet, envTS.URL+"/api/v1/projects/"+envProj.ID+"/models", envOwner, nil)
	var pmAfter projectModelsView
	decode(t, after, &pmAfter)
	if pmAfter.EnvFallback {
		t.Fatal("a project-owned model (CountModels>0) must disable the env fallback")
	}
}

// FIX A: a provider created WITH custom headers snapshots the (encrypted) header
// set onto each of its model rows; editing the provider's headers re-syncs the
// snapshot; the plaintext header value is NEVER echoed by any API response
// (only headers_set).
func TestProjectProviderHeadersSnapshotAndSync(t *testing.T) {
	ts, st, proj, ownerTok, _, _ := projectRBACServer(t)
	const headerVal = "super-secret-header-value"

	provider, status := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
		"headers": map[string]any{"X-Gateway-Token": headerVal},
	})
	if status != http.StatusCreated {
		t.Fatalf("create provider with headers: status=%d", status)
	}
	if !provider.HeadersSet {
		t.Fatalf("provider view should report headers_set=true: %+v", provider)
	}

	model, status := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "m1"})
	if status != http.StatusCreated {
		t.Fatalf("add model: status=%d", status)
	}

	ctx := context.Background()
	stored, err := st.GetModel(ctx, model.ID)
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if len(stored.HeadersEnc) == 0 {
		t.Fatal("model must snapshot the provider's encrypted headers (FIX A)")
	}
	if strings.Contains(string(stored.HeadersEnc), headerVal) {
		t.Fatal("model headers_enc must be ciphertext, not the plaintext value")
	}
	firstEnc := string(stored.HeadersEnc)

	// The plaintext header value is NEVER echoed by any API response.
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", ownerTok, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), headerVal) {
		t.Fatalf("project provider list leaked the plaintext header: %s", raw)
	}
	if !strings.Contains(string(raw), `"headers_set":true`) {
		t.Fatalf("project provider list omitted headers_set: %s", raw)
	}

	// Editing the provider's headers re-syncs the snapshot onto its model rows.
	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID, ownerTok, map[string]any{
		"headers": map[string]any{"X-Gateway-Token": "rotated-value"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch provider headers: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	stored2, err := st.GetModel(ctx, model.ID)
	if err != nil {
		t.Fatalf("get model after header edit: %v", err)
	}
	if len(stored2.HeadersEnc) == 0 || string(stored2.HeadersEnc) == firstEnc {
		t.Fatal("editing provider headers must re-sync the model's headers_enc snapshot")
	}

	// Clearing the headers (empty object) drops the snapshot from the model rows.
	resp = do(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID, ownerTok, map[string]any{
		"headers": map[string]any{},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch clear headers: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	stored3, err := st.GetModel(ctx, model.ID)
	if err != nil {
		t.Fatalf("get model after header clear: %v", err)
	}
	if len(stored3.HeadersEnc) != 0 {
		t.Fatalf("clearing provider headers must clear the model snapshot, got %d bytes", len(stored3.HeadersEnc))
	}
}

// FIX C: editing a model belonging to a service_identity provider (valid, keyless)
// must NOT silently downgrade the provider's auth_type to none.
func TestProjectProviderServiceIdentityPreservedOnModelEdit(t *testing.T) {
	ts, st, proj, ownerTok, _, _ := projectRBACServer(t)
	provider, status := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "SI", "kind": "openai", "base_url": "https://coding-plan.internal/v1",
		"auth_type": "service_identity", "catalog_mode": "disabled",
	})
	if status != http.StatusCreated || provider.AuthType != "service_identity" {
		t.Fatalf("create service_identity provider: status=%d auth=%q", status, provider.AuthType)
	}
	model, status := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "m1"})
	if status != http.StatusCreated {
		t.Fatalf("add model: status=%d", status)
	}

	// A metadata-only model edit (enabled toggle) recomputes the provider sync.
	resp := do(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers/"+provider.ID+"/models/"+model.ID, ownerTok, map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch model enabled: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	stored, err := st.GetModelProvider(context.Background(), provider.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if stored.AuthType != domain.ModelProviderAuthServiceIdentity {
		t.Fatalf("editing a model must NOT downgrade a service_identity provider (FIX C): got %q", stored.AuthType)
	}
}

// FIX E: the cluster grant endpoint refuses to grant a PROJECT-OWNED model (that
// would leak it cross-project); a cluster-global model still grants fine.
func TestGrantRejectsProjectOwnedModel(t *testing.T) {
	ts, _, proj, ownerTok, _, _ := projectRBACServer(t)
	provider, _ := createProjectProvider(t, ts, proj.ID, ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "none", "catalog_mode": "disabled",
	})
	owned, status := addProjectModel(t, ts, proj.ID, provider.ID, ownerTok, map[string]any{"name": "M", "model_id": "m1"})
	if status != http.StatusCreated {
		t.Fatalf("add model: status=%d", status)
	}

	projB := createProject(t, ts)

	// Cluster-admin tries to grant project A's private model to project B → 409.
	resp := do(t, http.MethodPut, ts.URL+"/api/v1/system/models/"+owned.ID+"/grants/"+projB.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("grant project-owned model: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "model_not_grantable" {
		t.Fatalf("error code=%q want model_not_grantable", body.Error.Code)
	}

	// A cluster-global model still grants fine.
	clusterModel := createModel(t, ts, "cluster-gpt", "https://cluster.internal/v1", "openai/gpt-4o", "")
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/system/models/"+clusterModel.ID+"/grants/"+projB.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant cluster model: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// A create with a key but no cipher is a fail-visible 409 (never store a key it
// cannot protect) — the project path shares encryptModelKey with the cluster path.
func TestProjectProviderKeyRequiresCipher(t *testing.T) {
	ts, st := catalogServer(t, false) // no AUTH_TOKEN_KEY
	_ = mkUser(t, st, "seed-admin")
	proj := createProject(t, ts)
	ownerTok := mkProjectMember(t, st, proj.ID, "owner", domain.RoleOwner)

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+proj.ID+"/model-providers", ownerTok, map[string]any{
		"name": "P", "kind": "openai", "base_url": "https://x.internal/v1",
		"auth_type": "api_key", "api_key": "sk-x", "catalog_mode": "disabled",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("key with no cipher: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "cipher_not_configured" {
		t.Fatalf("error code=%q want cipher_not_configured", body.Error.Code)
	}
}
