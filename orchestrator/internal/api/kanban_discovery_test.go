package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
)

// fakeDiscovery is a test stand-in for the jtype discovery client (D30 pickers).
type fakeDiscovery struct {
	workspaces []jtype.Workspace
	docs       []jtype.Doc
	boards     map[string]*jtype.Board // relativePath -> board
	err        error                   // when set, ListWorkspaces/ListDocuments return it
}

func (f fakeDiscovery) ListWorkspaces(ctx context.Context) ([]jtype.Workspace, error) {
	return f.workspaces, f.err
}
func (f fakeDiscovery) ListDocuments(ctx context.Context, ws string) ([]jtype.Doc, error) {
	return f.docs, f.err
}
func (f fakeDiscovery) GetBoard(ctx context.Context, ws, ref string) (*jtype.Board, error) {
	if b, ok := f.boards[ref]; ok {
		return b, nil
	}
	return nil, jtype.ErrDocNotFound
}
func (f fakeDiscovery) GetBoardByDoc(ctx context.Context, ws, docID string) (*jtype.Board, error) {
	// Resolve the doc id back to its path (as the real client does via GetDocument),
	// then return the same board GetBoard would — the boards map is path-keyed.
	for _, d := range f.docs {
		if d.ID == docID {
			if b, ok := f.boards[d.Path]; ok {
				return b, nil
			}
		}
	}
	return nil, jtype.ErrDocNotFound
}

// discoveryFixture wires a fake discovery client onto an owner-authorized kanban
// fixture with the integration ON (base URL set) and ONE token-bearing link
// seeded in the project — D36: discovery borrows a per-link token from the
// project's existing links (there is no cluster token). The distinctive token
// lets the no-leak test scan for it; lastToken records which credential the
// server resolved for the discovery client.
const discoveryToken = "per-link-secret-tok-DO-NOT-LEAK"

func discoveryFixture(t *testing.T, fake fakeDiscovery) kanbanFixture {
	t.Helper()
	// A board arg turns the integration ON (sets JtypeBaseURL => Factory ok).
	f := setupKanban(t, fakeBoardValidator{board: &jtype.Board{}})
	enc, err := f.srv.Cipher().EncryptString(discoveryToken)
	if err != nil {
		t.Fatal(err)
	}
	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws-1", BoardRef: "b1",
		ProjectID: f.projectID, ServiceID: f.serviceID, TriggerColumn: "ai",
		Enabled: true, TokenEnc: enc, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := f.st.CreateKanbanLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	f.srv.jtypeDiscoveryFor = func(_ *jtype.Factory, token string) jtypeDiscovery {
		lastDiscoveryToken = token
		return fake
	}
	return f
}

// lastDiscoveryToken captures the token the server resolved for the discovery
// client (set by the seam in discoveryFixture).
var lastDiscoveryToken string

func (f kanbanFixture) workspacesURL() string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/jtype/workspaces"
}
func (f kanbanFixture) boardsURL(ws string) string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/jtype/boards?workspace=" + ws
}

// C4: workspaces is owner-only (member → 403) and returns {workspaces:[…]}.
func TestListWorkspaces_OwnerOnly(t *testing.T) {
	f := discoveryFixture(t, fakeDiscovery{workspaces: []jtype.Workspace{{ID: "ws-1", Name: "My Team"}}})

	if r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["member"], nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member want 403, got %d", r.StatusCode)
	}
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("owner want 200, got %d", r.StatusCode)
	}
	var out struct {
		Workspaces []jtypeWorkspaceView `json:"workspaces"`
	}
	decode(t, r, &out)
	if len(out.Workspaces) != 1 || out.Workspaces[0].ID != "ws-1" || out.Workspaces[0].Name != "My Team" {
		t.Fatalf("workspaces = %+v", out.Workspaces)
	}
}

// C4: boards returns {boards:[{id,ref,title,columns}]} — ref is the relativePath
// (what create submits), id is the config id, columns seed the column pickers.
func TestListBoards_ReturnsColumns(t *testing.T) {
	fake := fakeDiscovery{
		docs: []jtype.Doc{
			{ID: "d1", Path: "jtype.board"},
			{ID: "d2", Path: "notes.md"}, // filtered out
		},
		boards: map[string]*jtype.Board{
			"jtype.board": {ID: "b_ab12cd34", Title: "jtype", Columns: []jtype.BoardColumn{{Key: "todo", Name: "To do"}, {Key: "ai", Name: "AI"}}},
		},
	}
	f := discoveryFixture(t, fake)

	r := do(t, http.MethodGet, f.boardsURL("ws-1"), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("boards want 200, got %d", r.StatusCode)
	}
	var out struct {
		Boards []jtypeBoardView `json:"boards"`
	}
	decode(t, r, &out)
	if len(out.Boards) != 1 {
		t.Fatalf("boards = %+v want 1 (.board only)", out.Boards)
	}
	b := out.Boards[0]
	if b.ID != "b_ab12cd34" || b.Ref != "jtype.board" || b.Title != "jtype" {
		t.Fatalf("board = %+v", b)
	}
	if len(b.Columns) != 2 || b.Columns[1].Key != "ai" {
		t.Fatalf("columns = %+v", b.Columns)
	}
}

