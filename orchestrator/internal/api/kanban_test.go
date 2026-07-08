package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// newKanbanServer builds a server with project+service seeded. When withBoard
// carries a board or error, it is injected as the board validator (otherwise
// validation is off, mirroring an unconfigured integration).
func newKanbanServer(t *testing.T, withBoard fakeBoardValidator) (*httptest.Server, *store.MemStore, *domain.Project, *domain.Service) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	if withBoard.board != nil || withBoard.err != nil {
		srv.jtypeBoard = withBoard
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "kan", CreatedAt: time.Now().UTC()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now().UTC()}
	_ = st.CreateService(ctx, svc)
	return ts, st, p, svc
}

func TestKanbanLinkCRUDAdmin(t *testing.T) {
	ts, st, p, svc := newKanbanServer(t, fakeBoardValidator{})
	linkURL := ts.URL + "/api/v1/system/kanban/links"

	// POST (admin via console token) → 201.
	body := map[string]any{
		"workspace_id": "ws", "board_ref": "b",
		"project_id": p.ID, "service_id": svc.ID,
		"trigger_column": "ai", "done_column": "done",
	}
	resp := do(t, http.MethodPost, linkURL, consoleToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create want 201, got %d", resp.StatusCode)
	}
	var created kanbanLinkView
	decode(t, resp, &created)
	if created.TriggerColumn != "ai" || !created.Enabled || created.DoneColumn != "done" {
		t.Fatalf("created link = %+v", created)
	}

	// GET list → 1 link.
	resp = do(t, http.MethodGet, linkURL, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list want 200, got %d", resp.StatusCode)
	}
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	decode(t, resp, &list)
	if len(list.Links) != 1 {
		t.Fatalf("list want 1, got %d", len(list.Links))
	}

	// DELETE → 200, then list empty.
	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/system/kanban/links/"+created.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete want 200, got %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, linkURL, consoleToken, nil)
	decode(t, resp, &list)
	if len(list.Links) != 0 {
		t.Fatalf("after delete want 0, got %d", len(list.Links))
	}
	_ = st
}

// POST/DELETE are admin-gated: an unauthenticated request is 401, while the
// console token (virtual cluster-admin) is accepted. A full non-admin user
// principal is exercised by the shared RBAC matrix in rbac_test.go.
func TestKanbanLinkCreateAuthGating(t *testing.T) {
	ts, _, p, svc := newKanbanServer(t, fakeBoardValidator{})
	linkURL := ts.URL + "/api/v1/system/kanban/links"
	body := map[string]any{
		"workspace_id": "ws", "board_ref": "b",
		"project_id": p.ID, "service_id": svc.ID, "trigger_column": "ai",
	}
	if resp := do(t, http.MethodPost, linkURL, "", body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token want 401, got %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, linkURL, consoleToken, body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("console token want 201, got %d", resp.StatusCode)
	}
}

func TestKanbanLinkCreateValidation(t *testing.T) {
	ts, _, p, svc := newKanbanServer(t, fakeBoardValidator{})
	linkURL := ts.URL + "/api/v1/system/kanban/links"

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing trigger", map[string]any{
			"workspace_id": "ws", "board_ref": "b", "project_id": p.ID, "service_id": svc.ID}, http.StatusBadRequest},
		{"unknown service", map[string]any{
			"workspace_id": "ws", "board_ref": "b", "project_id": p.ID, "service_id": "nope", "trigger_column": "ai"}, http.StatusBadRequest},
		{"service not in project", map[string]any{
			"workspace_id": "ws", "board_ref": "b", "project_id": "other", "service_id": svc.ID, "trigger_column": "ai"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := do(t, http.MethodPost, linkURL, consoleToken, c.body)
			if resp.StatusCode != c.want {
				body := ""
				_ = json.NewDecoder(resp.Body).Decode(&body)
				t.Fatalf("%s: want %d, got %d (%s)", c.name, c.want, resp.StatusCode, body)
			}
		})
	}
}

// Board column validation: a configured jtype rejects an unknown column, and a
// jtype failure surfaces as 503 (fail-visible) rather than a silent accept.
func TestKanbanLinkColumnValidation(t *testing.T) {
	board := &jtype.Board{ID: "b", Title: "B", Columns: []jtype.BoardColumn{
		{Key: "todo", Name: "Todo"}, {Key: "ai", Name: "AI"}, {Key: "done", Name: "Done"},
	}}
	ts, _, p, svc := newKanbanServer(t, fakeBoardValidator{board: board})
	linkURL := ts.URL + "/api/v1/system/kanban/links"

	// Bad trigger column → 400.
	bad := map[string]any{"workspace_id": "ws", "board_ref": "b",
		"project_id": p.ID, "service_id": svc.ID, "trigger_column": "nope"}
	if resp := do(t, http.MethodPost, linkURL, consoleToken, bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad column want 400, got %d", resp.StatusCode)
	}
	// Bad done column → 400.
	badDone := map[string]any{"workspace_id": "ws", "board_ref": "b",
		"project_id": p.ID, "service_id": svc.ID, "trigger_column": "ai", "done_column": "nope"}
	if resp := do(t, http.MethodPost, linkURL, consoleToken, badDone); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad done column want 400, got %d", resp.StatusCode)
	}
	// Good columns → 201.
	good := map[string]any{"workspace_id": "ws", "board_ref": "b",
		"project_id": p.ID, "service_id": svc.ID, "trigger_column": "ai", "done_column": "done"}
	if resp := do(t, http.MethodPost, linkURL, consoleToken, good); resp.StatusCode != http.StatusCreated {
		t.Fatalf("good columns want 201, got %d", resp.StatusCode)
	}

	// jtype unreachable → 503 fail-visible.
	ts2, _, p2, svc2 := newKanbanServer(t, fakeBoardValidator{err: errFakeJtype})
	body2 := map[string]any{"workspace_id": "ws", "board_ref": "b",
		"project_id": p2.ID, "service_id": svc2.ID, "trigger_column": "ai"}
	if resp := do(t, http.MethodPost, ts2.URL+"/api/v1/system/kanban/links", consoleToken, body2); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("jtype down want 503, got %d", resp.StatusCode)
	}
}

var errFakeJtype = errString("jtype offline")

type errString string

func (e errString) Error() string { return string(e) }
