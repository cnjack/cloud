// Package provider wraps a git host's PR API behind a small interface so the
// reconciler can open draft PRs without importing an HTTP client, and can be
// unit-tested with a fake (the same seam pattern as k8s.JobLauncher).
//
// Scope (ST-1 / decision D08): the ONLY operations are "find an open PR by head
// branch" and "create a draft PR". There is deliberately NO merge and NO CI
// trigger — that is a hard architectural gate (never auto-merge, never auto-CI).
package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
)

// PR is the minimal view of a pull request the reconciler needs.
type PR struct {
	Number int
	URL    string // human-facing HTML URL (persisted on the run as pr_url)
	State  string // "open" | "closed" | "merged" | "" (unknown) — used by PRStatus
	// Head/Base branch refs. Populated by PRByNumber (M7 webhook needs them to
	// build/diff against an existing PR); empty for the list/create shapes that do
	// not carry them.
	HeadRef string
	BaseRef string
}

// CreateDraftPRInput is the request to open a draft PR.
type CreateDraftPRInput struct {
	// Owner/Repo identify the repository ("owner/name" split by the caller).
	Owner string
	Repo  string
	// Head is the source branch (agent/run-<id>); Base is the target
	// (project.default_branch).
	Head string
	Base string
	// Title/Body for the PR. Title is "[jcode] <prompt first line>".
	Title string
	Body  string
}

// Provider is the git-host PR API seam. Implementations are idempotent-friendly:
// FindOpenPRByHead lets the caller check for an existing PR before creating one,
// so a retried reconcile never double-opens.
type Provider interface {
	// FindOpenPRByHead returns the open PR whose head branch is `head`, or
	// (nil, nil) if none exists. owner/repo identify the repository.
	FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error)
	// CreateDraftPR opens a DRAFT pull request and returns it. It must never
	// merge or trigger CI.
	CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error)
	// CreatePRReview posts a plain review comment on a pull request (the AI review
	// output). It never approves/requests-changes with a merge effect — it is a
	// comment-only review, so the hard "never auto-merge" gate holds (M3/M5).
	CreatePRReview(ctx context.Context, owner, repo string, prNumber int, body string) error
	// PRStatus returns the current state of a PR ("open"/"closed"/"merged"), or
	// state "" when it cannot be determined (M5 GET /pr live status).
	PRStatus(ctx context.Context, owner, repo string, prNumber int) (*PR, error)
	// PRByNumber returns a PR by its number/iid with its HeadRef/BaseRef/URL/State
	// populated. The M7 webhook needs the head/base branches of the PR a comment
	// was posted on (the webhook payload's issue does not carry them).
	PRByNumber(ctx context.Context, owner, repo string, prNumber int) (*PR, error)
	// CreateIssueComment posts a plain comment on an issue / PR conversation (the
	// M7 webhook receipt "🚀 run started …" and failure replies). It is a comment
	// only — it never approves/merges, so the never-auto-merge gate holds.
	CreateIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) error
}

// Repo is one entry in a provider repository listing (the Drone-style
// service-onboarding picker). ID is the provider's numeric repo id — stored on
// a service as its rename-proof identity (provider_repo_id).
type Repo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"` // "owner/name"
	Description   string `json:"description,omitempty"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url,omitempty"`
}

// RepoLister lists repositories visible to the authenticated token. It is a
// SEPARATE interface from Provider on purpose: the D08 PR seam stays as narrow
// as ever (find/create-draft/comment only); listing is a read-only onboarding
// concern. All three concrete clients implement it — callers type-assert the
// Factory-built client.
type RepoLister interface {
	// ListRepos returns up to `limit` repos matching `query` (empty = all),
	// page-numbered from 1, most recently active first.
	ListRepos(ctx context.Context, query string, page, limit int) ([]Repo, error)
}

// Factory builds a Provider client for a given git host authenticated with a
// specific token (the triggering user's OAuth token, or the fallback gitea PAT).
// The M3 draft-PR / review passes act with the token that owns the change, so a
// single static client is not enough — each run resolves its own credential and
// asks the factory for a matching client.
type Factory interface {
	// PRClient returns a Provider for host `prov` authenticated with token
	// (scheme is "token" for a gitea PAT or "Bearer" for an OAuth access token).
	// giteaBaseURL supplies the self-hosted gitea root. ErrNotConfigured when a
	// client cannot be built (e.g. gitea with no base URL).
	PRClient(prov domain.GitProvider, token, scheme string) (Provider, error)
}

// ErrNotConfigured is returned by a factory when the provider credentials/URL
// are absent, so the reconciler can degrade gracefully (leave the run as a
// diff-only success) rather than crash.
var ErrNotConfigured = errors.New("git provider not configured")

// httpFactory is the default Factory: it builds gitea/github/gitlab REST clients.
type httpFactory struct {
	giteaURL string
}

// NewFactory builds the default provider Factory. giteaURL is the self-hosted
// gitea root used for gitea provider clients.
func NewFactory(giteaURL string) Factory { return &httpFactory{giteaURL: strings.TrimSpace(giteaURL)} }

func (f *httpFactory) PRClient(prov domain.GitProvider, token, scheme string) (Provider, error) {
	switch prov {
	case domain.ProviderGitea:
		return NewGiteaClientWithScheme(f.giteaURL, token, scheme)
	case domain.ProviderGitHub:
		return NewGitHubClient("", token)
	case domain.ProviderGitLab:
		return NewGitLabClient("", token)
	default:
		return nil, ErrNotConfigured
	}
}

// SplitRepo splits an "owner/name" repo identifier. Extra path segments beyond
// the first slash are folded into the name so "org/sub/repo" is tolerated as
// owner=org, name="sub/repo" — Gitea repo names never contain slashes, but this
// keeps a stray input from silently targeting the wrong repo.
func SplitRepo(ownerRepo string) (owner, name string, ok bool) {
	ownerRepo = strings.TrimSuffix(strings.TrimSpace(ownerRepo), ".git")
	i := strings.Index(ownerRepo, "/")
	if i <= 0 || i == len(ownerRepo)-1 {
		return "", "", false
	}
	return ownerRepo[:i], ownerRepo[i+1:], true
}
