package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

// apiKeyFixture sets up two projects (A owned by "owner", B owned by
// "otherOwner"), each with a "default" service, plus a member/viewer/stranger
// on project A — the shape TestAPIKeyScopedPrincipalPermissionMatrix and
// friends need to exercise both "within scope" and "cross-project" behaviour.
// serviceB (in project B) backs the IDOR-by-id checks: a key scoped to A must
// be denied by a resource's real ProjectID, not by the URL shape.
type apiKeyFixture struct {
	ts          *httptest.Server
	projectA    string
	serviceA    string
	projectB    string
	serviceB    string
	ownerTok    string
	otherOwnTok string
	memberTok   string
	viewerTok   string
	strangerTok string
	adminTok    string
}

func setupAPIKeyFixture(t *testing.T) apiKeyFixture {
	t.Helper()
	ts, st, _ := newTestServer(t)

	admin := mkUser(t, st, "ak-admin") // first user => cluster admin
	owner := mkUser(t, st, "ak-owner")
	otherOwner := mkUser(t, st, "ak-other-owner")
	member := mkUser(t, st, "ak-member")
	viewer := mkUser(t, st, "ak-viewer")
	stranger := mkUser(t, st, "ak-stranger")

	adminTok := mkSession(t, st, admin.ID)
	ownerTok := mkSession(t, st, owner.ID)
	otherOwnTok := mkSession(t, st, otherOwner.ID)
	memberTok := mkSession(t, st, member.ID)
	viewerTok := mkSession(t, st, viewer.ID)
	strangerTok := mkSession(t, st, stranger.ID)

	// Project A: owner + a "default" raw-repo service + member/viewer.
	resp := do(t, "POST", ts.URL+"/api/v1/projects", ownerTok, map[string]any{"name": "proj-a"})
	var pvA projectView
	decode(t, resp, &pvA)

	svcResp := do(t, "POST", ts.URL+"/api/v1/projects/"+pvA.ID+"/services", ownerTok,
		map[string]any{"name": "default", "repo_url": "https://git/a.git"})
	var svcA domain.Service
	decode(t, svcResp, &svcA)

	for uid, role := range map[string]string{member.ID: "member", viewer.ID: "viewer"} {
		r := do(t, "POST", ts.URL+"/api/v1/projects/"+pvA.ID+"/members", ownerTok,
			map[string]any{"user_id": uid, "role": role})
		r.Body.Close()
	}

	// Project B: a completely separate project owned by someone else, with its
	// own service (the cross-tenant IDOR target).
	respB := do(t, "POST", ts.URL+"/api/v1/projects", otherOwnTok, map[string]any{"name": "proj-b"})
	var pvB projectView
	decode(t, respB, &pvB)

	svcRespB := do(t, "POST", ts.URL+"/api/v1/projects/"+pvB.ID+"/services", otherOwnTok,
		map[string]any{"name": "default", "repo_url": "https://git/b.git"})
	var svcB domain.Service
	decode(t, svcRespB, &svcB)

	return apiKeyFixture{
		ts:          ts,
		projectA:    pvA.ID,
		serviceA:    svcA.ID,
		projectB:    pvB.ID,
		serviceB:    svcB.ID,
		ownerTok:    ownerTok,
		otherOwnTok: otherOwnTok,
		memberTok:   memberTok,
		viewerTok:   viewerTok,
		strangerTok: strangerTok,
		adminTok:    adminTok,
	}
}

// createAPIKey is a small helper: owner mints a key on projectA and returns
// the decoded one-time response.
func createAPIKey(t *testing.T, f apiKeyFixture, name string) createAPIKeyResponse {
	t.Helper()
	resp := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", f.ownerTok,
		map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create api key: status=%d body=%s", resp.StatusCode, b)
	}
	var out createAPIKeyResponse
	decode(t, resp, &out)
	return out
}

