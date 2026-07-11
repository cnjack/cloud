package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeBoardValidator is a test stand-in for the jtype board fetch.
type fakeBoardValidator struct {
	board *jtype.Board
	err   error
}

func (f fakeBoardValidator) GetBoard(ctx context.Context, ws, ref string) (*jtype.Board, error) {
	return f.board, f.err
}

// kanbanFixture is a server with a cipher (AUTH_TOKEN_KEY) + one project/service
// seeded through the API, plus owner/member/viewer/stranger/admin bearer tokens
// so the project-scoped kanban RBAC (F6 / D25) can be exercised end to end.
type kanbanFixture struct {
	ts        *httptest.Server
	srv       *Server
	st        *store.MemStore
	projectID string
	serviceID string
	tokens    map[string]string
}

func setupKanban(t *testing.T, board fakeBoardValidator) kanbanFixture {
	t.Helper()
	st := store.NewMemStore()
	// A valid 32-byte AUTH_TOKEN_KEY so the cipher (per-link token seal) is live.
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, AuthTokenKey: key})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	// When a board (or error) is supplied, ENABLE the integration (env base URL, so
	// the resolver reports it on) and wire the fake as the board-validator seam so
	// column validation runs (mirrors an ON integration); otherwise validation is
	// off (mirrors an unconfigured cluster). The fake ignores the resolved factory.
	if board.board != nil || board.err != nil {
		cfg.JtypeBaseURL = "http://jtype.test"
		srv.boardValidatorFor = func(_ *jtype.Factory, _ string) boardValidator { return board }
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	admin := mkUser(t, st, "kadmin") // first user => cluster admin
	owner := mkUser(t, st, "kowner")
	member := mkUser(t, st, "kmember")
	viewer := mkUser(t, st, "kviewer")
	stranger := mkUser(t, st, "kstranger")
	tokens := map[string]string{
		"admin":    mkSession(t, st, admin.ID),
		"owner":    mkSession(t, st, owner.ID),
		"member":   mkSession(t, st, member.ID),
		"viewer":   mkSession(t, st, viewer.ID),
		"stranger": mkSession(t, st, stranger.ID),
		"service":  consoleToken,
	}

	// Owner creates the project (becomes owner) + a service.
	resp := do(t, "POST", ts.URL+"/api/v1/projects", tokens["owner"], map[string]any{"name": "kan"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pv.ID+"/services", tokens["owner"],
		map[string]any{"name": "default", "repo_url": "https://git/x.git"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create service: %d", resp.StatusCode)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	for uid, role := range map[string]string{member.ID: "member", viewer.ID: "viewer"} {
		r := do(t, "POST", ts.URL+"/api/v1/projects/"+pv.ID+"/members", tokens["owner"],
			map[string]any{"user_id": uid, "role": role})
		if r.StatusCode != http.StatusOK {
			t.Fatalf("add %s: %d", role, r.StatusCode)
		}
		r.Body.Close()
	}
	return kanbanFixture{ts: ts, srv: srv, st: st, projectID: pv.ID, serviceID: svc.ID, tokens: tokens}
}

func (f kanbanFixture) linksURL() string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/links"
}

// setClusterToken sets the env JTYPE_TOKEN fallback and invalidates the shared
// resolver so the effective cluster-token change is visible on the next request
// (the resolver caches for a few seconds; D27).
func (f kanbanFixture) setClusterToken(tok string) {
	f.srv.cfg.JtypeToken = tok
	f.srv.Kanban().Invalidate()
}

// Owner CRUD on the project-scoped links, and the token is never echoed back.
func TestProjectKanbanLinkCRUD(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{}) // validation off (no board)

	body := map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "ai", "done_column": "done",
	}
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create want 201, got %d", resp.StatusCode)
	}
	// The response must carry token_set but NEVER a token field.
	var raw map[string]any
	decode(t, resp, &raw)
	if raw["token_set"] != false {
		t.Fatalf("no-token link should report token_set=false, got %v", raw["token_set"])
	}
	if _, leaked := raw["token"]; leaked {
		t.Fatal("response leaked a token field")
	}
	created := raw["id"].(string)

	// List (owner) → 1 link.
	resp = do(t, http.MethodGet, f.linksURL(), f.tokens["owner"], nil)
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	decode(t, resp, &list)
	if len(list.Links) != 1 || list.Links[0].TriggerColumn != "ai" || list.Links[0].DoneColumn != "done" {
		t.Fatalf("list = %+v", list.Links)
	}

	// Delete (owner) → 200, then list empty.
	resp = do(t, http.MethodDelete, f.linksURL()+"/"+created, f.tokens["owner"], nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete want 200, got %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, f.linksURL(), f.tokens["owner"], nil)
	decode(t, resp, &list)
	if len(list.Links) != 0 {
		t.Fatalf("after delete want 0, got %d", len(list.Links))
	}
}