// C4 P0 privacy: neither discovery response body may contain the effective token.
func TestDiscovery_NoTokenLeak(t *testing.T) {
	fake := fakeDiscovery{
		workspaces: []jtype.Workspace{{ID: "ws-1", Name: "My Team"}},
		docs:       []jtype.Doc{{ID: "d1", Path: "jtype.board"}},
		boards:     map[string]*jtype.Board{"jtype.board": {ID: "b_x", Title: "t", Columns: []jtype.BoardColumn{{Key: "ai"}}}},
	}
	f := discoveryFixture(t, fake)

	for _, url := range []string{f.workspacesURL(), f.boardsURL("ws-1")} {
		r := do(t, http.MethodGet, url, f.tokens["owner"], nil)
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(body), discoveryToken) {
			t.Fatalf("response for %s leaked the effective token: %s", url, body)
		}
	}
}

// C4: integration off → typed 409 kanban_not_configured (fail-visible, not 200).
func TestDiscovery_IntegrationOff(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{}) // no base URL => Factory !ok
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("integration off want 409, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "kanban_not_configured" {
		t.Fatalf("error code = %q want kanban_not_configured", code)
	}
}

// C4: a jtype 401 is a 400 jtype_unauthorized (config error), not a 503.
func TestDiscovery_JtypeUnauthorized(t *testing.T) {
	f := discoveryFixture(t, fakeDiscovery{err: &jtype.Error{StatusCode: 401, Code: "unauthorized", Message: "bad token"}})
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unauthorized want 400, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "jtype_unauthorized" {
		t.Fatalf("error code = %q want jtype_unauthorized", code)
	}
}

// C4 / D36: integration on but the project has NO token-bearing link to borrow
// → typed 409 kanban_token_required (fail-visible; the console then falls back
// to manual entry — never a silent empty list).
func TestDiscovery_NoToken_409(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{board: &jtype.Board{}}) // base URL set; no links at all
	f.srv.jtypeDiscoveryFor = func(_ *jtype.Factory, _ string) jtypeDiscovery {
		return fakeDiscovery{workspaces: []jtype.Workspace{{ID: "ws-1"}}}
	}
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("no token-bearing link want 409, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "kanban_token_required" {
		t.Fatalf("error code = %q want kanban_token_required", code)
	}
}

// D36: discovery authorises jtype reads with a PER-LINK token borrowed from one
// of the project's links — the same token the link itself would use.
func TestDiscovery_BorrowsPerLinkToken(t *testing.T) {
	f := discoveryFixture(t, fakeDiscovery{workspaces: []jtype.Workspace{{ID: "ws-1", Name: "My Team"}}})

	lastDiscoveryToken = ""
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("owner want 200, got %d", r.StatusCode)
	}
	r.Body.Close()
	if lastDiscoveryToken != discoveryToken {
		t.Fatalf("discovery client resolved token %q want the link's per-link token", lastDiscoveryToken)
	}
}

// D36: a tokenless link does NOT unblock discovery, but a DISABLED link WITH a
// token does (its credential is still valid; enabled links are preferred).
func TestDiscovery_TokenlessLinksDontCount(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{board: &jtype.Board{}})
	// A tokenless enabled link: not borrowable.
	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws-1", BoardRef: "b1",
		ProjectID: f.projectID, ServiceID: f.serviceID, TriggerColumn: "ai",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := f.st.CreateKanbanLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	r := do(t, http.MethodGet, f.workspacesURL(), f.tokens["owner"], nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("tokenless link must not unblock discovery: want 409, got %d", r.StatusCode)
	}
	if code := errorCode(t, r); code != "kanban_token_required" {
		t.Fatalf("error code = %q want kanban_token_required", code)
	}
	r.Body.Close()
}

// C4: boards requires the ?workspace= query parameter.
func TestListBoards_RequiresWorkspace(t *testing.T) {
	f := discoveryFixture(t, fakeDiscovery{})
	r := do(t, http.MethodGet, f.ts.URL+"/api/v1/projects/"+f.projectID+"/kanban/jtype/boards", f.tokens["owner"], nil)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing workspace want 400, got %d", r.StatusCode)
	}
}