// TestCreateAPIKeyReturnsPlaintextOnce: the create response carries the
// plaintext key exactly once; the raw body never contains key_hash, and a
// subsequent list never echoes the plaintext again.
func TestCreateAPIKeyReturnsPlaintextOnce(t *testing.T) {
	f := setupAPIKeyFixture(t)

	resp := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", f.ownerTok,
		map[string]any{"name": "ci-bot"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d want 201", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if strings.Contains(string(raw), "key_hash") {
		t.Fatalf("response leaks key_hash: %s", raw)
	}
	var out createAPIKeyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "ci-bot" || out.ProjectID != f.projectA || out.ID == "" {
		t.Fatalf("unexpected view: %+v", out)
	}
	if !strings.HasPrefix(out.Key, "jck_") {
		t.Fatalf("key=%q missing jck_ prefix", out.Key)
	}
	if out.Prefix == "" || !strings.HasPrefix(out.Key, out.Prefix) {
		t.Fatalf("prefix %q is not a prefix of key %q", out.Prefix, out.Key)
	}

	// The list view never carries the plaintext or the hash — only the safe
	// fields (F12 §5: "Responses never contain key_hash or plaintext").
	listResp := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", f.ownerTok, nil)
	listRaw, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if strings.Contains(string(listRaw), out.Key) {
		t.Fatalf("list response leaks the plaintext key: %s", listRaw)
	}
	if strings.Contains(string(listRaw), "key_hash") {
		t.Fatalf("list response leaks key_hash: %s", listRaw)
	}
	var lv struct {
		APIKeys []apiKeyView `json:"api_keys"`
	}
	if err := json.Unmarshal(listRaw, &lv); err != nil {
		t.Fatal(err)
	}
	if len(lv.APIKeys) != 1 || lv.APIKeys[0].ID != out.ID || lv.APIKeys[0].Prefix != out.Prefix {
		t.Fatalf("list = %+v want the one key just created", lv.APIKeys)
	}
}

// TestCreateAPIKeyRequiresName: an empty/blank name is a fail-visible 400, not
// a silently-unidentifiable key.
func TestCreateAPIKeyRequiresName(t *testing.T) {
	f := setupAPIKeyFixture(t)
	resp := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", f.ownerTok,
		map[string]any{"name": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank name: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestAPIKeyManagementOwnerRBAC: list/create/revoke are owner-only — a member,
// viewer, stranger and (crucially) the scoped key ITSELF may not call them; a
// cluster-admin and the owner may.
func TestAPIKeyManagementOwnerRBAC(t *testing.T) {
	f := setupAPIKeyFixture(t)
	created := createAPIKey(t, f, "seed")

	cases := []struct {
		name       string
		tok        string
		want       int // list / revoke (200 on success)
		wantCreate int // create (201 on success)
	}{
		{"owner", f.ownerTok, http.StatusOK, http.StatusCreated},
		{"admin", f.adminTok, http.StatusOK, http.StatusCreated},
		{"member", f.memberTok, http.StatusForbidden, http.StatusForbidden},
		{"viewer", f.viewerTok, http.StatusForbidden, http.StatusForbidden},
		{"stranger", f.strangerTok, http.StatusForbidden, http.StatusForbidden},
		{"scoped-key-itself", created.Key, http.StatusForbidden, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run("list/"+c.name, func(t *testing.T) {
			r := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", c.tok, nil)
			defer r.Body.Close()
			if r.StatusCode != c.want {
				t.Fatalf("status=%d want %d", r.StatusCode, c.want)
			}
		})
		t.Run("create/"+c.name, func(t *testing.T) {
			r := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", c.tok,
				map[string]any{"name": "x-" + c.name})
			defer r.Body.Close()
			if r.StatusCode != c.wantCreate {
				t.Fatalf("status=%d want %d", r.StatusCode, c.wantCreate)
			}
		})
		t.Run("revoke/"+c.name, func(t *testing.T) {
			// Mint a disposable key per attempt so a successful owner/admin revoke
			// in one subtest doesn't starve the next.
			disposable := createAPIKey(t, f, "disposable-"+c.name)
			r := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/"+disposable.ID, c.tok, nil)
			defer r.Body.Close()
			if r.StatusCode != c.want {
				t.Fatalf("status=%d want %d", r.StatusCode, c.want)
			}
		})
	}
}

// TestRevokeAPIKeyCrossProjectNotFound: an owner of project A cannot revoke a
// key that belongs to project B by guessing its id (404, not 403 — it does
// not confirm the id exists elsewhere).
func TestRevokeAPIKeyCrossProjectNotFound(t *testing.T) {
	f := setupAPIKeyFixture(t)
	bKeyResp := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectB+"/apikeys", f.otherOwnTok,
		map[string]any{"name": "b-key"})
	var bKey createAPIKeyResponse
	decode(t, bKeyResp, &bKey)

	r := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/"+bKey.ID, f.ownerTok, nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project revoke: status=%d want 404", r.StatusCode)
	}
}

