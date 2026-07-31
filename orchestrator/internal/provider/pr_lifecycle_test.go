package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFindOpenPRByHeadRejectsAmbiguousRemoteState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":1,"head":{"ref":"same"}},{"number":2,"head":{"ref":"same"}}]`))
	}))
	defer srv.Close()
	c, _ := NewGitHubClient(srv.URL, "token")
	if _, err := c.FindOpenPRByHead(context.Background(), "o", "r", "same"); !errors.Is(err, ErrMultipleOpenPRs) {
		t.Fatalf("err=%v want ErrMultipleOpenPRs", err)
	}
}

func TestGitHubCreateReadyAndMarkDraftReady(t *testing.T) {
	var created map[string]any
	var mutation map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://gh/p/7","state":"open","draft":false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/8":
			_, _ = w.Write([]byte(`{"number":8,"node_id":"PR_node","html_url":"https://gh/p/8","state":"open","draft":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			_ = json.NewDecoder(r.Body).Decode(&mutation)
			_, _ = w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := NewGitHubClient(srv.URL, "token")
	pr, err := c.CreatePR(context.Background(), CreatePRInput{Owner: "o", Repo: "r", Head: "h", Base: "main", Title: "t"})
	if err != nil || pr.Draft || created["draft"] != false {
		t.Fatalf("ready create pr=%+v body=%+v err=%v", pr, created, err)
	}
	pr, err = c.MarkPRReady(context.Background(), "o", "r", 8)
	if err != nil || pr.Draft || mutation["query"] == nil {
		t.Fatalf("mark ready pr=%+v mutation=%+v err=%v", pr, mutation, err)
	}
}

func TestGiteaCreateReadyAndStripOnlyCloudWIPPrefix(t *testing.T) {
	var created, updated map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/o/r/pulls":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://g/p/7","state":"open"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/o/r/pulls/8":
			_, _ = w.Write([]byte(`{"number":8,"html_url":"https://g/p/8","state":"open","title":"WIP: [jcode] task"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/repos/o/r/pulls/8":
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_, _ = w.Write([]byte(`{"number":8,"html_url":"https://g/p/8","state":"open","title":"[jcode] task"}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := NewGiteaClient(srv.URL, "token")
	if _, err := c.CreatePR(context.Background(), CreatePRInput{Owner: "o", Repo: "r", Head: "h", Base: "main", Title: "[jcode] task"}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(created["title"].(string), GiteaWIPPrefix) {
		t.Fatalf("ready create kept WIP: %+v", created)
	}
	pr, err := c.MarkPRReady(context.Background(), "o", "r", 8)
	if err != nil || pr.Draft || updated["title"] != "[jcode] task" {
		t.Fatalf("mark ready pr=%+v update=%+v err=%v", pr, updated, err)
	}
}

func TestGitLabCreateReadyAndStripDraftPrefix(t *testing.T) {
	var created, updated map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodPost && path == "/projects/o%2Fr/merge_requests":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"iid":7,"web_url":"https://gl/m/7","state":"opened"}`))
		case r.Method == http.MethodGet && path == "/projects/o%2Fr/merge_requests/8":
			_, _ = w.Write([]byte(`{"iid":8,"web_url":"https://gl/m/8","state":"opened","draft":true,"title":"Draft: [jcode] task"}`))
		case r.Method == http.MethodPut && path == "/projects/o%2Fr/merge_requests/8":
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_, _ = w.Write([]byte(`{"iid":8,"web_url":"https://gl/m/8","state":"opened","draft":false,"title":"[jcode] task"}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := NewGitLabClient(srv.URL, "token")
	if _, err := c.CreatePR(context.Background(), CreatePRInput{Owner: "o", Repo: "r", Head: "h", Base: "main", Title: "[jcode] task"}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(created["title"].(string), GitLabDraftPrefix) {
		t.Fatalf("ready create kept Draft: %+v", created)
	}
	pr, err := c.MarkPRReady(context.Background(), "o", "r", 8)
	if err != nil || pr.Draft || updated["title"] != "[jcode] task" {
		t.Fatalf("mark ready pr=%+v update=%+v err=%v", pr, updated, err)
	}
}

func TestGitLabMarkReadyVerifiesProviderState(t *testing.T) {
	var updated map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"iid":9,"web_url":"https://gl/m/9","state":"opened","draft":true,"title":"[Draft] task"}`))
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&updated)
			// Provider truth wins: a successful HTTP response that remains Draft
			// must never be persisted locally as Ready.
			_, _ = w.Write([]byte(`{"iid":9,"web_url":"https://gl/m/9","state":"opened","draft":true,"title":"[Draft] task"}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := NewGitLabClient(srv.URL, "token")
	pr, err := c.MarkPRReady(context.Background(), "o", "r", 9)
	if pr != nil || !errors.Is(err, ErrUnsupportedPRTransition) {
		t.Fatalf("pr=%+v err=%v want verified provider_unsupported", pr, err)
	}
	if updated["title"] != "task" {
		t.Fatalf("recognized [Draft] marker was not stripped: %+v", updated)
	}
}
