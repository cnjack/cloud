package jtype

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// docsFrom builds a []Doc from relativePaths (ids are "id:"+path).
func docsFrom(paths ...string) []Doc {
	out := make([]Doc, 0, len(paths))
	for _, p := range paths {
		out = append(out, Doc{ID: "id:" + p, Path: p})
	}
	return out
}

// C1 — the pure board resolver, mirroring board-react's resolveBoard.test.ts.

func TestResolveBoard_RootByName(t *testing.T) {
	// RC1 regression: a real board lives at the workspace root as <name>.board, not
	// under boards/. The old boards/<ref>.board lookup would miss it.
	d, err := resolveBoardDoc(docsFrom("jtype.board", "notes.md"), "jtype")
	if err != nil {
		t.Fatalf("resolve root board: %v", err)
	}
	if d.Path != "jtype.board" {
		t.Fatalf("resolved %q want jtype.board", d.Path)
	}
}

func TestResolveBoard_CaseInsensitive(t *testing.T) {
	d, err := resolveBoardDoc(docsFrom("Jcode.board"), "jcode")
	if err != nil {
		t.Fatalf("case-insensitive resolve: %v", err)
	}
	if d.Path != "Jcode.board" {
		t.Fatalf("resolved %q", d.Path)
	}
}

func TestResolveBoard_DotBoardSuffixTolerated(t *testing.T) {
	for _, ref := range []string{"jtype.board", "./jtype", "/jtype", "  jtype  "} {
		d, err := resolveBoardDoc(docsFrom("jtype.board"), ref)
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		if d.Path != "jtype.board" {
			t.Fatalf("ref %q resolved %q", ref, d.Path)
		}
	}
}

func TestResolveBoard_ExactPathBeatsBasename(t *testing.T) {
	docs := docsFrom("jtype.board", "sub/jtype.board")
	// Exact path wins for the root file.
	d, err := resolveBoardDoc(docs, "jtype.board")
	if err != nil || d.Path != "jtype.board" {
		t.Fatalf("exact root = %q err=%v", d.Path, err)
	}
	// A full sub path resolves to the sub file (exact).
	d, err = resolveBoardDoc(docs, "sub/jtype.board")
	if err != nil || d.Path != "sub/jtype.board" {
		t.Fatalf("exact sub = %q err=%v", d.Path, err)
	}
}

func TestResolveBoard_AmbiguousBasename(t *testing.T) {
	docs := docsFrom("a/jtype.board", "b/jtype.board")
	_, err := resolveBoardDoc(docs, "jtype")
	var ambig *ErrBoardAmbiguousError
	if !errors.As(err, &ambig) {
		t.Fatalf("want ErrBoardAmbiguousError, got %v", err)
	}
	if len(ambig.Candidates) != 2 {
		t.Fatalf("candidates = %v want 2", ambig.Candidates)
	}
}

func TestResolveBoard_NotFound(t *testing.T) {
	if _, err := resolveBoardDoc(docsFrom("notes.md", "other.board"), "jtype"); !errors.Is(err, ErrDocNotFound) {
		t.Fatalf("want ErrDocNotFound, got %v", err)
	}
}

func TestResolveBoard_EmptyRef(t *testing.T) {
	if _, err := resolveBoardDoc(docsFrom("jtype.board"), "   "); !errors.Is(err, ErrDocNotFound) {
		t.Fatalf("empty ref want ErrDocNotFound, got %v", err)
	}
}

// C1 HTTP-level: GetBoard returns the board's config id (the b_… cards carry in
// frontmatter) — the value canonicalized into a link's BoardRef (RC2).
func TestGetBoard_ReturnsConfigID(t *testing.T) {
	f := newFakeJtype()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	ctx := context.Background()

	// A root-level board named jtype.board whose JSON id is a random b_… config id.
	_ = c.SaveDocument(ctx, "ws", "jtype.board",
		`{"id":"b_ab12cd34","title":"jtype","columns":[{"key":"ai","name":"AI"},{"key":"done","name":"Done"}]}`, "")

	b, err := c.GetBoard(ctx, "ws", "jtype")
	if err != nil {
		t.Fatalf("get board by name: %v", err)
	}
	if b.ID != "b_ab12cd34" {
		t.Fatalf("board id = %q want b_ab12cd34 (the config id)", b.ID)
	}
	if b.Title != "jtype" {
		t.Fatalf("board title = %q", b.Title)
	}
	if len(b.Columns) != 2 || b.Columns[0].Key != "ai" {
		t.Fatalf("columns = %+v", b.Columns)
	}

	// A name with no matching .board is ErrDocNotFound (not a 503-ish transport err).
	if _, err := c.GetBoard(ctx, "ws", "ghost"); !errors.Is(err, ErrDocNotFound) {
		t.Fatalf("missing board want ErrDocNotFound, got %v", err)
	}
}

// C4 client-level: ListWorkspaces hits GET /api/v1/workspaces (which jtype wraps
// as {"workspaces":[…]}, NOT a bare array) and tolerates either a name or title
// label.
func TestListWorkspaces(t *testing.T) {
	f := newFakeJtype()
	f.mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workspaces": []map[string]any{
			{"id": "ws-1", "name": "My Team"},
			{"id": "ws-2", "title": "Other"}, // title fallback
		}})
	})
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)

	wss, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(wss) != 2 || wss[0].ID != "ws-1" || wss[0].Name != "My Team" {
		t.Fatalf("workspaces = %+v", wss)
	}
	if wss[1].Name != "Other" {
		t.Fatalf("title fallback failed: %+v", wss[1])
	}
}