// A supplied token is sealed (stored encrypted, decryptable, never returned) and
// token_set flips to true; an omitted token leaves token_set false.
func TestProjectKanbanLinkTokenEncrypted(t *testing.T) {
	board := &jtype.Board{Columns: []jtype.BoardColumn{{Key: "ai"}, {Key: "done"}}}
	f := setupKanban(t, fakeBoardValidator{board: board})

	body := map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "ai", "token": "jtype-pat-secret",
	}
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create want 201, got %d", resp.StatusCode)
	}
	var view kanbanLinkView
	decode(t, resp, &view)
	if !view.TokenSet {
		t.Fatal("token_set should be true when a token was supplied")
	}

	// Stored encrypted (not plaintext), and decrypts back to the original.
	link, err := f.st.GetKanbanLink(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(link.TokenEnc) == 0 || string(link.TokenEnc) == "jtype-pat-secret" {
		t.Fatalf("token not sealed: %q", link.TokenEnc)
	}
	got, err := f.srv.Cipher().DecryptString(link.TokenEnc)
	if err != nil || got != "jtype-pat-secret" {
		t.Fatalf("decrypt = %q err=%v", got, err)
	}
}

// RBAC: only the owner (and cluster-admin/service) may list/create/delete a
// project's links; member/viewer/stranger are forbidden.
func TestProjectKanbanLinkRBAC(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{}) // validation off

	create := func(tok string) int {
		r := do(t, http.MethodPost, f.linksURL(), tok, map[string]any{
			"workspace_id": "ws-" + tok[:4], "board_ref": "b-" + tok[:4],
			"service_id": f.serviceID, "trigger_column": "ai",
		})
		defer r.Body.Close()
		return r.StatusCode
	}
	list := func(tok string) int {
		r := do(t, http.MethodGet, f.linksURL(), tok, nil)
		defer r.Body.Close()
		return r.StatusCode
	}

	want := map[string]struct{ create, list int }{
		"owner":    {http.StatusCreated, http.StatusOK},
		"admin":    {http.StatusCreated, http.StatusOK},
		"service":  {http.StatusCreated, http.StatusOK},
		"member":   {http.StatusForbidden, http.StatusForbidden},
		"viewer":   {http.StatusForbidden, http.StatusForbidden},
		"stranger": {http.StatusForbidden, http.StatusForbidden},
	}
	for role, exp := range want {
		if got := list(f.tokens[role]); got != exp.list {
			t.Errorf("role=%s list=%d want %d", role, got, exp.list)
		}
		if got := create(f.tokens[role]); got != exp.create {
			t.Errorf("role=%s create=%d want %d", role, got, exp.create)
		}
	}
}

