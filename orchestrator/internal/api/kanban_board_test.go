package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
)

// --- D31 board embed proxy: fake jtype client + fixture ------------------------

// proxyCall records one ProxyDocumentAPI invocation so a test can assert the
// method, the server-built path, the streamed body, and — critically — WHICH
// token the proxy resolved (per-link vs cluster).
type proxyCall struct {
	method string
	path   string
	body   string
	token  string
}

// boardProxyStub is the shared, mutable fake behind the boardProxyFor seam. It
// records every call and replays a canned status+body (or a transport error). One
// stub backs a whole test; each ProxyDocumentAPI is served by a boardProxyConn that
// captures the token the seam resolved for that request.
type boardProxyStub struct {
	mu     sync.Mutex
	calls  []proxyCall
	status int
	body   string
	err    error
}

func (b *boardProxyStub) proxyFor(_ *jtype.Factory, token string) jtypeBoardProxy {
	return &boardProxyConn{stub: b, token: token}
}

func (b *boardProxyStub) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *boardProxyStub) lastCall() proxyCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return proxyCall{}
	}
	return b.calls[len(b.calls)-1]
}

type boardProxyConn struct {
	stub  *boardProxyStub
	token string
}

func (c *boardProxyConn) ProxyDocumentAPI(_ context.Context, method, path string, body io.Reader) (*http.Response, error) {
	var bod string
	if body != nil {
		bb, _ := io.ReadAll(body)
		bod = string(bb)
	}
	c.stub.mu.Lock()
	c.stub.calls = append(c.stub.calls, proxyCall{method: method, path: path, body: bod, token: c.token})
	st, bd, er := c.stub.status, c.stub.body, c.stub.err
	c.stub.mu.Unlock()
	if er != nil {
		return nil, er
	}
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{
		StatusCode: st,
		Body:       io.NopCloser(strings.NewReader(bd)),
		Header:     make(http.Header),
	}, nil
}

// boardFixture is a kanbanFixture with the jtype integration ON and a mutable
// board-proxy stub wired behind the boardProxyFor seam. The distinctive cluster
// token lets the no-leak scan detect a serialized token.
type boardFixture struct {
	kanbanFixture
	stub *boardProxyStub
}

const boardClusterToken = "cluster-secret-board-tok-DO-NOT-LEAK"

// setupBoard builds the fixture. clusterToken=true sets the cluster fallback so a
// tokenless link resolves a credential (the common path); pass false to exercise
// the no-credential 503 edge.
func setupBoard(t *testing.T, clusterToken bool) boardFixture {
	t.Helper()
	// A board arg turns the integration ON (env base URL => Factory ok).
	f := setupKanban(t, fakeBoardValidator{board: &jtype.Board{}})
	if clusterToken {
		f.setClusterToken(boardClusterToken)
	}
	stub := &boardProxyStub{status: http.StatusOK, body: "[]"}
	f.srv.boardProxyFor = stub.proxyFor
	return boardFixture{kanbanFixture: f, stub: stub}
}

// seedLink inserts a kanban link into the store directly (bypassing the
// canonicalizing create path) so a test controls workspace/token/enabled exactly.
func (f boardFixture) seedLink(t *testing.T, workspace, boardRef string, tokenEnc []byte, enabled bool) *domain.KanbanLink {
	t.Helper()
	now := time.Now().UTC()
	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: workspace, BoardRef: boardRef,
		ProjectID: f.projectID, ServiceID: f.serviceID,
		TriggerColumn: "ai", Enabled: enabled, TokenEnc: tokenEnc,
		BoardStatus: domain.KanbanBoardOK, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.st.CreateKanbanLink(context.Background(), link); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	return link
}

func (f boardFixture) docsURL(ws string) string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/board/documents?workspace=" + ws
}
func (f boardFixture) saveURL(ws string) string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/board/documents/save?workspace=" + ws
}
func (f boardFixture) docURL(ws, docID string) string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/board/documents/" + docID + "?workspace=" + ws
}
func (f boardFixture) boardLinksURL() string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/board/links"
}

