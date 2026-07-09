package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGitHubEnsureCommentWebhook proves the github hook registration is
// idempotent-by-URL and posts the issue_comment hook shape (F13): the first call
// (no existing hooks) POSTs a hook carrying events=[issue_comment], name=web, and
// the secret in config; a second call with a hook already at the same target URL
// makes NO POST.
func TestGitHubEnsureCommentWebhook(t *testing.T) {
	const hookURL = "http://orch.test/webhooks/github"
	var existing []map[string]any
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/hooks":
			_ = json.NewEncoder(w).Encode(existing)
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/hooks":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = append(posted, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewGitHubClient(srv.URL, "ghtok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// First call: no hooks yet → one POST.
	if err := c.EnsureCommentWebhook(ctx, "o", "r", hookURL, "s3cr3t"); err != nil {
		t.Fatalf("ensure(1): %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("posted %d hooks, want 1", len(posted))
	}
	got := posted[0]
	if got["name"] != "web" {
		t.Errorf("hook name=%v want web", got["name"])
	}
	evs, _ := got["events"].([]any)
	if len(evs) != 1 || evs[0] != "issue_comment" {
		t.Errorf("events=%v want [issue_comment]", got["events"])
	}
	cfg, _ := got["config"].(map[string]any)
	if cfg["url"] != hookURL || cfg["secret"] != "s3cr3t" || cfg["content_type"] != "json" {
		t.Errorf("config=%v", cfg)
	}

	// Second call: a hook already exists at hookURL → NO new POST (idempotent).
	existing = []map[string]any{{"config": map[string]any{"url": hookURL}}}
	if err := c.EnsureCommentWebhook(ctx, "o", "r", hookURL, "s3cr3t"); err != nil {
		t.Fatalf("ensure(2): %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("idempotent call posted again: total=%d want 1", len(posted))
	}
}

// TestGitLabEnsureCommentWebhook proves the gitlab hook registration is
// idempotent-by-URL and posts the note-hook shape (F13): note_events=true and the
// secret in `token`; a hook already at the same URL suppresses the POST. The
// project id segment is url-encoded ("o%2Fr").
func TestGitLabEnsureCommentWebhook(t *testing.T) {
	const hookURL = "http://orch.test/webhooks/gitlab"
	var existing []map[string]any
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath() // keeps %2F in the project id segment
		switch {
		case r.Method == "GET" && path == "/projects/o%2Fr/hooks":
			_ = json.NewEncoder(w).Encode(existing)
		case r.Method == "POST" && path == "/projects/o%2Fr/hooks":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = append(posted, body)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewGitLabClient(srv.URL, "gltok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := c.EnsureCommentWebhook(ctx, "o", "r", hookURL, "s3cr3t"); err != nil {
		t.Fatalf("ensure(1): %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("posted %d hooks, want 1", len(posted))
	}
	got := posted[0]
	if got["url"] != hookURL || got["token"] != "s3cr3t" {
		t.Errorf("hook url/token = %v/%v", got["url"], got["token"])
	}
	if ne, _ := got["note_events"].(bool); !ne {
		t.Errorf("note_events=%v want true", got["note_events"])
	}

	// Idempotent: a hook already at hookURL → no new POST.
	existing = []map[string]any{{"url": hookURL}}
	if err := c.EnsureCommentWebhook(ctx, "o", "r", hookURL, "s3cr3t"); err != nil {
		t.Fatalf("ensure(2): %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("idempotent call posted again: total=%d want 1", len(posted))
	}
}