// A link may only be deleted through the project path it belongs to: another
// project's owner gets a 404 (not a cross-project delete).
func TestProjectKanbanLinkDeleteScopedToProject(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})

	// Owner creates a link in the fixture project.
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID, "trigger_column": "ai",
	})
	var view kanbanLinkView
	decode(t, resp, &view)

	// The stranger owns a DIFFERENT project; deleting the link via that project's
	// path must 404 (the link is not in project2).
	p2 := do(t, "POST", f.ts.URL+"/api/v1/projects", f.tokens["stranger"], map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	del := do(t, http.MethodDelete,
		f.ts.URL+"/api/v1/projects/"+pv2.ID+"/kanban/links/"+view.ID, f.tokens["stranger"], nil)
	if del.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project delete want 404, got %d", del.StatusCode)
	}
	del.Body.Close()
	// The link is still there.
	if _, err := f.st.GetKanbanLink(context.Background(), view.ID); err != nil {
		t.Fatalf("link should survive cross-project delete attempt: %v", err)
	}
}

func TestProjectKanbanLinkValidation(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing trigger", map[string]any{"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID}, http.StatusBadRequest},
		{"unknown service", map[string]any{"workspace_id": "ws", "board_ref": "b", "service_id": "nope", "trigger_column": "ai"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], c.body)
			defer resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Fatalf("%s: want %d got %d", c.name, c.want, resp.StatusCode)
			}
		})
	}
}

// A service that belongs to a different project cannot be bound.
func TestProjectKanbanLinkServiceNotInProject(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})
	// Create a second project + service (owned by the same owner) and try to bind
	// that foreign service under the fixture project.
	p2 := do(t, "POST", f.ts.URL+"/api/v1/projects", f.tokens["owner"], map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	s2 := do(t, "POST", f.ts.URL+"/api/v1/projects/"+pv2.ID+"/services", f.tokens["owner"],
		map[string]any{"name": "default", "repo_url": "https://git/y.git"})
	var svc2 domain.Service
	decode(t, s2, &svc2)

	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": svc2.ID, "trigger_column": "ai",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign service want 400, got %d", resp.StatusCode)
	}
}

// Column validation against a live board (F6: with the link's token or the
// cluster fallback), plus the fail-visible edges.
func TestProjectKanbanLinkColumnValidation(t *testing.T) {
	board := &jtype.Board{Columns: []jtype.BoardColumn{{Key: "ai"}, {Key: "done"}}}
	f := setupKanban(t, fakeBoardValidator{board: board})

	// No token supplied AND no cluster fallback → token_required (fail-visible).
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID, "trigger_column": "ai",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no token & no fallback want 400 token_required, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// With a supplied token, a bad trigger column → 400.
	bad := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "nope", "token": "pat",
	})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad column want 400, got %d", bad.StatusCode)
	}
	bad.Body.Close()

	// Good columns via the CLUSTER FALLBACK token (no per-link token supplied).
	f.setClusterToken("cluster-pat")
	good := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "ai", "done_column": "done",
	})
	if good.StatusCode != http.StatusCreated {
		t.Fatalf("good columns via fallback want 201, got %d", good.StatusCode)
	}
	var view kanbanLinkView
	decode(t, good, &view)
	if view.TokenSet {
		t.Fatal("fallback link must report token_set=false")
	}

	// jtype unreachable → 503 fail-visible.
	f2 := setupKanban(t, fakeBoardValidator{err: errFakeJtype})
	f2.setClusterToken("cluster-pat")
	down := do(t, http.MethodPost, f2.linksURL(), f2.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f2.serviceID, "trigger_column": "ai",
	})
	if down.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("jtype down want 503, got %d", down.StatusCode)
	}
	down.Body.Close()
}

// A per-link token with no cipher configured is a typed 409 (never stored clear).
func TestProjectKanbanLinkTokenNeedsCipher(t *testing.T) {
	board := &jtype.Board{Columns: []jtype.BoardColumn{{Key: "ai"}}}
	f := setupKanban(t, fakeBoardValidator{board: board})
	f.srv.cipher = nil // simulate AUTH_TOKEN_KEY unset

	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "ai", "token": "pat",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("token without cipher want 409, got %d", resp.StatusCode)
	}
}

