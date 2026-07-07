package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRedact proves credentials in http(s) URL userinfo are stripped and the
// token value never survives redaction.
func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://gho_secretTOKEN@github.com/o/r.git", "https://***@github.com/o/r.git"},
		{"http://x-access-token:tok123@gitea.svc/o/r.git", "http://***@gitea.svc/o/r.git"},
		{"clone of https://oauth2:glpat-xyz@gitlab.com/o/r failed", "clone of https://***@gitlab.com/o/r failed"},
		{"git://git.host/seed.git", "git://git.host/seed.git"}, // no userinfo — unchanged
	}
	for _, tc := range cases {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q want %q", tc.in, got, tc.want)
		}
		if strings.Contains(Redact(tc.in), "secretTOKEN") || strings.Contains(Redact(tc.in), "tok123") ||
			strings.Contains(Redact(tc.in), "glpat-xyz") {
			t.Errorf("Redact leaked a token: %q", Redact(tc.in))
		}
	}
}

func gitAvailable() bool { _, err := exec.LookPath("git"); return err == nil }

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestCreateSourceBundleAndPush is the end-to-end git roundtrip: build a source
// bundle from a repo, and push a runner-style BASE..BRANCH bundle's branch to a
// bare origin.
func TestCreateSourceBundleAndPush(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	ctx := context.Background()
	g := New()
	dir := t.TempDir()

	// origin bare repo with a main commit.
	work := filepath.Join(dir, "work")
	runGit(t, "-C", mkdir(t, work), "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(work, "README.md"), "# seed\n")
	runGit(t, "-C", work, "add", "-A")
	runGit(t, "-C", work, "commit", "-q", "-m", "init")
	origin := filepath.Join(dir, "origin.git")
	runGit(t, "clone", "--bare", "--quiet", work, origin)
	originURL := "file://" + origin

	// 1. CreateSourceBundle produces a verifiable bundle.
	srcBundle := filepath.Join(dir, "source.bundle")
	if err := g.CreateSourceBundle(ctx, originURL, srcBundle); err != nil {
		t.Fatalf("CreateSourceBundle: %v", err)
	}
	if out, err := exec.Command("git", "bundle", "verify", srcBundle).CombinedOutput(); err != nil {
		t.Fatalf("bundle verify: %v: %s", err, out)
	}

	// 2. A runner produces a feature branch off main and bundles BASE..BRANCH.
	runner := filepath.Join(dir, "runner")
	runGit(t, "clone", "--quiet", originURL, runner)
	runGit(t, "-C", runner, "checkout", "-q", "-b", "jcode/run-abc123")
	writeFile(t, filepath.Join(runner, "NEW.txt"), "agent change\n")
	runGit(t, "-C", runner, "add", "-A")
	runGit(t, "-C", runner, "commit", "-q", "-m", "agent")
	runBundle := filepath.Join(dir, "run.bundle")
	runGit(t, "-C", runner, "bundle", "create", runBundle, "main..jcode/run-abc123")

	// 3. PushBundleBranch pushes that branch to origin and returns its sha.
	sha, err := g.PushBundleBranch(ctx, originURL, runBundle, "jcode/run-abc123")
	if err != nil {
		t.Fatalf("PushBundleBranch: %v", err)
	}
	if sha == "" {
		t.Fatal("expected a pushed commit sha")
	}

	// origin now has the branch.
	out, err := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/jcode/run-abc123").CombinedOutput()
	if err != nil {
		t.Fatalf("origin missing pushed branch: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != sha {
		t.Fatalf("origin branch sha=%q want %q", strings.TrimSpace(string(out)), sha)
	}

	// 4. Idempotent: a second push of the same bundle is a no-op (up-to-date).
	if _, err := g.PushBundleBranch(ctx, originURL, runBundle, "jcode/run-abc123"); err != nil {
		t.Fatalf("second PushBundleBranch (idempotent): %v", err)
	}
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
