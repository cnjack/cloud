package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestGitLabSCMWebhookReconcilesAndDeletes(t *testing.T) {
	var mu sync.Mutex
	hooked, created, deleted := false, 0, 0
	var body map[string]any
	var authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		mu.Lock()
		defer mu.Unlock()
		authorization = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && path == "/projects/o%2Fr/hooks":
			if !hooked {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "url": "https://cloud.test/webhooks/gitlab", "push_events": true, "merge_requests_events": true, "note_events": true, "issues_events": true, "pipeline_events": true, "tag_push_events": true, "releases_events": true}})
		case r.Method == http.MethodPost && path == "/projects/o%2Fr/hooks":
			_ = json.NewDecoder(r.Body).Decode(&body)
			hooked, created = true, created+1
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && path == "/projects/o%2Fr/hooks/7":
			hooked, deleted = false, deleted+1
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, path, http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	c, err := NewGitLabClient(upstream.URL, "installation-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.EnsureSCMWebhook(ctx, "o", "r", "https://cloud.test/webhooks/gitlab", "secret"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if created != 1 || authorization != "Bearer installation-token" || body["token"] != "secret" || body["push_events"] != true || body["merge_requests_events"] != true || body["issues_events"] != true || body["pipeline_events"] != true || body["tag_push_events"] != true || body["releases_events"] != true {
		t.Fatalf("created=%d authorization=%q body=%v", created, authorization, body)
	}
	mu.Unlock()
	// An already-complete hook does not create a duplicate.
	if err := c.EnsureSCMWebhook(ctx, "o", "r", "https://cloud.test/webhooks/gitlab", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSCMWebhook(ctx, "o", "r", "https://cloud.test/webhooks/gitlab"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 1 || deleted != 1 || hooked {
		t.Fatalf("created=%d deleted=%d hooked=%v", created, deleted, hooked)
	}
}

func TestGiteaSCMWebhookReconcilesAndDeletes(t *testing.T) {
	var mu sync.Mutex
	hooked, created, deleted := false, 0, 0
	var body map[string]any
	var authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authorization = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/o/r/hooks":
			if !hooked {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 8, "active": true, "events": []string{"push", "pull_request", "pull_request_sync", "pull_request_review", "issues", "issue_comment", "pull_request_comment", "status", "create", "delete", "release"}, "config": map[string]string{"url": "https://cloud.test/webhooks/gitea"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/o/r/hooks":
			_ = json.NewDecoder(r.Body).Decode(&body)
			hooked, created = true, created+1
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/repos/o/r/hooks/8":
			hooked, deleted = false, deleted+1
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.URL.Path, http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	c, err := NewGiteaClientWithScheme(upstream.URL, "installation-token", "Bearer")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.EnsureSCMWebhook(ctx, "o", "r", "https://cloud.test/webhooks/gitea", "secret"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	events, _ := body["events"].([]any)
	if created != 1 || authorization != "Bearer installation-token" || len(events) != 11 {
		t.Fatalf("created=%d authorization=%q events=%v body=%v", created, authorization, events, body)
	}
	mu.Unlock()
	if err := c.DeleteSCMWebhook(ctx, "o", "r", "https://cloud.test/webhooks/gitea"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deleted != 1 || hooked {
		t.Fatalf("deleted=%d hooked=%v", deleted, hooked)
	}
}