// P3: the cipher precondition is checked BEFORE the board network validation —
// with no cipher AND jtype down, a tokened create is a 409 (config error), not a
// 503 masked behind the network failure.
func TestProjectKanbanLinkCipherCheckedBeforeBoardFetch(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{err: errFakeJtype}) // jtype unreachable
	f.srv.cipher = nil                                         // AUTH_TOKEN_KEY unset

	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID,
		"trigger_column": "ai", "token": "pat",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cipher check must precede the board fetch: want 409, got %d", resp.StatusCode)
	}
}

// P1: credential_status is derived per link — per_link / cluster_fallback /
// missing — so an owner sees a dead link in the console, not in logs.
func TestProjectKanbanLinkCredentialStatus(t *testing.T) {
	board := &jtype.Board{Columns: []jtype.BoardColumn{{Key: "ai"}}}
	f := setupKanban(t, fakeBoardValidator{board: board})

	// With a per-link token -> "per_link" (cluster fallback irrelevant).
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws1", "board_ref": "b1", "service_id": f.serviceID,
		"trigger_column": "ai", "token": "pat",
	})
	var withTok kanbanLinkView
	decode(t, resp, &withTok)
	if withTok.CredentialStatus != "per_link" {
		t.Fatalf("tokened link status=%q want per_link", withTok.CredentialStatus)
	}

	// Without a per-link token, cluster fallback SET -> "cluster_fallback".
	f.setClusterToken("cluster-pat")
	resp = do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws2", "board_ref": "b2", "service_id": f.serviceID,
		"trigger_column": "ai",
	})
	var fallback kanbanLinkView
	decode(t, resp, &fallback)
	if fallback.CredentialStatus != "cluster_fallback" {
		t.Fatalf("fallback link status=%q want cluster_fallback", fallback.CredentialStatus)
	}

	// Cluster fallback UNSET -> the same tokenless link lists as "missing".
	f.setClusterToken("")
	resp = do(t, http.MethodGet, f.linksURL(), f.tokens["owner"], nil)
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	decode(t, resp, &list)
	byID := map[string]string{}
	for _, l := range list.Links {
		byID[l.ID] = l.CredentialStatus
	}
	if byID[withTok.ID] != "per_link" {
		t.Fatalf("tokened link listed as %q want per_link", byID[withTok.ID])
	}
	if byID[fallback.ID] != "missing" {
		t.Fatalf("tokenless link without cluster fallback listed as %q want missing", byID[fallback.ID])
	}
}