// 1. A member reading THEIR project's linked workspace gets 200 with the upstream
// body copied through byte-for-byte (including isPublished/versionId fidelity),
// and the proxy issued exactly one GET to the server-built path.
func TestBoardProxy_MemberReadsOwnWorkspace(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)
	f.stub.body = `[{"id":"d1","relativePath":"a.board","isPublished":true,"versionId":"v9"}]`

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("member read want 200, got %d", r.StatusCode)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if string(body) != f.stub.body {
		t.Fatalf("body not passed through verbatim:\n got %s\nwant %s", body, f.stub.body)
	}
	if n := f.stub.callCount(); n != 1 {
		t.Fatalf("proxy calls = %d want 1", n)
	}
	c := f.stub.lastCall()
	if c.method != http.MethodGet || c.path != "/api/v1/workspaces/ws-A/documents" {
		t.Fatalf("upstream call = %s %s", c.method, c.path)
	}
}

// 2. A non-member (stranger) is 403'd BEFORE any jtype call.
func TestBoardProxy_NonMember403(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["stranger"], nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member want 403, got %d", r.StatusCode)
	}
	r.Body.Close()
	if n := f.stub.callCount(); n != 0 {
		t.Fatalf("non-member reached jtype (%d calls)", n)
	}
}

// 3. A viewer is 403'd (read+write threshold is member+; viewers get no board).
func TestBoardProxy_Viewer403(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["viewer"], nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer want 403, got %d", r.StatusCode)
	}
	r.Body.Close()
	if n := f.stub.callCount(); n != 0 {
		t.Fatalf("viewer reached jtype (%d calls)", n)
	}
}

// 4. THE CONFUSED-DEPUTY PROOF: a member of project A cannot read a workspace that
// belongs to a DIFFERENT project's link. Even though the cluster token could read
// ws-B (it backs project B's own board), A's member is 403'd (workspace_not_linked)
// and NOTHING is forwarded to jtype.
func TestBoardProxy_WorkspaceNotInProjectLinks403(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true) // A's only link

	// A second project (owned by the stranger) with a link to ws-B. ws-B is a real,
	// readable workspace — just not one THIS project was granted.
	p2 := do(t, http.MethodPost, f.ts.URL+"/api/v1/projects", f.tokens["stranger"], map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	now := time.Now().UTC()
	if err := f.st.CreateKanbanLink(context.Background(), &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws-B", BoardRef: "b_b",
		ProjectID: pv2.ID, ServiceID: "svc-b", TriggerColumn: "ai", Enabled: true,
		BoardStatus: domain.KanbanBoardOK, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Member of A asks for ws-B through A's project path.
	r := do(t, http.MethodGet, f.docsURL("ws-B"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-workspace read want 403, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "workspace_not_linked" {
		t.Fatalf("error code = %q want workspace_not_linked", code)
	}
	if n := f.stub.callCount(); n != 0 {
		t.Fatalf("confused-deputy guard leaked a jtype call (%d)", n)
	}
}

// 5. Missing ?workspace= is a typed 400 before any jtype call.
func TestBoardProxy_MissingWorkspace400(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)

	r := do(t, http.MethodGet, f.ts.URL+"/api/v1/projects/"+f.projectID+"/kanban/board/documents", f.tokens["member"], nil)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing workspace want 400, got %d", r.StatusCode)
	}
	r.Body.Close()
	if n := f.stub.callCount(); n != 0 {
		t.Fatalf("missing-workspace reached jtype (%d calls)", n)
	}
}

// 6. Integration off (Factory !ok) → 409 kanban_not_configured (fail-visible). The
// scoping check still passes first (a link exists), then the resolver reports off.
func TestBoardProxy_KanbanNotConfigured409(t *testing.T) {
	// No board => setupKanban leaves the env base URL unset => Factory !ok.
	kf := setupKanban(t, fakeBoardValidator{})
	stub := &boardProxyStub{status: http.StatusOK, body: "[]"}
	kf.srv.boardProxyFor = stub.proxyFor
	f := boardFixture{kanbanFixture: kf, stub: stub}
	f.seedLink(t, "ws-A", "b_a", nil, true)

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("integration off want 409, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "kanban_not_configured" {
		t.Fatalf("error code = %q want kanban_not_configured", code)
	}
}

// 7. A link with no per-link token and NO cluster fallback → 503 jtype_unreachable
// (ResolveToken => ErrNoToken). Never a silent skip.
func TestBoardProxy_NoCredential503(t *testing.T) {
	f := setupBoard(t, false) // integration ON, but no cluster token
	f.seedLink(t, "ws-A", "b_a", nil, true)

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no credential want 503, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "jtype_unreachable" {
		t.Fatalf("error code = %q want jtype_unreachable", code)
	}
	if n := f.stub.callCount(); n != 0 {
		t.Fatalf("no-credential must not call jtype (%d)", n)
	}
}

// 8. A per-link token is PREFERRED over the cluster fallback: the proxy resolves
// and forwards the link's own token, not the cluster one.
func TestBoardProxy_PerLinkTokenPreferred(t *testing.T) {
	f := setupBoard(t, true) // cluster token set...
	const perLink = "per-link-secret-DO-NOT-LEAK"
	enc, err := f.srv.Cipher().EncryptString(perLink)
	if err != nil {
		t.Fatal(err)
	}
	f.seedLink(t, "ws-A", "b_a", enc, true) // ...but the link has its own token

	r := do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("read want 200, got %d", r.StatusCode)
	}
	r.Body.Close()
	c := f.stub.lastCall()
	if c.token != perLink {
		t.Fatalf("proxy used token %q want the per-link token", c.token)
	}
	if c.token == boardClusterToken {
		t.Fatal("proxy used the cluster token instead of the per-link token")
	}
}

