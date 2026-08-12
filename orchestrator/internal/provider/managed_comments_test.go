package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubManagedIssueComments(t *testing.T) {
	t.Parallel()

	const escapedRepo = "/repos/acme%20team/web%2Fapi"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == escapedRepo+"/issues/7/comments":
			assertCommentBody(t, r, "processing")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":9007199254740993,"html_url":"https://github.example/comment/9007199254740993","body":"processing","user":{"id":9001,"login":"jcode-bot"},"performed_via_github_app":{"id":12345}}`))
		case r.Method == http.MethodPatch && r.URL.EscapedPath() == escapedRepo+"/issues/comments/9007199254740993":
			assertCommentBody(t, r, "complete")
			_, _ = w.Write([]byte(`{"id":9007199254740993,"html_url":"https://github.example/comment/9007199254740993","body":"complete","user":{"id":9001,"login":"jcode-bot"},"performed_via_github_app":{"id":12345}}`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == escapedRepo+"/issues/7/comments":
			if got := r.URL.Query().Get("per_page"); got != "2" {
				t.Fatalf("per_page = %q, want 2", got)
			}
			switch page := r.URL.Query().Get("page"); page {
			case "1":
				w.Header().Set("Link", fmt.Sprintf(`<%s%s/issues/7/comments?per_page=2&page=3>; rel="last"`, server.URL, escapedRepo))
				_, _ = w.Write([]byte(`[{"id":1,"body":"oldest","user":{"login":"jcode-bot"}},{"id":2,"body":"old","user":{"login":"jcode-bot"}}]`))
			case "2":
				_, _ = w.Write([]byte(`[{"id":3,"body":"middle","user":{"login":"jcode-bot"}},{"id":4,"body":"newer","user":{"login":"jcode-bot"}}]`))
			case "3":
				_, _ = w.Write([]byte(`[{"id":5,"body":"newest","user":{"login":"jcode-bot"}}]`))
			default:
				t.Fatalf("page = %q, want 1, 2, or 3", page)
			}
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewGitHubClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := client.CreateManagedIssueComment(ctx, "acme team", "web/api", 7, "processing")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, created, "9007199254740993", "https://github.example/comment/9007199254740993", "processing")
	if created.AuthorID != "9001" || created.AuthorLogin != "jcode-bot" || created.AuthorAppID != "12345" {
		t.Fatalf("created author identity=%#v", created)
	}

	updated, err := client.UpdateIssueComment(ctx, "acme team", "web/api", 7, created.ID, "complete")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, updated, created.ID, created.URL, "complete")

	comments, err := client.ListIssueComments(ctx, "acme team", "web/api", 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertIssueCommentsNewestFirst(t, comments, "5", "4")
}

func TestGiteaManagedIssueComments(t *testing.T) {
	t.Parallel()

	const escapedRepo = "/api/v1/repos/acme%20team/web%2Fapi"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == escapedRepo+"/issues/7/comments":
			assertCommentBody(t, r, "processing")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":41,"html_url":"https://gitea.example/comment/41","body":"processing","user":{"id":9001,"login":"jcode-bot"}}`))
		case r.Method == http.MethodPatch && r.URL.EscapedPath() == escapedRepo+"/issues/comments/41":
			assertCommentBody(t, r, "complete")
			_, _ = w.Write([]byte(`{"id":41,"html_url":"https://gitea.example/comment/41","body":"complete","user":{"id":9001,"login":"jcode-bot"}}`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == escapedRepo+"/issues/7/comments":
			_, _ = w.Write([]byte(`[{"id":1,"body":"oldest","user":{"id":9001,"login":"jcode-bot"}},{"id":4,"body":"newer","user":{"id":9001,"login":"jcode-bot"}},{"id":3,"body":"middle","user":{"id":9001,"login":"jcode-bot"}},{"id":5,"body":"newest","user":{"id":9001,"login":"jcode-bot"}}]`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v1/user":
			_, _ = w.Write([]byte(`{"id":9001,"login":"jcode-bot"}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewGiteaClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := client.CreateManagedIssueComment(ctx, "acme team", "web/api", 7, "processing")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, created, "41", "https://gitea.example/comment/41", "processing")
	if created.AuthorID != "9001" || created.AuthorLogin != "jcode-bot" || created.AuthorAppID != "" {
		t.Fatalf("created author identity=%#v", created)
	}

	updated, err := client.UpdateIssueComment(ctx, "acme team", "web/api", 7, created.ID, "complete")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, updated, created.ID, created.URL, "complete")

	comments, err := client.ListIssueComments(ctx, "acme team", "web/api", 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertIssueCommentsNewestFirst(t, comments, "5", "4")
	identity, err := client.CurrentUserIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "9001" || identity.Login != "jcode-bot" || identity.AppID != "" {
		t.Fatalf("current identity=%#v", identity)
	}
}

func TestGitLabManagedIssueComments(t *testing.T) {
	t.Parallel()

	const notesPath = "/projects/acme%20team%2Fweb%2Fapi/merge_requests/7/notes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == notesPath:
			assertCommentBody(t, r, "processing")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":51,"body":"processing","author":{"id":9001,"username":"jcode-bot"}}`))
		case r.Method == http.MethodPut && r.URL.EscapedPath() == notesPath+"/51":
			assertCommentBody(t, r, "complete")
			_, _ = w.Write([]byte(`{"id":51,"body":"complete","author":{"id":9001,"username":"jcode-bot"}}`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == notesPath:
			if got := r.URL.Query().Get("per_page"); got != "2" {
				t.Fatalf("per_page = %q, want 2", got)
			}
			if got := r.URL.Query().Get("page"); got != "1" {
				t.Fatalf("page = %q, want 1", got)
			}
			if got := r.URL.Query().Get("order_by"); got != "created_at" {
				t.Fatalf("order_by = %q, want created_at", got)
			}
			if got := r.URL.Query().Get("sort"); got != "desc" {
				t.Fatalf("sort = %q, want desc", got)
			}
			_, _ = w.Write([]byte(`[{"id":12,"body":"newest","author":{"username":"jcode-bot"}},{"id":11,"body":"older","author":{"username":"jcode-bot"}}]`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/user":
			_, _ = w.Write([]byte(`{"id":9001,"username":"jcode-bot"}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewGitLabClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := client.CreateManagedIssueComment(ctx, "acme team", "web/api", 7, "processing")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, created, "51", "", "processing")
	if created.AuthorID != "9001" || created.AuthorLogin != "jcode-bot" || created.AuthorAppID != "" {
		t.Fatalf("created author identity=%#v", created)
	}

	updated, err := client.UpdateIssueComment(ctx, "acme team", "web/api", 7, created.ID, "complete")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, updated, created.ID, "", "complete")

	comments, err := client.ListIssueComments(ctx, "acme team", "web/api", 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertIssueCommentsNewestFirst(t, comments, "12", "11")
	identity, err := client.CurrentUserIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "9001" || identity.Login != "jcode-bot" || identity.AppID != "" {
		t.Fatalf("current identity=%#v", identity)
	}
}

func TestGitLabManagedIssueCommentsPaginatesMaximumSizeUnicodeNotes(t *testing.T) {
	t.Parallel()

	const notesPath = "/projects/acme%2Fweb/merge_requests/7/notes"
	const maxGitLabNoteCharacters = 1_000_000
	largeBody := strings.Repeat("😀", maxGitLabNoteCharacters)
	if len(largeBody)*managedIssueCommentPageSize <= maxProviderJSONResponseBytes {
		t.Fatal("fixture no longer exceeds the ordinary provider response limit")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != notesPath {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		requests.Add(1)
		if got := r.URL.Query().Get("per_page"); got != "2" {
			t.Fatalf("per_page = %q, want 2", got)
		}
		if got := r.URL.Query().Get("order_by"); got != "created_at" {
			t.Fatalf("order_by = %q, want created_at", got)
		}
		if got := r.URL.Query().Get("sort"); got != "desc" {
			t.Fatalf("sort = %q, want desc", got)
		}
		w.Header().Set("Content-Type", "application/json")
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 1 || page > 3 {
			t.Fatalf("page = %q, want 1, 2, or 3", r.URL.Query().Get("page"))
		}
		start := (page - 1) * managedIssueCommentPageSize
		end := start + managedIssueCommentPageSize
		if end > 5 {
			end = 5
		}
		notes := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			notes = append(notes, map[string]any{
				"id": 10 - i, "body": largeBody,
				"author": map[string]any{"id": 9001, "username": "jcode-bot"},
			})
		}
		if err := json.NewEncoder(w).Encode(notes); err != nil {
			t.Errorf("encode large notes: %v", err)
		}
	}))
	defer server.Close()

	client, err := NewGitLabClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListIssueComments(context.Background(), "acme", "web", 7, 5)
	if !errors.Is(err, ErrManagedIssueCommentListTooLarge) {
		t.Fatalf("error = %v, want aggregate recovery budget error", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want scan to stop on page 2", got)
	}
}

func TestGiteaManagedIssueCommentsBoundsCompleteIssueHistory(t *testing.T) {
	t.Parallel()

	const commentsPath = "/api/v1/repos/acme/web/issues/7/comments"
	largeBody := strings.Repeat("😀", 1_000_000)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != commentsPath {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		comments := []map[string]any{
			{"id": 3, "body": largeBody}, {"id": 2, "body": largeBody}, {"id": 1, "body": largeBody},
		}
		if err := json.NewEncoder(w).Encode(comments); err != nil {
			t.Errorf("encode large comments: %v", err)
		}
	}))
	defer server.Close()

	client, err := NewGiteaClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListIssueComments(context.Background(), "acme", "web", 7, 100)
	if !errors.Is(err, ErrManagedIssueCommentListTooLarge) {
		t.Fatalf("error = %v, want aggregate recovery budget error", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want one bounded complete-history request", got)
	}
}

func TestProviderUserIdentityMatchesIssueComment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		identity ProviderUserIdentity
		comment  IssueComment
		want     bool
	}{
		{name: "exact app id", identity: ProviderUserIdentity{AppID: "123"}, comment: IssueComment{AuthorAppID: "123", AuthorLogin: "shared-bot"}, want: true},
		{name: "app id cannot fall back to login", identity: ProviderUserIdentity{AppID: "123", Login: "shared-bot"}, comment: IssueComment{AuthorAppID: "456", AuthorLogin: "shared-bot"}},
		{name: "exact user id survives rename", identity: ProviderUserIdentity{ID: "7", Login: "old-name"}, comment: IssueComment{AuthorID: "7", AuthorLogin: "new-name"}, want: true},
		{name: "mismatched ids cannot fall back", identity: ProviderUserIdentity{ID: "7", Login: "same"}, comment: IssueComment{AuthorID: "8", AuthorLogin: "same"}},
		{name: "login fallback when response omits id", identity: ProviderUserIdentity{ID: "7", Login: "JCode-Bot"}, comment: IssueComment{AuthorLogin: "jcode-bot"}, want: true},
		{name: "zero app ids are invalid", identity: ProviderUserIdentity{AppID: "0"}, comment: IssueComment{AuthorAppID: "0"}},
		{name: "zero user ids are invalid", identity: ProviderUserIdentity{ID: "0"}, comment: IssueComment{AuthorID: "0"}},
		{name: "empty identities never match", identity: ProviderUserIdentity{}, comment: IssueComment{}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.identity.MatchesIssueComment(test.comment); got != test.want {
				t.Fatalf("MatchesIssueComment() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProviderIdentityOmitsNonPositiveIDs(t *testing.T) {
	t.Parallel()

	comment, err := newIssueComment(1, "", "body", 0, " bot ", -1)
	if err != nil {
		t.Fatal(err)
	}
	if comment.AuthorID != "" || comment.AuthorAppID != "" || comment.AuthorLogin != "bot" {
		t.Fatalf("comment identity = %#v", comment)
	}
	identity, err := newProviderUserIdentity(0, " legacy-bot ")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "" || identity.Login != "legacy-bot" {
		t.Fatalf("identity = %#v", identity)
	}
	if _, err := newProviderUserIdentity(0, "  "); err == nil {
		t.Fatal("empty identity succeeded")
	}
}

func TestManagedIssueCommentsRejectMalformedCommentIDs(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	github, _ := NewGitHubClient(server.URL, "token")
	gitea, _ := NewGiteaClient(server.URL, "token")
	gitlab, _ := NewGitLabClient(server.URL, "token")
	clients := []ManagedIssueCommentProvider{github, gitea, gitlab}
	invalidIDs := []string{"", "0", "-1", "+1", "01", " 1", "1 ", "1/2", "9223372036854775808"}
	for _, client := range clients {
		for _, id := range invalidIDs {
			if _, err := client.UpdateIssueComment(context.Background(), "o", "r", 1, id, "body"); err == nil {
				t.Errorf("UpdateIssueComment(%q) succeeded, want invalid ID error", id)
			}
		}
	}
	if requestCount != 0 {
		t.Fatalf("malformed IDs caused %d HTTP requests", requestCount)
	}
}

func TestManagedIssueCommentLimit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input int
		want  int
	}{{-1, 100}, {0, 100}, {1, 1}, {100, 100}, {101, 100}} {
		if got := managedIssueCommentLimit(test.input); got != test.want {
			t.Errorf("managedIssueCommentLimit(%d) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestFakeProviderManagedIssueComments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFakeProvider()

	first, err := fake.CreateManagedIssueComment(ctx, "acme", "web", 7, "processing")
	if err != nil {
		t.Fatal(err)
	}
	second, err := fake.CreateManagedIssueComment(ctx, "acme", "web", 7, "newer")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := fake.UpdateIssueComment(ctx, "acme", "web", 7, first.ID, "complete")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueComment(t, updated, first.ID, first.URL, "complete")
	if len(fake.UpdatedComments) != 1 || fake.UpdatedComments[0].ID != first.ID || fake.UpdatedComments[0].Body != "complete" {
		t.Fatalf("updated comments = %#v", fake.UpdatedComments)
	}
	comments, err := fake.ListIssueComments(ctx, "acme", "web", 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertIssueCommentsNewestFirst(t, comments, second.ID, first.ID)

	createErr := errors.New("create failed")
	fake.CommentErr = createErr
	if _, err := fake.CreateManagedIssueComment(ctx, "acme", "web", 7, "retry"); !errors.Is(err, createErr) {
		t.Fatalf("create error = %v", err)
	}
	updateErr := errors.New("update failed")
	fake.UpdateCommentErr = updateErr
	if _, err := fake.UpdateIssueComment(ctx, "acme", "web", 7, first.ID, "retry"); !errors.Is(err, updateErr) {
		t.Fatalf("update error = %v", err)
	}
	listErr := errors.New("list failed")
	fake.ListCommentsErr = listErr
	if _, err := fake.ListIssueComments(ctx, "acme", "web", 7, 100); !errors.Is(err, listErr) {
		t.Fatalf("list error = %v", err)
	}
	fake.ListCommentsErr = nil
	identity, err := fake.CurrentUserIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "9001" || identity.Login != "jcode-bot" || identity.AppID != "12345" {
		t.Fatalf("fake identity = %#v", identity)
	}
	fake.UserID, fake.Username, fake.AppID = "0", " ", "-1"
	if _, err := fake.CurrentUserIdentity(ctx); err == nil {
		t.Fatal("invalid fake identity succeeded")
	}
}

func TestProviderLastPage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		header  http.Header
		perPage int
		want    int
		wantErr bool
	}{
		{name: "single page", header: http.Header{}, perPage: 100, want: 1},
		{name: "link", header: http.Header{"Link": {`<https://git.example/comments?page=7&per_page=100>; rel="last"`}}, perPage: 100, want: 7},
		{name: "total fallback", header: http.Header{"X-Total-Count": {"201"}}, perPage: 100, want: 3},
		{name: "malformed link", header: http.Header{"Link": {`not-a-url; rel="last"`}}, perPage: 100, wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := providerLastPage(test.header, test.perPage)
			if test.wantErr {
				if err == nil {
					t.Fatalf("providerLastPage() = %d, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("providerLastPage() = %d, %v, want %d", got, err, test.want)
			}
		})
	}
}

func assertCommentBody(t *testing.T, r *http.Request, want string) {
	t.Helper()
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.Body != want {
		t.Fatalf("body = %q, want %q", body.Body, want)
	}
}

func assertIssueComment(t *testing.T, got *IssueComment, wantID, wantURL, wantBody string) {
	t.Helper()
	if got == nil || got.ID != wantID || got.URL != wantURL || got.Body != wantBody {
		t.Fatalf("comment = %#v, want ID=%q URL=%q Body=%q", got, wantID, wantURL, wantBody)
	}
}

func assertIssueCommentsNewestFirst(t *testing.T, got []IssueComment, wantIDs ...string) {
	t.Helper()
	if len(got) != len(wantIDs) {
		t.Fatalf("comments = %#v, want %d", got, len(wantIDs))
	}
	for i, wantID := range wantIDs {
		if got[i].ID != wantID {
			t.Fatalf("comments[%d].ID = %q, want %q (all: %#v)", i, got[i].ID, wantID, got)
		}
	}
}
