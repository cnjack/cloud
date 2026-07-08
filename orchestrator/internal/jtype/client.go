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
// relative path matches path, or ErrDocNotFound. Used by the admin API to
// validate a board ref (boards/<ref>.board) against the live workspace.
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

// GetBoard fetches and parses a board (`.board` JSON document) by its ref. The
// board document lives at relative path "boards/<ref>.board". Used by the admin
// API to validate trigger/done column names against the board's actual columns.
func (c *Client) GetBoard(ctx context.Context, workspace, boardRef string) (*Board, error) {
	id, err := c.ResolveDocIDByPath(ctx, workspace, "boards/"+boardRef+".board")
	if err != nil {
		return nil, err
	}
	doc, err := c.GetDocument(ctx, workspace, id)
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
	if err := json.Unmarshal([]byte(doc.Content), &raw); err != nil {
		return nil, fmt.Errorf("parse board %s: %w", boardRef, err)
	}
	b := &Board{ID: raw.ID, Title: raw.Title}
	for _, col := range raw.Columns {
		b.Columns = append(b.Columns, BoardColumn{Key: col.Key, Name: col.Name})
	}
	return b, nil
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

// ErrDocNotFound is returned by ResolveDocIDByPath when no document matches.
var ErrDocNotFound = fmt.Errorf("jtype: document not found")

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
