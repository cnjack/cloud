package jtype

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the jtype document REST API (Authorization: Bearer <PAT>).
// It is the single seam the kanban poller + writeback use; one mcp-scope PAT
// authorises every read/write across all workspaces on the instance.
type Client struct {
	baseURL string // e.g. http://127.0.0.1:13345 (no trailing slash)
	token   string
	http    *http.Client
}

// NewClient builds a Client. baseURL is trimmed of trailing slashes; an empty
// token yields a client whose calls fail with a typed error (fail-visible) —
// callers should disable the integration at config time instead.
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

// Doc is a workspace document list item (the fields the poller needs).
type Doc struct {
	ID           string
	Path         string // relativePath (e.g. "cards/add-health-banner.md")
	UpdatedClock int64
	Title        string
}

// Document is a single document's full content + metadata (GET /documents/{id}).
type Document struct {
	Path         string
	Title        string
	Content      string // raw .md body including frontmatter
	ContentHash  string // server content hash; pass back to SaveDocument for safe write
	UpdatedClock int64
}

// Board is a parsed `.board` document: its columns are the valid `status`
// values a card on this board may carry.
type Board struct {
	ID      string
	Title   string
	Columns []BoardColumn
}

// BoardColumn is one column of a board (Key is the frontmatter status value).
type BoardColumn struct {
	Key  string
	Name string
}

// ListDocuments returns every document in the workspace (the poller filters to
// `.md` cards by frontmatter board). 4xx/5xx return a typed *Error.
func (c *Client) ListDocuments(ctx context.Context, workspace string) ([]Doc, error) {
	var raw []struct {
		ID           string `json:"id"`
		RelativePath string `json:"relativePath"`
		Title        string `json:"title"`
		UpdatedClock int64  `json:"updatedClock"`
	}
	if err := c.getJSON(ctx, c.path("/api/v1/workspaces/%s/documents", workspace), &raw); err != nil {
		return nil, err
	}
	out := make([]Doc, 0, len(raw))
	for _, r := range raw {
		out = append(out, Doc{ID: r.ID, Path: r.RelativePath, Title: r.Title, UpdatedClock: r.UpdatedClock})
	}
	return out, nil
}

// GetDocument fetches a single document's full content (frontmatter + body).
func (c *Client) GetDocument(ctx context.Context, workspace, id string) (*Document, error) {
	var raw struct {
		RelativePath string `json:"relativePath"`
		Title        string `json:"title"`
		Content      string `json:"content"`
		ContentHash  string `json:"contentHash"`
		UpdatedClock int64  `json:"updatedClock"`
	}
	if err := c.getJSON(ctx, c.path("/api/v1/workspaces/%s/documents/%s", workspace, id), &raw); err != nil {
		return nil, err
	}
	return &Document{
		Path: raw.RelativePath, Title: raw.Title, Content: raw.Content,
		ContentHash: raw.ContentHash, UpdatedClock: raw.UpdatedClock,
	}, nil
}