// TestRevokeAPIKeyIdempotent: revoking twice is 200 both times (a retried
// DELETE is safe), and revoking an unknown id 404s.
func TestRevokeAPIKeyIdempotent(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "twice")

	r1 := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/"+k.ID, f.ownerTok, nil)
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first revoke: status=%d want 200", r1.StatusCode)
	}
	r1.Body.Close()
	r2 := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/"+k.ID, f.ownerTok, nil)
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("second revoke: status=%d want 200 (idempotent)", r2.StatusCode)
	}
	r2.Body.Close()

	r3 := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/does-not-exist", f.ownerTok, nil)
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke unknown id: status=%d want 404", r3.StatusCode)
	}
	r3.Body.Close()
}

// TestAPIKeyRevocationEffectiveImmediately: a key authenticates fine, then —
// the very next request after revocation — 401s. No propagation delay.
func TestAPIKeyRevocationEffectiveImmediately(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "revoke-me")

	before := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA, k.Key, nil)
	if before.StatusCode != http.StatusOK {
		t.Fatalf("before revoke: status=%d want 200", before.StatusCode)
	}
	before.Body.Close()

	rev := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys/"+k.ID, f.ownerTok, nil)
	rev.Body.Close()

	after := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA, k.Key, nil)
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after revoke: status=%d want 401", after.StatusCode)
	}
	after.Body.Close()
}

// TestAPIKeyLastUsedUpdated: an unused key shows no last_used_at; after one
// authenticated request it is stamped.
func TestAPIKeyLastUsedUpdated(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "watch-me")
	if k.LastUsedAt != nil {
		t.Fatalf("fresh key last_used_at=%v want nil", k.LastUsedAt)
	}

	use := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA, k.Key, nil)
	use.Body.Close()

	listResp := do(t, "GET", f.ts.URL+"/api/v1/projects/"+f.projectA+"/apikeys", f.ownerTok, nil)
	var lv struct {
		APIKeys []apiKeyView `json:"api_keys"`
	}
	decode(t, listResp, &lv)
	var found *apiKeyView
	for i := range lv.APIKeys {
		if lv.APIKeys[i].ID == k.ID {
			found = &lv.APIKeys[i]
		}
	}
	if found == nil {
		t.Fatal("created key not found in list")
	}
	if found.LastUsedAt == nil {
		t.Fatal("last_used_at should be stamped after use")
	}
}

