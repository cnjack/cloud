package jtype

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeJtype is a tiny stand-in for the jtype document API, exercising the
// client's request shapes and error handling without a real server.
type fakeJtype struct {
	mux *http.ServeMux
	// in-memory docs keyed by id; path<->id maps for list/get.
	docs     map[string]fakeDoc
	pathToID map[string]string
	lastBody string // last comment body posted (for assertions)
}

type fakeDoc struct {
	path string
	body string // .md content
	hash string
}

func newFakeJtype() *fakeJtype {
	f := &fakeJtype{
		mux:      http.NewServeMux(),
		docs:     map[string]fakeDoc{},
		pathToID: map[string]string{},
	}
	f.mux.HandleFunc("/api/v1/workspaces/", f.handle)
	return f
}

func (f *fakeJtype) handle(w http.ResponseWriter, r *http.Request) {
	// All routes start with /api/v1/workspaces/{ws}/...; we route on the suffix
	// after the workspace id segment.
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/workspaces/")
	// rest = "{ws}/documents..." or "{ws}/documents/save" etc.
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	// parts[0] = ws, parts[1] = "documents"
	suffix := ""
	if len(parts) == 3 {
		suffix = parts[2]
	}
	switch {
	case suffix == "" && r.Method == http.MethodGet: // list
		var out []map[string]any
		for id, d := range f.docs {
			out = append(out, map[string]any{
				"id": id, "relativePath": d.path, "title": d.path, "updatedClock": 1,
			})
		}
		writeJSON(w, http.StatusOK, out)

	case suffix == "save" && r.Method == http.MethodPost: // save
		var req struct {
			RelativePath    string `json:"relativePath"`
			Content         string `json:"content"`
			BaseContentHash string `json:"baseContentHash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id, ok := f.pathToID[req.RelativePath]
		if !ok {
			id = "doc-" + req.RelativePath
			f.pathToID[req.RelativePath] = id
		}
		// Concurrent-edit check: if a base hash was supplied and mismatches, 409.
		if req.BaseContentHash != "" {
			if cur, found := f.docs[id]; found && cur.hash != req.BaseContentHash {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "conflict", "message": "stale base"})
				return
			}
		}
		h := fmt.Sprintf("h-%x", len(req.Content))
		f.docs[id] = fakeDoc{path: req.RelativePath, body: req.Content, hash: h}
		writeJSON(w, http.StatusOK, map[string]any{"contentHash": h})

	case strings.Contains(suffix, "/comments") && r.Method == http.MethodPost: // comment
		var req struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastBody = req.Body
		writeJSON(w, http.StatusOK, map[string]any{"id": "cmt-1"})

	case strings.Contains(suffix, "/comments") && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, []any{})

	default:
		// GET .../documents/{id}
		id := parts[2]
		d, ok := f.docs[id]
		if !ok {
			// id might be the path key for a freshly-listed doc.
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"relativePath": d.path, "title": d.path, "content": d.body,
			"contentHash": d.hash, "updatedClock": 1,
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func TestParseCard(t *testing.T) {
	content := "---\nboard: jcloud-dev\nstatus: todo\ntitle: Add a health banner\n---\n\nAdd a /healthz freshness banner.\n"
	c := ParseCard(content)
	if c.Board != "jcloud-dev" || c.Status != "todo" || c.Title != "Add a health banner" {
		t.Fatalf("frontmatter = %+v", c)
	}
	if !strings.Contains(c.Body, "freshness banner") {
		t.Fatalf("body = %q", c.Body)
	}

	// Quoted title + no board → not a card.
	quoted := "---\nstatus: \"ai\"\ntitle: \"Do thing\"\n---\nbody\n"
	c2 := ParseCard(quoted)
	if c2.Status != "ai" || c2.Title != "Do thing" || c2.Board != "" {
		t.Fatalf("quoted parse = %+v", c2)
	}

	// No frontmatter → not a card.
	if c3 := ParseCard("just markdown"); c3.Board != "" || c3.Status != "" {
		t.Fatalf("plain doc should be empty card, got %+v", c3)
	}
}

func TestSetStatusReplacesAndInserts(t *testing.T) {
	// Replace existing status, preserving body + other keys byte-for-byte.
	orig := "---\nboard: b\nstatus: todo\ntitle: T\n---\nbody\n"
	got := SetStatus(orig, "ai")
	if !strings.Contains(got, "status: ai") {
		t.Fatalf("status not rewritten: %q", got)
	}
	if strings.Contains(got, "status: todo") {
		t.Fatalf("old status lingers: %q", got)
	}
	if !strings.Contains(got, "board: b") || !strings.Contains(got, "title: T") || !strings.HasSuffix(got, "body\n") {
		t.Fatalf("other content not preserved: %q", got)
	}
	c := ParseCard(got)
	if c.Status != "ai" || c.Board != "b" || c.Title != "T" {
		t.Fatalf("re-parse mismatch: %+v", c)
	}

	// Insert status when absent.
	ins := SetStatus("---\nboard: b\n---\nbody\n", "review")
	if !strings.Contains(ins, "status: review") || !strings.Contains(ins, "board: b") {
		t.Fatalf("insert failed: %q", ins)
	}
}

func TestClientListGetSaveComment(t *testing.T) {
	f := newFakeJtype()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	ctx := context.Background()

	// Seed a card by saving it (also tests SaveDocument without a base hash).
	card := "---\nboard: b\nstatus: todo\ntitle: T\n---\nhello\n"
	if err := c.SaveDocument(ctx, "ws", "cards/x.md", card, ""); err != nil {
		t.Fatalf("save: %v", err)
	}

	docs, err := c.ListDocuments(ctx, "ws")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 1 || docs[0].Path != "cards/x.md" {
		t.Fatalf("list = %+v", docs)
	}

	doc, err := c.GetDocument(ctx, "ws", docs[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(doc.Content, "hello") || doc.ContentHash == "" {
		t.Fatalf("get = %+v", doc)
	}

	if err := c.AddComment(ctx, "ws", docs[0].ID, "run done: PR http://x"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if f.lastBody != "run done: PR http://x" {
		t.Fatalf("comment body = %q", f.lastBody)
	}
}

func TestClientMoveCardRoundtrip(t *testing.T) {
	f := newFakeJtype()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	ctx := context.Background()

	card := "---\nboard: b\nstatus: todo\ntitle: T\n---\nbody\n"
	_ = c.SaveDocument(ctx, "ws", "cards/m.md", card, "")
	docs, _ := c.ListDocuments(ctx, "ws")

	if err := c.MoveCard(ctx, "ws", docs[0].ID, "done"); err != nil {
		t.Fatalf("move: %v", err)
	}
	doc, _ := c.GetDocument(ctx, "ws", docs[0].ID)
	if got := ParseCard(doc.Content).Status; got != "done" {
		t.Fatalf("after move status = %q, want done", got)
	}
}

func TestClientTypedErrors(t *testing.T) {
	// A 404 surfaces as a typed *Error(StatusCode=404).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": "no doc"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	_, err := c.GetDocument(context.Background(), "ws", "missing")
	je, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T (%v)", err, err)
	}
	if je.StatusCode != 404 || je.Code != "not_found" {
		t.Fatalf("typed error = %+v", je)
	}
}

func TestClientSaveConflict(t *testing.T) {
	f := newFakeJtype()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	ctx := context.Background()

	_ = c.SaveDocument(ctx, "ws", "cards/c.md", "v1", "")
	docs, _ := c.ListDocuments(ctx, "ws")
	doc, _ := c.GetDocument(ctx, "ws", docs[0].ID)

	// Simulate a concurrent edit: save with a DIFFERENT content first (changes hash).
	_ = c.SaveDocument(ctx, "ws", "cards/c.md", "v2-concurrent", "")
	// Now save the stale snapshot with the OLD hash → expect 409 conflict.
	err := c.SaveDocument(ctx, "ws", "cards/c.md", "v1", doc.ContentHash)
	je, ok := err.(*Error)
	if !ok || je.StatusCode != 409 {
		t.Fatalf("want 409 *Error, got %T (%v)", err, err)
	}
}

func TestResolveDocIDByPath(t *testing.T) {
	f := newFakeJtype()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok", 0)
	ctx := context.Background()

	_ = c.SaveDocument(ctx, "ws", "boards/dev.board", `{"id":"dev","title":"d","columns":[{"key":"ai","name":"AI"}]}`, "")
	id, err := c.ResolveDocIDByPath(ctx, "ws", "boards/dev.board")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id == "" {
		t.Fatalf("empty id")
	}
	if _, err := c.ResolveDocIDByPath(ctx, "ws", "missing.md"); err != ErrDocNotFound {
		t.Fatalf("missing want ErrDocNotFound, got %v", err)
	}
	b, err := c.GetBoard(ctx, "ws", "dev")
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	if len(b.Columns) != 1 || b.Columns[0].Key != "ai" {
		t.Fatalf("board columns = %+v", b.Columns)
	}
}

// --- D31: ProxyDocumentAPI (raw verbatim passthrough for the board embed) -----

// ProxyDocumentAPI builds {base}{path}, sets Bearer + Accept, and returns the raw
// response (status + body) UNMODIFIED — including fields the typed Doc/Document
// structs drop (isPublished, versionId), which is exactly why it bypasses them.
func TestProxyDocumentAPI_ForwardsAndReturnsRaw(t *testing.T) {
	var gotAuth, gotAccept, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Fields the typed structs drop — must survive verbatim.
		_, _ = io.WriteString(w, `[{"id":"d1","relativePath":"a.board","isPublished":true,"versionId":"v9","updatedClock":7}]`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok-123", 0)
	resp, err := c.ProxyDocumentAPI(context.Background(), http.MethodGet, "/api/v1/workspaces/ws-1/documents", nil)
	if err != nil {
		t.Fatalf("ProxyDocumentAPI: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", resp.StatusCode)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/workspaces/ws-1/documents" {
		t.Fatalf("upstream got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q want Bearer tok-123", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if !strings.Contains(string(body), `"isPublished":true`) || !strings.Contains(string(body), `"versionId":"v9"`) {
		t.Fatalf("raw passthrough lost fields: %s", body)
	}
}

// A >=400 upstream status is returned as a normal response (not an error) for
// verbatim passthrough — the caller copies it through.
func TestProxyDocumentAPI_PassesThroughErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"conflict","message":"stale"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", 0)
	resp, err := c.ProxyDocumentAPI(context.Background(), http.MethodPost, "/api/v1/workspaces/ws/documents/save", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("a 409 must be a response, not an error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d want 409", resp.StatusCode)
	}
}

// The save path streams the request body through and sets Content-Type.
func TestProxyDocumentAPI_StreamsBody(t *testing.T) {
	var gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"mergeStatus":"clean","contentHash":"h1"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", 0)
	resp, err := c.ProxyDocumentAPI(context.Background(), http.MethodPost,
		"/api/v1/workspaces/ws/documents/save", strings.NewReader(`{"relativePath":"a.md","content":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotBody != `{"relativePath":"a.md","content":"x"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
}

// An empty token sends NO Authorization header (matches `do`) — the fail-visible
// path where the caller failed to resolve a credential; jtype then 401s.
func TestProxyDocumentAPI_EmptyTokenNoAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", 0)
	resp, err := c.ProxyDocumentAPI(context.Background(), http.MethodGet, "/api/v1/workspaces/ws/documents", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hadAuth {
		t.Fatal("empty token must not set an Authorization header")
	}
}