// SaveDocument writes a document by relative path (POST .../documents/save).
// baseContentHash, when non-empty, is the hash GetDocument returned; the server
// uses it to detect a concurrent edit and replies 409 (which SaveDocument
// surfaces as a typed *Error the caller can retry next tick). An empty hash
// means "write unconditionally" (last-write-wins).
func (c *Client) SaveDocument(ctx context.Context, workspace, path, content, baseContentHash string) error {
	body := map[string]any{"relativePath": path, "content": content}
	if baseContentHash != "" {
		body["baseContentHash"] = baseContentHash
	}
	resp, err := c.do(ctx, http.MethodPost, c.path("/api/v1/workspaces/%s/documents/save", workspace), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// AddComment posts a comment on a document (POST .../documents/{id}/comments).
// Used by the writeback pass to attach PR/console links to a card, and by the
// poller's "LLM not configured" notice.
func (c *Client) AddComment(ctx context.Context, workspace, docID, body string) error {
	resp, err := c.do(ctx, http.MethodPost,
		c.path("/api/v1/workspaces/%s/documents/%s/comments", workspace, docID),
		map[string]any{"body": body})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// MoveCard reads a card, rewrites its frontmatter status to newStatus, and
// saves it (a column move in jtype). It passes the fetched content hash so a
// concurrent edit surfaces as a retryable 409 rather than clobbering the card.
// workspace + docID identify the card; the path is resolved from the fetch.
func (c *Client) MoveCard(ctx context.Context, workspace, docID, newStatus string) error {
	doc, err := c.GetDocument(ctx, workspace, docID)
	if err != nil {
		return fmt.Errorf("move card: load: %w", err)
	}
	updated := SetStatus(doc.Content, newStatus)
	if err := c.SaveDocument(ctx, workspace, doc.Path, updated, doc.ContentHash); err != nil {
		return fmt.Errorf("move card: save: %w", err)
	}
	return nil
}

// ResolveDocIDByPath lists the workspace and returns the document id whose
// relative path matches path, or ErrDocNotFound. Kept for the rig smoke tests;
// GetBoard resolves by name (resolveBoardDoc) rather than by an exact path.
func (c *Client) ResolveDocIDByPath(ctx context.Context, workspace, path string) (string, error) {
	docs, err := c.ListDocuments(ctx, workspace)
	if err != nil {
		return "", err
	}
	for _, d := range docs {
		if d.Path == path {
			return d.ID, nil
		}
	}
	return "", ErrDocNotFound
}

// resolveBoardDoc resolves a host-supplied boardRef to a workspace `.board`
// document. It mirrors jtype-board-react's resolveBoard.ts exactly so the console
// (which only knows a friendly name or a relative path) and the board embed agree
// on which document a name selects. Resolution order:
//
//  1. normalize ref: trim, strip a leading "./" or "/"; empty => ErrDocNotFound.
//  2. wanted = ref (+ ".board" if absent), compared case-insensitively.
//  3. candidate set = docs whose relativePath ends ".board" (case-insensitive).
//  4. exact wins: a doc whose lowercased relativePath == ref or == wanted.
//  5. unique basename: docs whose lowercased relativePath ends "/wanted", or (for
//     a root board with no slash) equals wanted. Exactly one => use it.
//  6. >1 basename matches => *ErrBoardAmbiguousError (candidate paths listed).
//  7. zero => ErrDocNotFound.
//
// Pure (no HTTP) so it is unit-tested directly, matching board-react's split.
func resolveBoardDoc(docs []Doc, boardRef string) (Doc, error) {
	ref := strings.TrimSpace(boardRef)
	ref = strings.TrimPrefix(ref, "./")
	ref = strings.TrimPrefix(ref, "/")
	if ref == "" {
		return Doc{}, ErrDocNotFound
	}
	refLower := strings.ToLower(ref)
	wanted := refLower
	if !strings.HasSuffix(wanted, ".board") {
		wanted += ".board"
	}

	boards := make([]Doc, 0, len(docs))
	for _, d := range docs {
		if strings.HasSuffix(strings.ToLower(d.Path), ".board") {
			boards = append(boards, d)
		}
	}

	for _, d := range boards {
		p := strings.ToLower(d.Path)
		if p == refLower || p == wanted {
			return d, nil
		}
	}

	var byName []Doc
	for _, d := range boards {
		p := strings.ToLower(d.Path)
		// Basename match in any folder ("/wanted"), or a root-level board whose whole
		// path IS the basename (no slash — same effect as board-react's `/${wanted}`
		// once you account for a top-level board).
		if strings.HasSuffix(p, "/"+wanted) || (!strings.Contains(p, "/") && p == wanted) {
			byName = append(byName, d)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		cands := make([]string, len(byName))
		for i, d := range byName {
			cands[i] = d.Path
		}
		return Doc{}, &ErrBoardAmbiguousError{Ref: boardRef, Candidates: cands}
	}
	return Doc{}, ErrDocNotFound
}

// GetBoard fetches and parses a board (`.board` JSON document) by NAME at any
// path in the workspace. A real jtype board lives at the workspace root (or any
// folder) as `<name>.board`; cards carry the board's config `id` (a random
// `b_xxxxxxxx`) in their frontmatter `board`. GetBoard returns that id in
// Board.ID — the canonical value a kanban link stores as its BoardRef so the
// poller's `card.Board == link.BoardRef` match succeeds. Used by the admin API to
// validate trigger/done column names against the board's actual columns.
//
// Errors: no matching `.board` => ErrDocNotFound; a bare name matching more than
// one board => *ErrBoardAmbiguousError (candidates listed); a jtype 4xx/5xx =>
// *Error (so callers distinguish an auth/workspace error from a missing board).
func (c *Client) GetBoard(ctx context.Context, workspace, boardRef string) (*Board, error) {
	docs, err := c.ListDocuments(ctx, workspace)
	if err != nil {
		return nil, err
	}
	doc, err := resolveBoardDoc(docs, boardRef)
	if err != nil {
		return nil, err
	}
	return c.GetBoardByDoc(ctx, workspace, doc.ID)
}

// GetBoardByDoc fetches and parses a `.board` document by its KNOWN document id,
// skipping the workspace re-listing that GetBoard does to resolve a ref. The
// discovery endpoint uses this: it already has every board doc's id from a single
// ListDocuments, so listing N boards costs 1 + N GetDocument round-trips, not the
// 1 + N*(ListDocuments+GetDocument) an N-way GetBoard fan-out would.
func (c *Client) GetBoardByDoc(ctx context.Context, workspace, docID string) (*Board, error) {
	full, err := c.GetDocument(ctx, workspace, docID)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Columns []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"columns"`
	}
	if err := json.Unmarshal([]byte(full.Content), &raw); err != nil {
		return nil, fmt.Errorf("parse board %s: %w", docID, err)
	}
	b := &Board{ID: raw.ID, Title: raw.Title}
	for _, col := range raw.Columns {
		b.Columns = append(b.Columns, BoardColumn{Key: col.Key, Name: col.Name})
	}
	return b, nil
}

// Workspace is a caller-visible jtype workspace (the fields the console picker
// needs). The token is never part of this shape.
type Workspace struct {
	ID   string
	Name string
}

// ListWorkspaces returns the caller's workspaces (GET /api/v1/workspaces). jtype
// returns [{id, name|title}]; either label field is tolerated. Used by the
// owner-only discovery endpoint that backs the console's workspace picker. 4xx/5xx
// return a typed *Error (fail-visible; never an empty list masking an auth error).
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	// jtype wraps this endpoint: {"workspaces":[{id,name,slug,role,…}]} — unlike
	// /documents, which is a bare array. Decode the wrapper (a bare-array decode
	// fails with "cannot unmarshal object into []struct").
	var raw struct {
		Workspaces []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"workspaces"`
	}
	if err := c.getJSON(ctx, c.baseURL+"/api/v1/workspaces", &raw); err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(raw.Workspaces))
	for _, w := range raw.Workspaces {
		name := w.Name
		if name == "" {
			name = w.Title
		}
		out = append(out, Workspace{ID: w.ID, Name: name})
	}
	return out, nil
}

// ProxyDocumentAPI issues an authenticated request to a jtype document-API path
// and returns the RAW http.Response for the caller to copy through unmodified.
// The board embed proxy (D31) needs jtype's EXACT wire shapes — including fields
// the typed Doc/Document structs deliberately drop (isPublished, versionId, a
// save's mergeStatus) — so re-serializing through those structs would be a
// silent field-dropping degradation (CLAUDE.md red line #1). This bypasses them.
//
// `path` MUST be built by the caller from validated components only (never a raw
// client-controlled string); it is appended to baseURL as-is. The bound token is
// applied as the Authorization Bearer header exactly as `do` does (empty token =>
// no header) and is therefore NEVER part of the returned body. `body`, when
// non-nil, is streamed as the request body (Content-Type: application/json).
//
// The caller owns resp.Body (must Close it). A >=400 status is returned as a
// normal response for verbatim passthrough — only a transport/build failure is an
// error here.
func (c *Client) ProxyDocumentAPI(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("jtype: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// --- errors -----------------------------------------------------------------

// Error is a typed jtype API error carrying the HTTP status and a short code,
// so callers can distinguish a 404 (board/card gone) from a 409 (concurrent
// edit) from a 5xx (jtype down). Fail-visible: jtype failures are never masked
// as "no card".
type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("jtype: %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// ErrDocNotFound is returned when no document matches (ResolveDocIDByPath, or
// GetBoard when no `.board` document resolves the ref).
var ErrDocNotFound = fmt.Errorf("jtype: document not found")

// ErrBoardAmbiguousError is returned by GetBoard when a bare board name matches
// more than one `.board` document (the same basename in different folders).
// Candidates lists the matching relativePaths so the caller can tell the user to
// pass a full path. Callers use errors.As(err, *ErrBoardAmbiguousError).
type ErrBoardAmbiguousError struct {
	Ref        string
	Candidates []string
}

func (e *ErrBoardAmbiguousError) Error() string {
	return fmt.Sprintf("jtype: board %q is ambiguous (%d matches: %s)",
		e.Ref, len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// readError turns a >=400 response body into a typed *Error. jtype's error
// envelope is {"error":"<code>","message":"…"} (or {"error":{"code","message"}});
// we tolerate either and fall back to the status text.
func readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	code, msg := parseErrorBody(body)
	if code == "" {
		code = statusErrorCode(resp.StatusCode)
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &Error{StatusCode: resp.StatusCode, Code: code, Message: msg}
}

func parseErrorBody(body []byte) (code, msg string) {
	var loose struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &loose) == nil && loose.Error != "" {
		// {"error":"conflict","message":"…"} or {"error":"<msg>"}.
		code = loose.Error
		msg = loose.Message
		// If `error` looks like a sentence rather than a code, treat it as msg.
		if strings.Contains(code, " ") && msg == "" {
			msg, code = code, statusWord(code)
		}
		return code, msg
	}
	var nested struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &nested) == nil {
		return nested.Error.Code, nested.Error.Message
	}
	return "", ""
}

func statusErrorCode(code int) string {
	switch code {
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "unauthorized"
	}
	return "error"
}

// statusWord derives a short code from a sentence (last resort).
func statusWord(s string) string {
	for _, suffix := range []string{".", "!", "?"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.ReplaceAll(strings.ToLower(strings.Fields(s)[0:1][0]), " ", "_")
}

// --- transport --------------------------------------------------------------

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("jtype: marshal body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("jtype: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// path joins baseURL + a format-produced segment, URL-encoding path arguments.
func (c *Client) path(format string, args ...string) string {
	enc := make([]any, len(args))
	for i, a := range args {
		enc[i] = url.PathEscape(a)
	}
	return c.baseURL + fmt.Sprintf(format, enc...)
}