// TestAPIKeyScopedPrincipalPermissionMatrix is the security core of F12: a
// scoped principal is capped at RoleMember on EXACTLY its own project.
func TestAPIKeyScopedPrincipalPermissionMatrix(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "matrix")
	tok := k.Key

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
		want   int
	}{
		{"own project view (member read)", "GET", "/api/v1/projects/" + f.projectA, nil, http.StatusOK},
		{"own project services (member read)", "GET", "/api/v1/projects/" + f.projectA + "/services", nil, http.StatusOK},
		{"create run in own project (member write)", "POST", "/api/v1/services/" + f.serviceA + "/runs",
			map[string]any{"prompt": "do it"}, http.StatusCreated},
		{"cross-project read", "GET", "/api/v1/projects/" + f.projectB, nil, http.StatusForbidden},
		{"cross-project services", "GET", "/api/v1/projects/" + f.projectB + "/services", nil, http.StatusForbidden},
		{"owner action on OWN project (rename)", "PATCH", "/api/v1/projects/" + f.projectA,
			map[string]any{"name": "renamed"}, http.StatusForbidden},
		{"owner action: manage members", "POST", "/api/v1/projects/" + f.projectA + "/members",
			map[string]any{"user_id": "someone", "role": "viewer"}, http.StatusForbidden},
		{"owner action: delete project", "DELETE", "/api/v1/projects/" + f.projectA, nil, http.StatusForbidden},
		{"cluster-admin surface: /system", "GET", "/api/v1/system", nil, http.StatusForbidden},
		{"self-manage api keys: list", "GET", "/api/v1/projects/" + f.projectA + "/apikeys", nil, http.StatusForbidden},
		{"self-manage api keys: create", "POST", "/api/v1/projects/" + f.projectA + "/apikeys",
			map[string]any{"name": "escalate"}, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, c.method, f.ts.URL+c.path, tok, c.body)
			defer r.Body.Close()
			if r.StatusCode != c.want {
				b, _ := io.ReadAll(r.Body)
				t.Fatalf("status=%d want %d body=%s", r.StatusCode, c.want, b)
			}
		})
	}
}

// TestAPIKeyListProjectsScopedToBoundProject: GET /projects for a scoped
// principal returns only the one project the key is bound to.
func TestAPIKeyListProjectsScopedToBoundProject(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "list-scope")

	resp := do(t, "GET", f.ts.URL+"/api/v1/projects", k.Key, nil)
	var l struct {
		Projects []projectView `json:"projects"`
	}
	decode(t, resp, &l)
	if len(l.Projects) != 1 || l.Projects[0].ID != f.projectA {
		t.Fatalf("projects = %+v want exactly [projectA]", l.Projects)
	}
	if l.Projects[0].Role != "member" {
		t.Fatalf("role = %q want member", l.Projects[0].Role)
	}
}

// TestAPIKeyServiceMemberActionsAllowed: within its own project, a scoped
// principal can do everything a Member can — retry/resume/messages/review are
// exercised at the RBAC-gate level (authorizeProject Member), sampled here via
// run creation + listing (the cheapest to set up end-to-end).
func TestAPIKeyServiceMemberActionsAllowed(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "member-actions")

	create := do(t, "POST", f.ts.URL+"/api/v1/services/"+f.serviceA+"/runs", k.Key, map[string]any{"prompt": "go"})
	if create.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(create.Body)
		t.Fatalf("create run: status=%d body=%s", create.StatusCode, b)
	}
	var run domain.Run
	decode(t, create, &run)
	if run.TriggeredByUserID != nil {
		t.Fatalf("api-key-triggered run triggered_by=%v want nil (no user identity)", *run.TriggeredByUserID)
	}

	// Member-visible reads: run detail, run list, events.
	getRun := do(t, "GET", f.ts.URL+"/api/v1/runs/"+run.ID, k.Key, nil)
	if getRun.StatusCode != http.StatusOK {
		t.Fatalf("get run: status=%d want 200", getRun.StatusCode)
	}
	getRun.Body.Close()

	listRuns := do(t, "GET", f.ts.URL+"/api/v1/runs", k.Key, nil)
	var lr struct {
		Runs []domain.Run `json:"runs"`
	}
	decode(t, listRuns, &lr)
	found := false
	for _, r := range lr.Runs {
		if r.ID == run.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("global run list for scoped key = %+v, missing the run just created", lr.Runs)
	}

	events := do(t, "GET", f.ts.URL+"/api/v1/runs/"+run.ID+"/events", k.Key, nil)
	if events.StatusCode != http.StatusOK {
		t.Fatalf("get events: status=%d want 200", events.StatusCode)
	}
	events.Body.Close()
}

