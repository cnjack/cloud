package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitRepo(t *testing.T) {
	cases := []struct {
		in          string
		owner, name string
		ok          bool
	}{
		{"jcloud/seed", "jcloud", "seed", true},
		{"jcloud/seed.git", "jcloud", "seed", true},
		{"  jcloud/seed  ", "jcloud", "seed", true},
		{"noslash", "", "", false},
		{"/leading", "", "", false},
		{"trailing/", "", "", false},
	}
	for _, tc := range cases {
		o, n, ok := SplitRepo(tc.in)
		if o != tc.owner || n != tc.name || ok != tc.ok {
			t.Errorf("SplitRepo(%q) = (%q,%q,%v) want (%q,%q,%v)", tc.in, o, n, ok, tc.owner, tc.name, tc.ok)
		}
	}
}

func TestNewGiteaClientNotConfigured(t *testing.T) {
	if _, err := NewGiteaClient("", "tok"); err != ErrNotConfigured {
		t.Errorf("empty url: err=%v want ErrNotConfigured", err)
	}
	if _, err := NewGiteaClient("http://x", ""); err != ErrNotConfigured {
		t.Errorf("empty token: err=%v want ErrNotConfigured", err)
	}
	if _, err := NewGiteaClient("http://x", "tok"); err != nil {
		t.Errorf("configured: unexpected err=%v", err)
	}
}

// TestGiteaCreateDraftPR verifies the create call hits the right path, carries
// the WIP prefix (draft), auths with the token, and parses the response.
func TestGiteaCreateDraftPR(t *testing.T) {
	var gotAuth, gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "http://gitea.test/jcloud/seed/pulls/42",
		})
	}))
	defer srv.Close()

	c, err := NewGiteaClient(srv.URL, "tok-abc")
	if err != nil {
		t.Fatal(err)
	}
	pr, err := c.CreateDraftPR(context.Background(), CreateDraftPRInput{
		Owner: "jcloud", Repo: "seed", Head: "agent/run-1", Base: "main",
		Title: "[jcode] add hello", Body: "linking run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 || pr.URL != "http://gitea.test/jcloud/seed/pulls/42" {
		t.Fatalf("pr = %+v", pr)
	}
	if gotAuth != "token tok-abc" {
		t.Errorf("auth header = %q want 'token tok-abc'", gotAuth)
	}
	if gotPath != "/api/v1/repos/jcloud/seed/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	// Draft is signalled via the WIP title prefix.
	title, _ := body["title"].(string)
	if !strings.HasPrefix(title, GiteaWIPPrefix) {
		t.Errorf("title = %q want WIP prefix (draft)", title)
	}
	if body["head"] != "agent/run-1" || body["base"] != "main" {
		t.Errorf("head/base wrong: %v / %v", body["head"], body["base"])
	}
}

// TestGiteaFindOpenPRByHead verifies the list call matches by head ref.
func TestGiteaFindOpenPRByHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"number":3,"html_url":"http://gitea.test/o/r/pulls/3","state":"open","head":{"ref":"other"}},
			{"number":9,"html_url":"http://gitea.test/o/r/pulls/9","state":"open","head":{"ref":"agent/run-9"}}
		]`))
	}))
	defer srv.Close()

	c, _ := NewGiteaClient(srv.URL, "tok")
	pr, err := c.FindOpenPRByHead(context.Background(), "o", "r", "agent/run-9")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 9 {
		t.Fatalf("find = %+v want #9", pr)
	}
	// A head with no match returns nil, nil.
	none, err := c.FindOpenPRByHead(context.Background(), "o", "r", "agent/run-none")
	if err != nil || none != nil {
		t.Fatalf("no-match = (%+v,%v) want (nil,nil)", none, err)
	}
}

// TestGiteaCreateErrorSurfacesStatus verifies a non-2xx becomes an error.
func TestGiteaCreateErrorSurfacesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"head already exists"}`))
	}))
	defer srv.Close()
	c, _ := NewGiteaClient(srv.URL, "tok")
	_, err := c.CreateDraftPR(context.Background(), CreateDraftPRInput{Owner: "o", Repo: "r", Head: "h", Base: "main", Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v want a 422 error", err)
	}
}

// TestFakeProviderIdempotencySeam sanity-checks the fake used by reconciler tests.
func TestFakeProviderIdempotencySeam(t *testing.T) {
	f := NewFakeProvider()
	ctx := context.Background()
	// No PR yet.
	if pr, _ := f.FindOpenPRByHead(ctx, "o", "r", "h"); pr != nil {
		t.Fatal("unexpected pre-existing PR")
	}
	pr, err := f.CreateDraftPR(ctx, CreateDraftPRInput{Owner: "o", Repo: "r", Head: "h", Base: "main", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// Now it is findable.
	found, _ := f.FindOpenPRByHead(ctx, "o", "r", "h")
	if found == nil || found.Number != pr.Number {
		t.Fatalf("find after create = %+v want %+v", found, pr)
	}
}
