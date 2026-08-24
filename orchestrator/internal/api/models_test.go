package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// catalogServer builds an API server for the D21 model-catalog endpoints. When
// cipher is true JCLOUD_MASTER_KEY is set (needed to store a key). No MODEL_* env is
// set, so the catalog is the ONLY source (env fallback off).
func catalogServer(t *testing.T, cipher bool) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken}
	if cipher {
		cfg.MasterKey = validTokenKey(t)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	// Provider verify/catalog tests point at an httptest upstream on 127.0.0.1;
	// opt out of the SSRF dial guard so those loopback probes are exercised.
	srv.allowPrivateModelHosts = true
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	return ts, st
}

// modelAdminViewT decodes the admin catalog view.
type modelAdminViewT struct {
	ID                string                   `json:"id"`
	Name              string                   `json:"name"`
	BaseURL           string                   `json:"base_url"`
	ModelName         string                   `json:"model_name"`
	ContextWindow     int                      `json:"context_window"`
	Capabilities      domain.ModelCapabilities `json:"capabilities"`
	APIKeySet         bool                     `json:"api_key_set"`
	GrantedProjectIDs []string                 `json:"granted_project_ids"`
	GrantedAccountIDs []string                 `json:"granted_account_ids"`
}

// createModel POSTs a model as the console admin and returns its view.
func createModel(t *testing.T, ts *httptest.Server, name, base, model, key string) modelAdminViewT {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/system/models", consoleToken, map[string]any{
		"name": name, "base_url": base, "model_name": model, "api_key": key,
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create model %q: status=%d body=%s", name, resp.StatusCode, b)
	}
	var v modelAdminViewT
	decode(t, resp, &v)
	return v
}

