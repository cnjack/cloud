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

// TestPushBundleBranchFFOnly is the M7 webhook update-mode roundtrip: a runner
// commits onto an existing PR head branch and bundles startSHA..HEAD; the control
// plane ff-only pushes it back onto that branch. It then proves the idempotent
// (already-present) path and the non-fast-forward rejection (never force-push).
func TestPushBundleBranchFFOnly(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	ctx := context.Background()
	g := New()
	dir := t.TempDir()

	// origin bare repo with main + an existing PR head branch "feature-x".
	work := filepath.Join(dir, "work")
	runGit(t, "-C", mkdir(t, work), "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(work, "README.md"), "# seed\n")
	runGit(t, "-C", work, "add", "-A")
	runGit(t, "-C", work, "commit", "-q", "-m", "init")
	runGit(t, "-C", work, "checkout", "-q", "-b", "feature-x")
	writeFile(t, filepath.Join(work, "FEATURE.txt"), "wip\n")
	runGit(t, "-C", work, "add", "-A")
	runGit(t, "-C", work, "commit", "-q", "-m", "feature start")
	origin := filepath.Join(dir, "origin.git")
	runGit(t, "clone", "--bare", "--quiet", work, origin)
	originURL := "file://" + origin

	// A runner clones feature-x, records its start SHA, adds a commit, bundles
	// startSHA..feature-x (exactly what the entrypoint does when BRANCH==BASE).
	runner := filepath.Join(dir, "runner")
	runGit(t, "clone", "--quiet", "--branch", "feature-x", originURL, runner)
	startSHA := revParse(t, runner, "HEAD")
	writeFile(t, filepath.Join(runner, "CONTRIBUTING.md"), "# contributing\n")
	runGit(t, "-C", runner, "add", "-A")
	runGit(t, "-C", runner, "commit", "-q", "-m", "add contributing")
	runBundle := filepath.Join(dir, "update.bundle")
	runGit(t, "-C", runner, "bundle", "create", runBundle, startSHA+"..feature-x")

	// ff-only push lands the change onto origin/feature-x.
	sha, present, err := g.PushBundleBranchFFOnly(ctx, originURL, runBundle, "feature-x")
	if err != nil {
		t.Fatalf("PushBundleBranchFFOnly: %v", err)
	}
	if present {
		t.Fatal("first push should not be already-present")
	}
	if got := revParse(t, origin, "refs/heads/feature-x"); got != sha {
		t.Fatalf("origin feature-x=%q want pushed sha %q", got, sha)
	}

	// Idempotent: re-pushing the same bundle reports already-present, no error.
	sha2, present2, err := g.PushBundleBranchFFOnly(ctx, originURL, runBundle, "feature-x")
	if err != nil || !present2 {
		t.Fatalf("second push: sha=%q present=%v err=%v want already-present", sha2, present2, err)
	}

	// Non-fast-forward: origin advances feature-x independently (a fresh clone off
	// origin's current tip, so this push ff-applies), so a divergent bundle can no
	// longer ff-apply → error, and NO force-push occurs.
	concurrent := filepath.Join(dir, "concurrent")
	runGit(t, "clone", "--quiet", "--branch", "feature-x", originURL, concurrent)
	writeFile(t, filepath.Join(concurrent, "OTHER.txt"), "concurrent\n")
	runGit(t, "-C", concurrent, "add", "-A")
	runGit(t, "-C", concurrent, "commit", "-q", "-m", "concurrent change")
	runGit(t, "-C", concurrent, "push", "-q", "origin", "feature-x")
	originTip := revParse(t, origin, "refs/heads/feature-x")

	// A second runner bundle off the ORIGINAL start (diverges from origin's tip).
	runner2 := filepath.Join(dir, "runner2")
	runGit(t, "clone", "--quiet", originURL, runner2)
	runGit(t, "-C", runner2, "checkout", "-q", "-B", "feature-x", startSHA)
	writeFile(t, filepath.Join(runner2, "DIVERGE.txt"), "diverge\n")
	runGit(t, "-C", runner2, "add", "-A")
	runGit(t, "-C", runner2, "commit", "-q", "-m", "divergent change")
	divBundle := filepath.Join(dir, "diverge.bundle")
	runGit(t, "-C", runner2, "bundle", "create", divBundle, startSHA+"..feature-x")

	if _, _, err := g.PushBundleBranchFFOnly(ctx, originURL, divBundle, "feature-x"); err == nil {
		t.Fatal("expected a non-fast-forward error")
	}
	if got := revParse(t, origin, "refs/heads/feature-x"); got != originTip {
		t.Fatalf("non-ff push must NOT move origin (force): tip=%q want %q", got, originTip)
	}
}

func revParse(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v: %s", ref, repo, err, out)
	}
	return strings.TrimSpace(string(out))
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
