package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubListInstallationReposUsesInstallationEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/installation/repositories" {
			t.Fatalf("path = %q, want /installation/repositories", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer installation-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "50" {
			t.Fatalf("per_page = %q", got)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Fatalf("page = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"repositories": [
				{"id": 77, "full_name": "cnjack/codespace_demo", "description": "E2E", "default_branch": "main", "private": false, "html_url": "https://github.com/cnjack/codespace_demo"},
				{"id": 88, "full_name": "cnjack/other", "default_branch": "main", "private": true}
			]
		}`))
	}))
	defer server.Close()

	client, err := NewGitHubClient(server.URL, "installation-token")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := client.ListInstallationRepos(context.Background(), "space", 2, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %#v", repos)
	}
	if got := repos[0]; got.ID != 77 || got.FullName != "cnjack/codespace_demo" ||
		got.DefaultBranch != "main" || got.Private || got.HTMLURL == "" {
		t.Fatalf("repo = %#v", got)
	}
}
