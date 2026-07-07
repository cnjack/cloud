// Package gitcli is a thin wrapper over the `git` binary for the two operations
// the M3 control plane performs on the user's behalf: building a source bundle
// of a repo (so a runner can fetch it instead of cloning a private repo with a
// token), and pushing a runner-produced bundle's branch to the provider.
//
// It shells out to `git` because there is no first-class pure-Go bundle/push
// path and the orchestrator image already ships the git CLI for exactly this
// (see the Dockerfile note). Every remote URL carries the credential in its
// userinfo; it is passed to a subprocess only, never written to a durable log,
// and every error is REDACTED before it leaves this package.
package gitcli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Git wraps a git binary. The zero value is not usable; use New.
type Git struct {
	bin string
}

// New returns a Git using the `git` binary from PATH.
func New() *Git { return &Git{bin: "git"} }

// Available reports whether the git binary is resolvable (tests skip if not).
func (g *Git) Available() bool {
	_, err := exec.LookPath(g.bin)
	return err == nil
}

// userinfoRE strips the userinfo (credentials) from an http(s) URL for logging.
var userinfoRE = regexp.MustCompile(`(https?://)[^/@\s]+@`)

// Redact removes credentials (URL userinfo) from a string so a git error can be
// surfaced without ever leaking the token.
func Redact(s string) string { return userinfoRE.ReplaceAllString(s, "$1***@") }

// run executes `git args...` with credential prompting disabled and returns
// combined output. On error, both the error text and the returned combined
// output are REDACTED so no caller can accidentally log a token.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	// Never block on an interactive credential/askpass prompt, and never consult a
	// credential helper — the token travels only in the URL we pass.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GCM_INTERACTIVE=never",
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := Redact(buf.String())
	if err != nil {
		return out, fmt.Errorf("git %s: %v: %s", Redact(strings.Join(redactArgs(args), " ")), err, tail(out, 400))
	}
	return out, nil
}

// redactArgs redacts any credential-bearing URL argument.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = Redact(a)
	}
	return out
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// CreateSourceBundle bare-clones remoteURL and writes an all-refs bundle to
// bundlePath. remoteURL must carry any auth in its userinfo. The bundle lets a
// runner reconstruct the repo (git clone <bundle>) without ever seeing a token.
func (g *Git) CreateSourceBundle(ctx context.Context, remoteURL, bundlePath string) error {
	work, err := os.MkdirTemp("", "jcloud-src-")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)
	bare := filepath.Join(work, "repo.git")
	if _, err := g.run(ctx, "clone", "--bare", "--quiet", remoteURL, bare); err != nil {
		return err
	}
	if _, err := g.run(ctx, "-C", bare, "bundle", "create", bundlePath, "--all"); err != nil {
		return err
	}
	return nil
}

// PushBundleBranch bare-clones remoteURL, fetches `branch` from the local bundle
// at bundlePath (the runner produced it as BASE..BRANCH, so the prerequisite base
// commit is already present in the fresh clone), then pushes that branch to
// origin. It returns the branch tip SHA. It is idempotent: a branch already
// present with the same content pushes as "up-to-date" (no error), and a
// re-fetch of the same commits is a fast-forward/no-op.
func (g *Git) PushBundleBranch(ctx context.Context, remoteURL, bundlePath, branch string) (string, error) {
	work, err := os.MkdirTemp("", "jcloud-push-")
	if err != nil {
		return "", fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)
	bare := filepath.Join(work, "repo.git")
	if _, err := g.run(ctx, "clone", "--bare", "--quiet", remoteURL, bare); err != nil {
		return "", err
	}
	ref := "refs/heads/" + branch
	// `+src:dst` forces the local ref so a re-run is idempotent.
	if _, err := g.run(ctx, "-C", bare, "fetch", "--quiet", bundlePath, "+"+ref+":"+ref); err != nil {
		return "", err
	}
	sha, err := g.run(ctx, "-C", bare, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	if _, err := g.run(ctx, "-C", bare, "push", "--quiet", "origin", ref+":"+ref); err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}