func TestModelCatalogCRUDAndRBAC(t *testing.T) {
	ts, st := catalogServer(t, true)
	admin := mkUser(t, st, "admin") // first user => cluster admin
	_ = admin
	bob := mkUser(t, st, "bob")
	bobTok := mkSession(t, st, bob.ID)

	// A non-admin cannot list or create.
	for _, m := range []struct{ method, body string }{{"GET", ""}, {"POST", ""}} {
		var body any
		if m.method == "POST" {
			body = map[string]any{"name": "x", "base_url": "http://x/v1", "model_name": "a/b"}
		}
		resp := do(t, m.method, ts.URL+"/api/v1/system/models", bobTok, body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s as non-admin: status=%d want 403", m.method, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Admin create — never leaks the plaintext key.
	const secret = "sk-super-secret"
	v := createModel(t, ts, "gpt", "https://api.openai.com/v1", "openai/gpt-4o", secret)
	if v.ID == "" || v.Name != "gpt" || v.BaseURL != "https://api.openai.com/v1" || !v.APIKeySet {
		t.Fatalf("create view wrong: %+v", v)
	}

	// List — present, no plaintext key anywhere.
	resp := do(t, "GET", ts.URL+"/api/v1/system/models", consoleToken, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), secret) {
		t.Fatalf("list leaked the plaintext api key: %s", raw)
	}
	if !strings.Contains(string(raw), `"api_key_set":true`) {
		t.Fatalf("list should report api_key_set:true, got %s", raw)
	}

	// The legacy flat create also creates a scope-unique implicit provider.
	dup := do(t, "POST", ts.URL+"/api/v1/system/models", consoleToken, map[string]any{
		"name": "gpt", "base_url": "http://y/v1", "model_name": "c/d",
	})
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("dup name: status=%d want 409", dup.StatusCode)
	}
	dup.Body.Close()

	// PATCH mutable metadata (keeps the key — api_key omitted).
	resp = do(t, "PATCH", ts.URL+"/api/v1/system/models/"+v.ID, consoleToken, map[string]any{
		"name":           "gpt4o",
		"context_window": 128_000,
		"capabilities": map[string]any{
			"reasoning": true,
			"tools":     true,
			"image":     false,
		},
	})
	var patched modelAdminViewT
	decode(t, resp, &patched)
	if patched.Name != "gpt4o" || patched.ContextWindow != 128_000 ||
		!patched.Capabilities.Reasoning || !patched.Capabilities.Tools ||
		patched.Capabilities.Image || !patched.APIKeySet {
		t.Fatalf("patch view wrong: %+v", patched)
	}
	stored, err := st.GetModel(context.Background(), v.ID)
	if err != nil {
		t.Fatalf("get patched model: %v", err)
	}
	if stored.ContextWindow != 128_000 || !stored.Capabilities.Reasoning || !stored.Capabilities.Tools {
		t.Fatalf("patch was not persisted: %+v", stored)
	}

	// PATCH invalid context_window => 400.
	resp = do(t, "PATCH", ts.URL+"/api/v1/system/models/"+v.ID, consoleToken, map[string]any{"context_window": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch bad context_window: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// PATCH invalid base_url => 400.
	resp = do(t, "PATCH", ts.URL+"/api/v1/system/models/"+v.ID, consoleToken, map[string]any{"base_url": "ftp://x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch bad base_url: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE => 204; then list is empty.
	resp = do(t, "DELETE", ts.URL+"/api/v1/system/models/"+v.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "DELETE", ts.URL+"/api/v1/system/models/"+v.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestModelCipherGate: a key needs JCLOUD_MASTER_KEY; a keyless model saves fine.
func TestModelCipherGate(t *testing.T) {
	ts, _ := catalogServer(t, false) // no JCLOUD_MASTER_KEY

	resp := do(t, "POST", ts.URL+"/api/v1/system/models", consoleToken, map[string]any{
		"name": "gpt", "base_url": "http://x/v1", "model_name": "a/b", "api_key": "k",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("key with no cipher: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "cipher_not_configured" {
		t.Fatalf("error code=%q want cipher_not_configured", body.Error.Code)
	}

	// Keyless saves without a cipher.
	v := createModel(t, ts, "vllm", "http://vllm/v1", "local/llama", "")
	if v.APIKeySet {
		t.Fatalf("keyless model should have api_key_set=false: %+v", v)
	}
}

// TestModelGrantsAndProjectModels covers grant/revoke RBAC and the member-facing
// project models endpoint (which must never leak base_url/key).
func TestModelGrantsAndProjectModels(t *testing.T) {
	ts, st := catalogServer(t, true)
	admin := mkUser(t, st, "admin")
	_ = admin
	bob := mkUser(t, st, "bob")
	bobTok := mkSession(t, st, bob.ID)

	p := createProject(t, ts)
	m := createModel(t, ts, "gpt", "https://secret.internal/v1", "openai/gpt-4o", "sk-secret")

	// Non-admin cannot grant.
	resp := do(t, "PUT", ts.URL+"/api/v1/system/models/"+m.ID+"/grants/"+p.ID, bobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("grant as non-admin: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Grant to a missing project => 404.
	resp = do(t, "PUT", ts.URL+"/api/v1/system/models/"+m.ID+"/grants/nope", consoleToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("grant missing project: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Grant (idempotent) — the admin view echoes the granted project.
	resp = do(t, "PUT", ts.URL+"/api/v1/system/models/"+m.ID+"/grants/"+p.ID, consoleToken, nil)
	var gv modelAdminViewT
	decode(t, resp, &gv)
	if len(gv.GrantedProjectIDs) != 1 || gv.GrantedProjectIDs[0] != p.ID {
		t.Fatalf("grant view mismatch: %+v", gv)
	}

	// Member view: id/name/model_name ONLY — never base_url or key.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+p.ID+"/models", consoleToken, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, leak := range []string{"secret.internal", "sk-secret", "base_url", "api_key"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("project models leaked %q: %s", leak, raw)
		}
	}
	var pm projectModelsView
	if err := json.Unmarshal(raw, &pm); err != nil {
		t.Fatalf("decode project models: %v", err)
	}
	if len(pm.Models) != 1 || pm.Models[0].ID != m.ID || pm.Models[0].ModelName != "openai/gpt-4o" {
		t.Fatalf("project models mismatch: %+v", pm)
	}
	if pm.EnvFallback {
		t.Fatalf("env_fallback should be false with a populated catalog")
	}

	// Revoke => project no longer lists the model.
	resp = do(t, "DELETE", ts.URL+"/api/v1/system/models/"+m.ID+"/grants/"+p.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+p.ID+"/models", consoleToken, nil)
	decode(t, resp, &pm)
	if len(pm.Models) != 0 {
		t.Fatalf("after revoke project should list 0 models: %+v", pm.Models)
	}
}

// TestModelAccountGrants covers the direct Cluster-model → Account entitlement:
// it is cluster-admin-only, idempotent, rejects project-owned models, and echoes
// the durable account grant without exposing provider credentials.
func TestModelAccountGrants(t *testing.T) {
	ts, st := catalogServer(t, true)
	_ = mkUser(t, st, "admin") // first user => cluster admin
	bob := mkUser(t, st, "bob")
	bobTok := mkSession(t, st, bob.ID)
	model := createModel(t, ts, "gpt", "https://secret.internal/v1", "openai/gpt-4o", "sk-secret")

	// A normal account cannot grant itself a Cluster model.
	resp := do(t, http.MethodPut,
		ts.URL+"/api/v1/system/models/"+model.ID+"/account-grants/"+bob.ID, bobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("account grant as non-admin: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown accounts fail visibly instead of creating an orphan entitlement.
	resp = do(t, http.MethodPut,
		ts.URL+"/api/v1/system/models/"+model.ID+"/account-grants/missing", consoleToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing account grant: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Grant is idempotent and returned in the secret-free admin projection.
	for i := 0; i < 2; i++ {
		resp = do(t, http.MethodPut,
			ts.URL+"/api/v1/system/models/"+model.ID+"/account-grants/"+bob.ID, consoleToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("grant attempt %d: status=%d want 200", i+1, resp.StatusCode)
		}
		var granted modelAdminViewT
		decode(t, resp, &granted)
		if len(granted.GrantedAccountIDs) != 1 || granted.GrantedAccountIDs[0] != bob.ID {
			t.Fatalf("account grant view=%+v", granted)
		}
	}

	// An Account grant now covers both Desktop and Cloud Repository runs. The
	// hidden Project container does not require a second authorization.
	accountProject := &domain.Project{
		ID: domain.NewID(), Name: "account-only", OwnerUserID: bob.ID, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateProject(t.Context(), accountProject); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(t.Context(), &domain.ProjectMember{
		ProjectID: accountProject.ID, UserID: bob.ID, Role: domain.RoleOwner, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+accountProject.ID+"/models", bobTok, nil)
	var accountProjectModels projectModelsView
	decode(t, resp, &accountProjectModels)
	if len(accountProjectModels.Models) != 1 || accountProjectModels.Models[0].ID != model.ID {
		t.Fatalf("account grant missing from Repository models: %+v", accountProjectModels.Models)
	}

	// A project-owned model cannot be promoted into an account-wide entitlement.
	project := &domain.Project{ID: domain.NewID(), Name: "private", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	provider := &domain.ModelProvider{
		ID: domain.NewID(), ProjectID: project.ID, Name: "private-provider", Kind: "openai",
		BaseURL: "https://private.invalid/v1", AuthType: domain.ModelProviderAuthNone,
		CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateModelProvider(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	privateModel := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, ProjectID: project.ID,
		Name: "private-model", ModelName: "openai/private", ModelID: "private",
		BaseURL: provider.BaseURL, Source: "custom", CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateModel(t.Context(), privateModel); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodPut,
		ts.URL+"/api/v1/system/models/"+privateModel.ID+"/account-grants/"+bob.ID, consoleToken, nil)
	var conflict errorBody
	decode(t, resp, &conflict)
	if resp.StatusCode != http.StatusConflict || conflict.Error.Code != "model_not_grantable" {
		t.Fatalf("private account grant: status=%d code=%q want 409/model_not_grantable",
			resp.StatusCode, conflict.Error.Code)
	}

	// Revoke is idempotent and immediately disappears from the admin view.
	for i := 0; i < 2; i++ {
		resp = do(t, http.MethodDelete,
			ts.URL+"/api/v1/system/models/"+model.ID+"/account-grants/"+bob.ID, consoleToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("revoke attempt %d: status=%d want 200", i+1, resp.StatusCode)
		}
		var revoked modelAdminViewT
		decode(t, resp, &revoked)
		if len(revoked.GrantedAccountIDs) != 0 {
			t.Fatalf("account grant survived revoke: %+v", revoked)
		}
	}
}

// grant is a helper that grants a model to a project as admin.
func grant(t *testing.T, ts *httptest.Server, modelID, projectID string) {
	t.Helper()
	resp := do(t, "PUT", ts.URL+"/api/v1/system/models/"+modelID+"/grants/"+projectID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant %s->%s: status=%d", modelID, projectID, resp.StatusCode)
	}
	resp.Body.Close()
}

// runModelView decodes a run's model_id + model_name (D21 audit fields).
type runModelView struct {
	ID          string  `json:"id"`
	ModelID     *string `json:"model_id"`
	ModelName   string  `json:"model_name"`
	ModelEffort string  `json:"model_effort"`
}

// createRunBody POSTs a run and returns (status, decoded run, error body).
func createRunBody(t *testing.T, ts *httptest.Server, serviceID string, body map[string]any) (int, runModelView, errorBody) {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/repositories/"+serviceID+"/runs", consoleToken, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var run runModelView
	var eb errorBody
	_ = json.Unmarshal(raw, &run)
	_ = json.Unmarshal(raw, &eb)
	return resp.StatusCode, run, eb
}

// TestRunModelSelectionChain drives the D21 resolution chain end-to-end through
// run creation: composer pick (granted/ungranted), sole grant, and multi-grant.
func TestRunModelSelectionChain(t *testing.T) {
	ts, _ := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "a", "http://a/v1", "p/a", "")
	b := createModel(t, ts, "b", "http://b/v1", "p/b", "")

	// (1) Sole grant is auto-selected (no pick, no default). model_name snapshotted.
	grant(t, ts, a.ID, p.ID)
	status, run, _ := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	if status != http.StatusCreated || run.ModelID == nil || *run.ModelID != a.ID {
		t.Fatalf("sole grant: status=%d model_id=%v want %s", status, run.ModelID, a.ID)
	}
	if run.ModelName != "p/a" {
		t.Fatalf("sole grant model_name=%q want p/a (audit snapshot)", run.ModelName)
	}

	// (2) Grant a second model → ambiguous, no default → 409 model_not_selected.
	grant(t, ts, b.ID, p.ID)
	status, _, eb := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	if status != http.StatusConflict || eb.Error.Code != "model_not_selected" {
		t.Fatalf("ambiguous: status=%d code=%q want 409/model_not_selected", status, eb.Error.Code)
	}

	// (3) Composer picks a granted model → OK, stamped.
	status, run, _ = createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi", "model_id": b.ID})
	if status != http.StatusCreated || run.ModelID == nil || *run.ModelID != b.ID {
		t.Fatalf("pick granted: status=%d model_id=%v want %s", status, run.ModelID, b.ID)
	}

	// (4) Composer picks a model NOT granted to the project → 403 model_not_granted.
	other := createModel(t, ts, "c", "http://c/v1", "p/c", "")
	status, _, eb = createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi", "model_id": other.ID})
	if status != http.StatusForbidden || eb.Error.Code != "model_not_granted" {
		t.Fatalf("pick ungranted: status=%d code=%q want 403/model_not_granted", status, eb.Error.Code)
	}
}

func TestRunModelEffortRequiresDeclaredReasoningCapability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reasoning  bool
		wantStatus int
		wantCode   string
	}{
		{name: "unsupported", reasoning: false, wantStatus: http.StatusBadRequest, wantCode: "model_effort_unsupported"},
		{name: "supported", reasoning: true, wantStatus: http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, st := catalogServer(t, true)
			p := createProject(t, ts)
			view := createModel(t, ts, "reasoning-"+tc.name, "http://model/v1", "openai/reasoner", "")
			model, err := st.GetModel(context.Background(), view.ID)
			if err != nil {
				t.Fatal(err)
			}
			model.Capabilities.Reasoning = tc.reasoning
			if err := st.UpdateModel(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			grant(t, ts, view.ID, p.ID)

			status, run, body := createRunBody(t, ts, p.ServiceID, map[string]any{
				"prompt": "reason carefully", "model_id": view.ID, "model_effort": "high",
			})
			if status != tc.wantStatus {
				t.Fatalf("status=%d want %d (body=%+v)", status, tc.wantStatus, body)
			}
			if tc.wantCode != "" && body.Error.Code != tc.wantCode {
				t.Fatalf("error code=%q want %q", body.Error.Code, tc.wantCode)
			}
			if tc.reasoning && run.ModelEffort != "high" {
				t.Fatalf("model_effort=%q want high", run.ModelEffort)
			}
		})
	}
}

func TestEnvironmentFallbackRejectsModelEffort(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	p := createProject(t, ts)

	status, _, body := createRunBody(t, ts, p.ServiceID, map[string]any{
		"prompt": "reason carefully", "model_effort": "medium",
	})
	if status != http.StatusConflict || body.Error.Code != "model_effort_unsupported" {
		t.Fatalf("status=%d code=%q want 409 model_effort_unsupported", status, body.Error.Code)
	}
}

func TestRetryRevalidatesPreservedModelEffort(t *testing.T) {
	ts, st := catalogServer(t, true)
	p := createProject(t, ts)
	view := createModel(t, ts, "retry-reasoner", "http://model/v1", "openai/reasoner", "")
	model, _ := st.GetModel(context.Background(), view.ID)
	model.Capabilities.Reasoning = true
	if err := st.UpdateModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	grant(t, ts, view.ID, p.ID)
	status, run, body := createRunBody(t, ts, p.ServiceID, map[string]any{
		"prompt": "reason", "model_id": view.ID, "model_effort": "high",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, body)
	}
	ctx := context.Background()
	original, _ := st.GetRun(ctx, run.ID)
	_, _ = st.ScheduleRun(ctx, original.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, original.ID, "Running", original.CreatedAt)
	_, _ = st.MarkSucceeded(ctx, original.ID, "Succeeded", original.CreatedAt)

	model.Capabilities.Reasoning = false
	if err := st.UpdateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	var errBody errorBody
	decode(t, resp, &errBody)
	if resp.StatusCode != http.StatusBadRequest || errBody.Error.Code != "model_effort_unsupported" {
		t.Fatalf("retry status=%d code=%q want 400/model_effort_unsupported", resp.StatusCode, errBody.Error.Code)
	}
}

func TestResumeRevalidatesPreservedModelEffort(t *testing.T) {
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t), PersistentWorkspace: true}
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	p := createProject(t, ts)
	view := createModel(t, ts, "resume-reasoner", "http://model/v1", "openai/reasoner", "")
	model, _ := st.GetModel(context.Background(), view.ID)
	model.Capabilities.Reasoning = true
	if err := st.UpdateModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	grant(t, ts, view.ID, p.ID)
	status, run, body := createRunBody(t, ts, p.ServiceID, map[string]any{
		"prompt": "reason", "model_id": view.ID, "model_effort": "medium", "session": true,
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, body)
	}
	ctx := context.Background()
	original, _ := st.GetRun(ctx, run.ID)
	_, _ = st.ScheduleRun(ctx, original.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, original.ID, "Running", original.CreatedAt)
	_, _ = st.SetRunACPSession(ctx, original.ID, "acp-model-effort")
	_, _ = st.MarkSucceeded(ctx, original.ID, "Succeeded", original.CreatedAt)

	model.Capabilities.Reasoning = false
	if err := st.UpdateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+run.ID+"/resume", consoleToken, map[string]string{"prompt": "continue"})
	var errBody errorBody
	decode(t, resp, &errBody)
	if resp.StatusCode != http.StatusBadRequest || errBody.Error.Code != "model_effort_unsupported" {
		t.Fatalf("resume status=%d code=%q want 400/model_effort_unsupported", resp.StatusCode, errBody.Error.Code)
	}
}

// TestServiceDefaultModel covers PATCH default_model_id (grant-validated) and its
// effect on the resolution chain.
func TestServiceDefaultModel(t *testing.T) {
	ts, _ := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "a", "http://a/v1", "p/a", "")
	b := createModel(t, ts, "b", "http://b/v1", "p/b", "")
	grant(t, ts, a.ID, p.ID)
	grant(t, ts, b.ID, p.ID)

	// Set default to a non-granted model => 400 model_not_granted.
	other := createModel(t, ts, "c", "http://c/v1", "p/c", "")
	resp := do(t, "PATCH", ts.URL+"/api/v1/repositories/"+p.ServiceID, consoleToken, map[string]any{"default_model_id": other.ID})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("default to ungranted: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Set default to a granted model => 200; it disambiguates the chain.
	resp = do(t, "PATCH", ts.URL+"/api/v1/repositories/"+p.ServiceID, consoleToken, map[string]any{"default_model_id": a.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set default: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	status, run, _ := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	if status != http.StatusCreated || run.ModelID == nil || *run.ModelID != a.ID {
		t.Fatalf("service default: status=%d model_id=%v want %s", status, run.ModelID, a.ID)
	}

	// Clear the default (explicit "") => back to ambiguous (two grants).
	resp = do(t, "PATCH", ts.URL+"/api/v1/repositories/"+p.ServiceID, consoleToken, map[string]any{"default_model_id": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear default: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	status, _, eb := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	if status != http.StatusConflict || eb.Error.Code != "model_not_selected" {
		t.Fatalf("after clear: status=%d code=%q want 409/model_not_selected", status, eb.Error.Code)
	}
}

// TestProjectDefaultModel covers the owner-editable Project fallback: it must
// accept only usable models, disambiguate headless runs, and be clearable.
func TestProjectDefaultModel(t *testing.T) {
	ts, _ := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "project-default-a", "http://a/v1", "p/a", "")
	b := createModel(t, ts, "project-default-b", "http://b/v1", "p/b", "")
	grant(t, ts, a.ID, p.ID)
	grant(t, ts, b.ID, p.ID)

	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+p.ID, consoleToken, map[string]any{"default_model_id": b.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set project default: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	status, run, _ := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "project default"})
	if status != http.StatusCreated || run.ModelID == nil || *run.ModelID != b.ID {
		t.Fatalf("project default: status=%d model_id=%v want %s", status, run.ModelID, b.ID)
	}

	ungranted := createModel(t, ts, "project-default-ungranted", "http://c/v1", "p/c", "")
	resp = do(t, "PATCH", ts.URL+"/api/v1/projects/"+p.ID, consoleToken, map[string]any{"default_model_id": ungranted.ID})
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusForbidden || eb.Error.Code != "model_not_granted" {
		t.Fatalf("ungranted project default: status=%d code=%q want 403/model_not_granted", resp.StatusCode, eb.Error.Code)
	}

	resp = do(t, "PATCH", ts.URL+"/api/v1/projects/"+p.ID, consoleToken, map[string]any{"default_model_id": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear project default: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	status, _, eb = createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "ambiguous again"})
	if status != http.StatusConflict || eb.Error.Code != "model_not_selected" {
		t.Fatalf("after clear: status=%d code=%q want 409/model_not_selected", status, eb.Error.Code)
	}
}

// TestRunModelNameSurvivesModelDeletion is the C1-companion audit: after a run's
// model is deleted, its model_id is FK-nulled but the model_name snapshot stays,
// so the run is still traceable to what it ran on.
func TestRunModelNameSurvivesModelDeletion(t *testing.T) {
	ts, _ := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "a", "http://a/v1", "p/a", "")
	grant(t, ts, a.ID, p.ID)
	_, run, _ := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	if run.ModelID == nil || run.ModelName != "p/a" {
		t.Fatalf("pre-delete run model=%v/%q want a/p-a", run.ModelID, run.ModelName)
	}

	// Delete the model → grants cascade, runs.model_id nulled, model_name kept.
	resp := do(t, "DELETE", ts.URL+"/api/v1/system/models/"+a.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete model: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID, consoleToken, nil)
	var after runModelView
	decode(t, resp, &after)
	if after.ModelID != nil {
		t.Fatalf("model_id should be nulled after model delete, got %v", after.ModelID)
	}
	if after.ModelName != "p/a" {
		t.Fatalf("model_name snapshot should survive deletion, got %q", after.ModelName)
	}
}

// TestRetryModelNotGrantedUsesReuseMessage is P2: a retry whose original model is
// no longer granted returns 403 model_not_granted with the RETRY-scoped wording
// ("the model this run used…"), not the composer-pick wording.
func TestRetryModelNotGrantedUsesReuseMessage(t *testing.T) {
	ts, st := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "a", "http://a/v1", "p/a", "")
	grant(t, ts, a.ID, p.ID)
	_, run, _ := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "hi"})
	// Drive to a terminal state so retry is allowed.
	ctx := context.Background()
	got, _ := st.GetRun(ctx, run.ID)
	_, _ = st.ScheduleRun(ctx, got.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, got.ID, "Running", got.CreatedAt)
	_, _ = st.MarkSucceeded(ctx, got.ID, "Succeeded", got.CreatedAt)

	// Revoke the model's grant, then retry → 403 with the reuse-scoped message.
	resp := do(t, "DELETE", ts.URL+"/api/v1/system/models/"+a.ID+"/grants/"+p.ID, consoleToken, nil)
	resp.Body.Close()
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusForbidden || eb.Error.Code != "model_not_granted" {
		t.Fatalf("retry: status=%d code=%q want 403/model_not_granted", resp.StatusCode, eb.Error.Code)
	}
	if !strings.Contains(eb.Error.Message, "this run used") {
		t.Fatalf("retry not-granted message should be reuse-scoped, got %q", eb.Error.Message)
	}
}

func TestRetryWithExplicitModelOverridePreservesFrozenContract(t *testing.T) {
	ts, st := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "retry-a", "http://a/v1", "provider/model-a", "")
	b := createModel(t, ts, "retry-b", "http://b/v1", "provider/model-b", "")
	grant(t, ts, a.ID, p.ID)
	grant(t, ts, b.ID, p.ID)

	status, created, body := createRunBody(t, ts, p.ServiceID, map[string]any{
		"prompt": "review this", "model_id": a.ID,
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, body)
	}
	ctx := context.Background()
	original, err := st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.ScheduleRun(ctx, original.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, original.ID, "Running", original.CreatedAt)
	_, _ = st.MarkFailed(ctx, original.ID, "Failed", domain.FailureModelRateLimited, "rate limited", time.Now())
	original, _ = st.GetRun(ctx, original.ID)
	if original.ExecutionContract == nil {
		t.Fatal("original workflow contract was not resolved")
	}
	originalContractJSON, _ := json.Marshal(original.ExecutionContract)

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+original.ID+"/retry", consoleToken,
		map[string]any{"model_id": b.ID})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("retry status=%d body=%s", resp.StatusCode, raw)
	}
	var retry domain.Run
	decode(t, resp, &retry)
	if retry.ModelID == nil || *retry.ModelID != b.ID || retry.ModelName != "provider/model-b" {
		t.Fatalf("retry model=%v/%q want %s/provider/model-b", retry.ModelID, retry.ModelName, b.ID)
	}
	if retry.RetriedFrom == nil || *retry.RetriedFrom != original.ID || retry.Attempt != original.Attempt+1 {
		t.Fatalf("retry lineage=%v attempt=%d", retry.RetriedFrom, retry.Attempt)
	}
	if retry.ExecutionContract == nil {
		t.Fatal("retry workflow contract missing")
	}
	selection := retry.ExecutionContract.Execution.LLMSelection
	if selection.ModelID != b.ID || selection.ModelName != "provider/model-b" || selection.Source != domain.WorkflowLLMSourceRetryOverride {
		t.Fatalf("retry contract selection=%+v", selection)
	}
	if retry.ExecutionContract.Hash == original.ExecutionContract.Hash {
		t.Fatal("model override reused the original contract hash")
	}
	if retry.ExecutionContract.Trigger != original.ExecutionContract.Trigger ||
		retry.ExecutionContract.Workflow != original.ExecutionContract.Workflow ||
		retry.ExecutionContract.Profile != original.ExecutionContract.Profile {
		t.Fatal("model override changed frozen workflow identity")
	}
	storedOriginal, _ := st.GetRun(ctx, original.ID)
	storedOriginalJSON, _ := json.Marshal(storedOriginal.ExecutionContract)
	if string(storedOriginalJSON) != string(originalContractJSON) {
		t.Fatal("explicit retry mutated the original contract")
	}
}

func TestRetryModelOverrideValidationCreatesNoRun(t *testing.T) {
	ts, st := catalogServer(t, true)
	p := createProject(t, ts)
	a := createModel(t, ts, "retry-a", "http://a/v1", "provider/model-a", "")
	ungranted := createModel(t, ts, "retry-ungranted", "http://b/v1", "provider/model-b", "")
	grant(t, ts, a.ID, p.ID)
	status, created, body := createRunBody(t, ts, p.ServiceID, map[string]any{
		"prompt": "do it", "model_id": a.ID,
	})
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%+v", status, body)
	}
	ctx := context.Background()
	original, _ := st.GetRun(ctx, created.ID)
	_, _ = st.ScheduleRun(ctx, original.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, original.ID, "Running", original.CreatedAt)
	_, _ = st.MarkSucceeded(ctx, original.ID, "Succeeded", original.CreatedAt)
	before, _ := st.ListRuns(ctx, p.ID, 10)

	tests := []struct {
		name       string
		body       map[string]any
		statusCode int
		code       string
	}{
		{name: "same model", body: map[string]any{"model_id": a.ID}, statusCode: http.StatusBadRequest, code: "retry_model_unchanged"},
		{name: "empty model", body: map[string]any{"model_id": "  "}, statusCode: http.StatusBadRequest, code: "bad_request"},
		{name: "ungranted model", body: map[string]any{"model_id": ungranted.ID}, statusCode: http.StatusForbidden, code: "model_not_granted"},
		{name: "unknown field", body: map[string]any{"unexpected": true}, statusCode: http.StatusBadRequest, code: "bad_request"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+original.ID+"/retry", consoleToken, tc.body)
			var eb errorBody
			decode(t, resp, &eb)
			if resp.StatusCode != tc.statusCode || eb.Error.Code != tc.code {
				t.Fatalf("status=%d code=%q want %d/%s", resp.StatusCode, eb.Error.Code, tc.statusCode, tc.code)
			}
		})
	}
	after, _ := st.ListRuns(ctx, p.ID, 10)
	if len(after) != len(before) {
		t.Fatalf("invalid overrides created runs: before=%d after=%d", len(before), len(after))
	}
}

// --- run-creation gate (fail-visible, moved from the old system_model_test) ---

func TestCreateRun409WhenModelNotConfigured(t *testing.T) {
	// A server with NO model configured (no env, no catalog, no grants).
	ts, st := catalogServer(t, false)
	p := createProject(t, ts)
	status, _, eb := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "do it"})
	if status != http.StatusConflict || eb.Error.Code != "model_not_configured" {
		t.Fatalf("no model: status=%d code=%q want 409/model_not_configured", status, eb.Error.Code)
	}
	// No run should have been created.
	runs, _ := st.ListRuns(context.Background(), p.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (gate must not queue)", len(runs))
	}
}

func TestRetryAndReview409WhenModelNotConfigured(t *testing.T) {
	// Server starts with an env fallback (empty catalog) so we can create + drive a
	// run, then the env is cleared before retry/review.
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, GiteaURL: "http://gitea.test", GiteaToken: "pat"})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/repositories/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "do it"})
	var run struct {
		ID string `json:"id"`
	}
	decode(t, resp, &run)
	got, _ := st.GetRun(ctx, run.ID)
	_, _ = st.ScheduleRun(ctx, got.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, got.ID, "Running", got.CreatedAt)
	_, _ = st.MarkSucceeded(ctx, got.ID, "Succeeded", got.CreatedAt)
	_, _ = st.SetRunGit(ctx, got.ID, "jcode/run-x", "abc")
	_, _ = st.MarkPRCreated(ctx, got.ID, "http://gitea.test/o/r/pulls/1", 1)

	// Clear the env fallback (mirrors an admin removing the only source).
	cfg.ModelBaseURL, cfg.ModelName, cfg.ModelAPIKey = "", "", ""
	srv.models.Invalidate()

	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry with no model: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/review", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("review with no model: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "model_not_configured" {
		t.Fatalf("review error code=%q want model_not_configured", body.Error.Code)
	}
}