// 9. saveDocument proxies POST …/documents/save with the body + resolved token; the
// upstream save response (mergeStatus/contentHash) is copied through.
func TestBoardProxy_SaveForwards(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)
	f.stub.body = `{"relativePath":"a.md","contentHash":"h2","updatedClock":8,"mergeStatus":"clean"}`

	reqBody := map[string]any{"relativePath": "a.md", "content": "x", "baseContentHash": "h1"}
	r := do(t, http.MethodPost, f.saveURL("ws-A"), f.tokens["member"], reqBody)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("save want 200, got %d", r.StatusCode)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(body), `"mergeStatus":"clean"`) || !strings.Contains(string(body), `"contentHash":"h2"`) {
		t.Fatalf("save response not passed through: %s", body)
	}
	c := f.stub.lastCall()
	if c.method != http.MethodPost || c.path != "/api/v1/workspaces/ws-A/documents/save" {
		t.Fatalf("save call = %s %s", c.method, c.path)
	}
	if !strings.Contains(c.body, `"relativePath":"a.md"`) || !strings.Contains(c.body, `"content":"x"`) {
		t.Fatalf("save body not forwarded: %s", c.body)
	}
	if c.token != boardClusterToken {
		t.Fatalf("save used token %q want cluster fallback", c.token)
	}
}