// TestAPIKeyIDORByResourceID is the authorization-by-real-resource-ProjectID
// regression: a key scoped to project A, targeting project B's resources by
// their bare {id} in the URL, must be 403 — the gate authorizes on the
// resource's stored ProjectID (loaded from the store), never on the URL shape.
// This is the classic IDOR-by-id an attacker who knows/guesses a foreign id
// would attempt.
func TestAPIKeyIDORByResourceID(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "idor")

	// Seed a run in project B (as B's owner) so there is a concrete foreign
	// resource id to attack.
	createB := do(t, "POST", f.ts.URL+"/api/v1/services/"+f.serviceB+"/runs", f.otherOwnTok,
		map[string]any{"prompt": "b-task"})
	if createB.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(createB.Body)
		t.Fatalf("seed run in B: status=%d body=%s", createB.StatusCode, b)
	}
	var runB domain.Run
	decode(t, createB, &runB)

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"read B's run by id", "GET", "/api/v1/runs/" + runB.ID, nil},
		{"read B's run events by id", "GET", "/api/v1/runs/" + runB.ID + "/events", nil},
		{"read B's run pr by id", "GET", "/api/v1/runs/" + runB.ID + "/pr", nil},
		{"dispatch on B's service by id", "POST", "/api/v1/services/" + f.serviceB + "/runs",
			map[string]any{"prompt": "hijack"}},
		{"list B's service runs by id", "GET", "/api/v1/services/" + f.serviceB + "/runs", nil},
		{"list B's project runs by id", "GET", "/api/v1/projects/" + f.projectB + "/runs", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, c.method, f.ts.URL+c.path, k.Key, c.body)
			defer r.Body.Close()
			if r.StatusCode != http.StatusForbidden {
				b, _ := io.ReadAll(r.Body)
				t.Fatalf("status=%d want 403 body=%s", r.StatusCode, b)
			}
		})
	}
}

// TestAPIKeyForbiddenNonProjectSurfaces closes the three CONFIRMED gaps: the
// authed but non-project-scoped endpoints that must reject a scoped principal
// rather than silently serve cross-tenant data (provider repo enumeration via
// the cluster bot fallback, the deployment-wide user directory) — and GET /me,
// which must return 200 for a scoped principal instead of panicking on the nil
// user.
func TestAPIKeyForbiddenNonProjectSurfaces(t *testing.T) {
	f := setupAPIKeyFixture(t)
	k := createAPIKey(t, f, "surfaces")

	// F12-1: provider repo enumeration → 403 (would otherwise fall through to
	// the cluster GITEA_TOKEN bot and list every org repo).
	repos := do(t, "GET", f.ts.URL+"/api/v1/providers/gitea/repos", k.Key, nil)
	if repos.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(repos.Body)
		t.Fatalf("providers/repos: status=%d want 403 body=%s", repos.StatusCode, b)
	}
	repos.Body.Close()

	// F12-2: user directory → 403 (cross-project; would let a key enumerate all
	// users and spot cluster admins).
	users := do(t, "GET", f.ts.URL+"/api/v1/users?q=ak", k.Key, nil)
	if users.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(users.Body)
		t.Fatalf("users: status=%d want 403 body=%s", users.StatusCode, b)
	}
	users.Body.Close()

	// F12-3: /me must be a well-formed 200, NOT a panic→500. It names the
	// api_key kind + bound project + member role, and carries no fabricated
	// user (empty id, not cluster-admin, empty identities [] non-nil).
	meResp := do(t, "GET", f.ts.URL+"/api/v1/me", k.Key, nil)
	if meResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(meResp.Body)
		t.Fatalf("me: status=%d want 200 body=%s", meResp.StatusCode, b)
	}
	var me meResponse
	decode(t, meResp, &me)
	if me.Kind != "api_key" {
		t.Fatalf("me.kind=%q want api_key", me.Kind)
	}
	if me.ScopedProjectID != f.projectA {
		t.Fatalf("me.scoped_project_id=%q want %q", me.ScopedProjectID, f.projectA)
	}
	if me.Role != string(domain.RoleMember) {
		t.Fatalf("me.role=%q want member", me.Role)
	}
	if me.IsService {
		t.Fatal("me.is_service must be false for an api_key principal")
	}
	if me.User.ID != "" || me.User.IsClusterAdmin {
		t.Fatalf("me.user must be a non-fabricated stub, got %+v", me.User)
	}
	if me.Identities == nil {
		t.Fatal("me.identities must be [] (non-nil) for an api_key principal")
	}
}