func TestRetryEnvironmentFallbackNeverSilentlySwitchesToCatalogModel(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	p := createProject(t, ts)

	status, created, body := createRunBody(t, ts, p.ServiceID, map[string]any{"prompt": "use env model"})
	if status != http.StatusCreated || created.ModelID != nil {
		t.Fatalf("create status=%d model=%v body=%+v", status, created.ModelID, body)
	}
	ctx := context.Background()
	original, _ := st.GetRun(ctx, created.ID)
	_, _ = st.ScheduleRun(ctx, original.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, original.ID, "Running", original.CreatedAt)
	_, _ = st.MarkFailed(ctx, original.ID, "Failed", domain.FailureModelRateLimited, "rate limited", time.Now())

	// Adding a catalog model disables the legacy environment fallback. Even
	// though this is now the project's sole granted model, a plain retry must
	// fail rather than silently changing providers/models.
	b := createModel(t, ts, "catalog-b", "http://b/v1", "provider/model-b", "")
	grant(t, ts, b.ID, p.ID)
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+original.ID+"/retry", consoleToken, nil)
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusConflict || eb.Error.Code != "model_not_configured" {
		t.Fatalf("same-model retry status=%d code=%q want 409/model_not_configured", resp.StatusCode, eb.Error.Code)
	}
	runs, _ := st.ListRuns(ctx, p.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("plain retry silently created a run: count=%d", len(runs))
	}

	// The user can still make the switch explicitly.
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/runs/"+original.ID+"/retry", consoleToken, map[string]any{"model_id": b.ID})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("explicit retry status=%d body=%s", resp.StatusCode, raw)
	}
	var retry domain.Run
	decode(t, resp, &retry)
	if retry.ModelID == nil || *retry.ModelID != b.ID {
		t.Fatalf("explicit retry model=%v want %s", retry.ModelID, b.ID)
	}
}

// TestEnvFallbackProjectModelsSignalsConfigured: with an empty catalog + MODEL_*
// env set, a project lists no granted models but env_fallback=true so the console
// ModelGate treats it as configured (local rig compatibility).
func TestEnvFallbackProjectModelsSignalsConfigured(t *testing.T) {
	ts, _, _ := newTestServer(t) // withTestModel => env fallback, empty catalog
	p := createProject(t, ts)
	resp := do(t, "GET", ts.URL+"/api/v1/projects/"+p.ID+"/models", consoleToken, nil)
	var pm projectModelsView
	decode(t, resp, &pm)
	if len(pm.Models) != 0 || !pm.EnvFallback {
		t.Fatalf("env fallback project models = %+v want empty + env_fallback:true", pm)
	}
}
