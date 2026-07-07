package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// freshServerWithGitea builds a test server over the given store whose config's
// gitea base is giteaURL (so ServiceCloneURL resolves provider repos there).
func freshServerWithGitea(t *testing.T, st *store.MemStore, giteaURL string) *httptest.Server {
	t.Helper()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken, GiteaURL: giteaURL, SourceBundleTTL: time.Minute}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// scheduledRun creates a project+service+run and gives the run a token so the
// internal (RUN_TOKEN) endpoints accept it. Returns the run id + plaintext token.
func scheduledRun(t *testing.T, st *store.MemStore, svc *domain.Service, kind domain.RunKind) (string, string) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc.ID = domain.NewID()
	svc.ProjectID = p.ID
	svc.Name = "default"
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	svc.CreatedAt = time.Now()
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "task",
		Status: domain.StatusQueued, Kind: kind, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	return run.ID, tok
}

// postRaw sends a raw (non-JSON) body with a bearer token and content-type.
func postRaw(t *testing.T, url, token, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestIngestBundleStoresAndRecordsBranch proves a bundle upload is stored, marks
// the run with its push branch (so the PR pass finds it), and enforces the 16MiB
// cap with a 413.
func TestIngestBundleStoresAndRecordsBranch(t *testing.T) {
	ts, st, _ := newTestServer(t)
	rid, tok := scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "o/r", GitMode: domain.GitModeDraftPR,
	}, domain.RunKindAgent)
	ctx := context.Background()

	payload := []byte("# v2 git bundle\nfake-bundle-bytes")
	resp := postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/bundle", tok, "application/octet-stream", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bundle upload: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	// Stored bytes match.
	got, err := st.GetRunBundle(ctx, rid)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored bundle mismatch: err=%v", err)
	}
	// The run now carries the deterministic push branch (drives ListRunsAwaitingPR).
	run, _ := st.GetRun(ctx, rid)
	if run.GitBranch != domain.RunBranchName(rid) {
		t.Fatalf("git_branch=%q want %q", run.GitBranch, domain.RunBranchName(rid))
	}

	// Over the 16MiB limit -> 413.
	big := make([]byte, maxBundleBytes+1)
	resp = postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/bundle", tok, "application/octet-stream", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize bundle: status=%d want 413", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestIngestBundleRejectsConsoleToken proves the internal bundle endpoint refuses
// the console token (RUN_TOKEN only).
func TestIngestBundleRejectsConsoleToken(t *testing.T) {
	ts, st, _ := newTestServer(t)
	rid, _ := scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "o/r", GitMode: domain.GitModeDraftPR,
	}, domain.RunKindAgent)
	resp := postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/bundle", consoleToken, "application/octet-stream", []byte("x"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("console token on internal bundle: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestIngestReviewStoresOutput proves a review upload lands in runs.review_output.
func TestIngestReviewStoresOutput(t *testing.T) {
	ts, st, _ := newTestServer(t)
	rid, tok := scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "o/r", GitMode: domain.GitModeDraftPR,
	}, domain.RunKindReview)
	ctx := context.Background()

	md := "## Review\n\nconclusion: approve\n\n- looks good\n"
	resp := postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/review", tok, "text/plain", []byte(md))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("review upload: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	run, _ := st.GetRun(ctx, rid)
	if run.ReviewOutput != md {
		t.Fatalf("review_output=%q want %q", run.ReviewOutput, md)
	}

	// Empty review -> 400.
	resp = postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/review", tok, "text/plain", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty review: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- source bundle (lazy generation over a local git fixture) ----------------

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// makeFixtureRepo builds a bare git repo at <dir>/<owner>/<repo>.git with one
// commit, and returns a gitea-style file:// base URL such that
// ServiceCloneURL(giteaProvider, owner/repo, base) resolves to that bare repo.
func makeFixtureRepo(t *testing.T, owner, repo string) (baseURL string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run("-C", work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", work, "add", "-A")
	run("-C", work, "commit", "-q", "-m", "init")
	bare := filepath.Join(dir, owner, repo+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	run("clone", "--bare", "--quiet", work, bare)
	return "file://" + dir
}

// TestGetSourceLazyGeneration proves GET /internal/.../source builds a valid git
// bundle from the service's repo (a local file fixture) over the RUN_TOKEN, and
// serves the same bytes from cache on a second request.
func TestGetSourceLazyGeneration(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	st := store.NewMemStore()
	base := makeFixtureRepo(t, "o", "r")
	srv := freshServerWithGitea(t, st, base)

	rid, tok := scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "o/r", GitMode: domain.GitModeReadonly,
	}, domain.RunKindAgent)

	data1 := getSource(t, srv, rid, tok, http.StatusOK)
	if len(data1) == 0 || !bytes.HasPrefix(data1, []byte("# v")) {
		t.Fatalf("source is not a git bundle (prefix=%q len=%d)", firstBytes(data1, 16), len(data1))
	}
	// Verify the bundle is loadable by git.
	verifyBundle(t, data1)

	// Second request served from cache — identical bytes.
	data2 := getSource(t, srv, rid, tok, http.StatusOK)
	if !bytes.Equal(data1, data2) {
		t.Fatal("cached source differs from first generation")
	}
}

// TestGetSourceRejectsRawService proves the source endpoint 400s for a raw
// service (only provider services fetch a source bundle).
func TestGetSourceRejectsRawService(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	st := store.NewMemStore()
	srv := freshServerWithGitea(t, st, "file:///nonexistent")
	rid, tok := scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git",
	}, domain.RunKindAgent)
	_ = getSource(t, srv, rid, tok, http.StatusBadRequest)
}

func firstBytes(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// verifyBundle writes the bytes to a temp file and asserts `git bundle verify`.
func verifyBundle(t *testing.T, data []byte) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "b-*.bundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cmd := exec.Command("git", "bundle", "verify", f.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git bundle verify failed: %v: %s", err, out)
	}
}

func getSource(t *testing.T, srv *httptest.Server, rid, tok string, wantStatus int) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/internal/v1/runs/"+rid+"/source", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET source: status=%d want %d (%s)", resp.StatusCode, wantStatus, body)
	}
	b, _ := io.ReadAll(resp.Body)
	return b
}