// 10. The resolved token appears in NO response body — across list/get/save AND a
// transport-error (503) response — and the reduced link view has no token field.
func TestBoardProxy_TokenNeverSerialized(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)

	scan := func(name string, r *http.Response) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(body), boardClusterToken) {
			t.Fatalf("%s response leaked the token: %s", name, body)
		}
	}

	// Success bodies (list / get / save).
	f.stub.body = `[{"id":"d1","relativePath":"a.board"}]`
	scan("list", do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil))
	f.stub.body = `{"relativePath":"a.board","content":"{}"}`
	scan("get", do(t, http.MethodGet, f.docURL("ws-A", "d1"), f.tokens["member"], nil))
	f.stub.body = `{"contentHash":"h","mergeStatus":"clean"}`
	scan("save", do(t, http.MethodPost, f.saveURL("ws-A"), f.tokens["member"], map[string]any{"relativePath": "a.md", "content": "x"}))

	// Transport-error path (503 envelope) must also be clean.
	f.stub.err = errString("dial tcp 10.0.0.1:443: connection refused")
	scan("error", do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil))

	// The reduced link view carries no credential fields at all.
	r := do(t, http.MethodGet, f.boardLinksURL(), f.tokens["member"], nil)
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	for _, k := range []string{"token", "token_set", "credential_status", "token_expires_at"} {
		if strings.Contains(string(body), k) {
			t.Fatalf("board/links leaked credential key %q: %s", k, body)
		}
	}
}

// 11. board/links is member+ and returns the reduced view; a non-member is 403'd.
func TestBoardEmbedLinks_MemberOkNonMember403(t *testing.T) {
	f := setupBoard(t, true)
	// Even a link WITH a per-link token must not leak credential posture.
	enc, _ := f.srv.Cipher().EncryptString("secret")
	f.seedLink(t, "ws-A", "b_a", enc, true)

	// Member gets the list.
	r := do(t, http.MethodGet, f.boardLinksURL(), f.tokens["member"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("member board/links want 200, got %d", r.StatusCode)
	}
	var out struct {
		Links []boardEmbedLinkView `json:"links"`
	}
	decode(t, r, &out)
	if len(out.Links) != 1 || out.Links[0].WorkspaceID != "ws-A" || out.Links[0].BoardRef != "b_a" {
		t.Fatalf("board/links = %+v", out.Links)
	}

	// Non-member is forbidden (→ empty list → no Kanban button).
	r = do(t, http.MethodGet, f.boardLinksURL(), f.tokens["stranger"], nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member board/links want 403, got %d", r.StatusCode)
	}
	r.Body.Close()
}

// 12. save is restricted to card (.md) paths: a member can't overwrite the
// .board config JSON or an arbitrary non-card doc in the linked workspace, and a
// rejected save never reaches jtype.
func TestBoardProxy_SaveRejectsNonCardPath(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, true)

	for _, bad := range []string{"my.board", "../secret.md", "notes/private.txt"} {
		body := map[string]any{"relativePath": bad, "content": "x"}
		r := do(t, http.MethodPost, f.saveURL("ws-A"), f.tokens["member"], body)
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("save relativePath=%q want 403, got %d", bad, r.StatusCode)
		}
		r.Body.Close()
	}
	// Rejected before any upstream call.
	if f.stub.lastCall().method != "" {
		t.Fatalf("a rejected save must not reach jtype: %+v", f.stub.lastCall())
	}
}

// 13. A DISABLED link grants no board access (matches the poller, which scans
// only enabled links): it is absent from board/links (→ no Kanban button) and its
// workspace is rejected by the proxy before any jtype call.
func TestBoardProxy_DisabledLinkNoAccess(t *testing.T) {
	f := setupBoard(t, true)
	f.seedLink(t, "ws-A", "b_a", nil, false) // disabled

	r := do(t, http.MethodGet, f.boardLinksURL(), f.tokens["member"], nil)
	var out struct {
		Links []boardEmbedLinkView `json:"links"`
	}
	decode(t, r, &out)
	if len(out.Links) != 0 {
		t.Fatalf("a disabled link must not appear in board/links: %+v", out.Links)
	}

	r = do(t, http.MethodGet, f.docsURL("ws-A"), f.tokens["member"], nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled-link workspace want 403, got %d", r.StatusCode)
	}
	r.Body.Close()
	if f.stub.lastCall().method != "" {
		t.Fatalf("a disabled-link proxy must not reach jtype: %+v", f.stub.lastCall())
	}
}