// P2: PATCH rotates/clears ONLY the per-link token — claims are retained (no
// re-dispatch), "" clears back to the cluster fallback, and the same RBAC /
// project-scoping / cipher gates as create apply.
func TestProjectKanbanLinkTokenRotation(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{}) // validation off
	ctx := context.Background()

	// Create a tokenless link, then stamp a dispatched claim on it.
	resp := do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID, "trigger_column": "ai",
	})
	var view kanbanLinkView
	decode(t, resp, &view)
	if _, err := f.st.EnsureKanbanClaim(ctx, view.ID, "docA", "cards/a.md"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetKanbanClaimRun(ctx, view.ID, "docA", "run-1"); err != nil {
		t.Fatal(err)
	}
	patchURL := f.linksURL() + "/" + view.ID

	// Rotate: 200, token_set=true, sealed (decrypts to the new value), claim kept.
	resp = do(t, http.MethodPatch, patchURL, f.tokens["owner"], map[string]any{"token": "new-pat"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate want 200, got %d", resp.StatusCode)
	}
	var rotated kanbanLinkView
	decode(t, resp, &rotated)
	if !rotated.TokenSet || rotated.CredentialStatus != "per_link" {
		t.Fatalf("rotated view = %+v", rotated)
	}
	link, err := f.st.GetKanbanLink(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.srv.Cipher().DecryptString(link.TokenEnc); err != nil || got != "new-pat" {
		t.Fatalf("rotated token decrypt=%q err=%v", got, err)
	}
	claim, _ := f.st.EnsureKanbanClaim(ctx, view.ID, "docA", "cards/a.md")
	if claim.RunID != "run-1" {
		t.Fatalf("rotation must retain claims; run_id=%q want run-1", claim.RunID)
	}

	// Clear with "": token gone; no cluster fallback in this fixture => "missing".
	resp = do(t, http.MethodPatch, patchURL, f.tokens["owner"], map[string]any{"token": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear want 200, got %d", resp.StatusCode)
	}
	var cleared kanbanLinkView
	decode(t, resp, &cleared)
	if cleared.TokenSet || cleared.CredentialStatus != "missing" {
		t.Fatalf("cleared view = %+v", cleared)
	}

	// Omitted token field -> typed 400 (never an accidental clear).
	resp = do(t, http.MethodPatch, patchURL, f.tokens["owner"], map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing token field want 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// RBAC: member / viewer / stranger forbidden; admin allowed.
	for _, role := range []string{"member", "viewer", "stranger"} {
		r := do(t, http.MethodPatch, patchURL, f.tokens[role], map[string]any{"token": "x"})
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("role=%s PATCH want 403, got %d", role, r.StatusCode)
		}
		r.Body.Close()
	}
	if r := do(t, http.MethodPatch, patchURL, f.tokens["admin"], map[string]any{"token": "x"}); r.StatusCode != http.StatusOK {
		t.Fatalf("admin PATCH want 200, got %d", r.StatusCode)
	}

	// Cross-project path -> 404 (as on delete).
	p2 := do(t, "POST", f.ts.URL+"/api/v1/projects", f.tokens["stranger"], map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	r := do(t, http.MethodPatch,
		f.ts.URL+"/api/v1/projects/"+pv2.ID+"/kanban/links/"+view.ID, f.tokens["stranger"],
		map[string]any{"token": "x"})
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project PATCH want 404, got %d", r.StatusCode)
	}
	r.Body.Close()

	// Cipher unset + non-empty token -> 409 (same gate as create).
	f.srv.cipher = nil
	r = do(t, http.MethodPatch, patchURL, f.tokens["owner"], map[string]any{"token": "x"})
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("PATCH token without cipher want 409, got %d", r.StatusCode)
	}
	r.Body.Close()
}

// The system overview is a cluster-admin READ-ONLY, cross-project list; a
// non-admin (owner) is forbidden.
func TestSystemKanbanLinksAdminOnly(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})
	// Seed a link so the overview has content.
	do(t, http.MethodPost, f.linksURL(), f.tokens["owner"], map[string]any{
		"workspace_id": "ws", "board_ref": "b", "service_id": f.serviceID, "trigger_column": "ai",
	}).Body.Close()

	sysURL := f.ts.URL + "/api/v1/system/kanban/links"
	// Owner (non cluster-admin) → 403.
	if r := do(t, http.MethodGet, sysURL, f.tokens["owner"], nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("owner system overview want 403, got %d", r.StatusCode)
	}
	// Admin → 200 with the link (project_id present so the admin knows the owner).
	r := do(t, http.MethodGet, sysURL, f.tokens["admin"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin overview want 200, got %d", r.StatusCode)
	}
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	decode(t, r, &list)
	if len(list.Links) != 1 || list.Links[0].ProjectID != f.projectID {
		t.Fatalf("overview = %+v", list.Links)
	}
	// The old management routes are gone (405, not a handler).
	if r := do(t, http.MethodPost, sysURL, f.tokens["admin"], map[string]any{}); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /system/kanban/links should be gone (405), got %d", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

var errFakeJtype = errString("jtype offline")

type errString string

func (e errString) Error() string { return string(e) }
