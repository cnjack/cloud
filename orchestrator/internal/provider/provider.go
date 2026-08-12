// Package provider wraps a git host's PR API behind a small interface so the
// reconciler can open draft PRs without importing an HTTP client, and can be
// unit-tested with a fake (the same seam pattern as k8s.JobLauncher).
//
// Scope (ST-1 / decision D08): the ONLY operations are "find an open PR by head
// branch" and "create a draft PR". There is deliberately NO merge and NO CI
// dispatch — that is a hard architectural gate (never auto-merge, never
// explicitly dispatch CI; repository push/PR workflows may still run).
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
)

// PR is the minimal view of a pull request the reconciler needs.
type PR struct {
	Number int
	URL    string // human-facing HTML URL (persisted on the run as pr_url)
	Title  string // provider title frozen onto review Runs for display/audit
	State  string // "open" | "closed" | "merged" | "" (unknown) — used by PRStatus
	Draft  bool   // provider-normalized Draft/WIP state
	// Head/Base branch refs. Populated by PRByNumber (M7 webhook needs them to
	// build/diff against an existing PR); empty for the list/create shapes that do
	// not carry them.
	HeadRef string
	BaseRef string
	HeadSHA string
	BaseSHA string
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

// CreatePRInput is the lifecycle-aware create request. Draft=false means the
// provider must create a normal, ready-for-review pull request.
type CreatePRInput struct {
	Owner string
	Repo  string
	Head  string
	Base  string
	Title string
	Body  string
	Draft bool
}

// Provider is the git-host PR API seam. Implementations are idempotent-friendly:
// FindOpenPRByHead lets the caller check for an existing PR before creating one,
// so a retried reconcile never double-opens.
type Provider interface {
	// FindOpenPRByHead returns the open PR whose head branch is `head`, or
	// (nil, nil) if none exists. owner/repo identify the repository.
	FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error)
	// CreateDraftPR opens a DRAFT pull request and returns it. It must never
	// merge or explicitly dispatch CI.
	CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error)
	// CreatePR creates either a Draft or Ready PR according to in.Draft.
	CreatePR(ctx context.Context, in CreatePRInput) (*PR, error)
	// MarkPRReady idempotently promotes a provider Draft/WIP PR. Closed/merged
	// PRs are returned unchanged; implementations never reopen them.
	MarkPRReady(ctx context.Context, owner, repo string, prNumber int) (*PR, error)
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

type PRReview struct {
	Body     string
	Comments []PRReviewComment
}

type PRReviewComment struct {
	Path    string
	Line    int
	EndLine int
	Body    string
}

// BatchReviewProvider posts one comment-only review with inline findings.
// Providers without this optional capability keep the top-level renderer.
type BatchReviewProvider interface {
	CreatePRReviewBatch(ctx context.Context, owner, repo string, prNumber int, review PRReview) error
}

// IssueCommentReactor provides low-noise acknowledgement for mention commands.
type IssueCommentReactor interface {
	CreateIssueCommentReaction(ctx context.Context, owner, repo string, commentID int64, content string) error
}

// IssueComment is the provider-neutral view of a mutable issue or pull-request
// conversation comment. ID deliberately remains a string at the abstraction
// boundary even though the currently supported providers return numeric IDs.
type IssueComment struct {
	ID          string
	URL         string
	Body        string
	AuthorID    string
	AuthorLogin string
	AuthorAppID string
}

// ProviderUserIdentity is the provider-neutral identity used to prove that a
// marker-bearing comment belongs to the credential that is synchronizing it.
// Numeric provider identifiers are canonical positive decimal strings; a
// missing/zero identifier is represented as empty and is never a match.
type ProviderUserIdentity struct {
	ID    string
	Login string
	AppID string
}

// MatchesIssueComment compares the strongest identity available. GitHub App
// ownership is never allowed to fall back to a bot login. For user-backed
// providers an exact numeric id wins; login is a compatibility fallback only
// when either side did not return an id.
func (identity ProviderUserIdentity) MatchesIssueComment(comment IssueComment) bool {
	if appID := canonicalProviderNumericIDString(identity.AppID); appID != "" {
		return appID == canonicalProviderNumericIDString(comment.AuthorAppID)
	}
	identityID := canonicalProviderNumericIDString(identity.ID)
	commentID := canonicalProviderNumericIDString(comment.AuthorID)
	if identityID != "" && commentID != "" {
		return identityID == commentID
	}
	identityLogin := strings.TrimSpace(identity.Login)
	commentLogin := strings.TrimSpace(comment.AuthorLogin)
	return identityLogin != "" && commentLogin != "" &&
		strings.EqualFold(identityLogin, commentLogin)
}

// CurrentUserIdentity is the optional capability for providers whose current
// token maps to an ordinary user account. GitHub App installation tokens do
// not implement it: callers must compare their frozen App id with
// IssueComment.AuthorAppID instead of relying on /user.
type CurrentUserIdentity interface {
	CurrentUserIdentity(ctx context.Context) (ProviderUserIdentity, error)
}

// ManagedIssueCommentProvider is the optional capability for a caller that must
// create one durable status comment and update that same comment as work
// progresses. Provider remains unchanged so existing callers and test fakes do
// not need to implement comment tracking.
type ManagedIssueCommentProvider interface {
	CreateManagedIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) (*IssueComment, error)
	UpdateIssueComment(ctx context.Context, owner, repo string, issueNumber int, commentID, body string) (*IssueComment, error)
	// ListIssueComments returns at most limit comments, newest first. Invalid or
	// provider-excessive limits use the bounded default of 100.
	ListIssueComments(ctx context.Context, owner, repo string, issueNumber, limit int) ([]IssueComment, error)
}

const (
	maxManagedIssueComments = 100
	// Keep list pages small because provider comment bodies can be much larger
	// than ordinary repository payloads (GitLab permits one million characters
	// per note). The recovery path may still assemble the newest 100 comments,
	// but no single response contains more than two bodies.
	managedIssueCommentPageSize = 2
	// Ascending-only APIs need one initial request to discover their last page,
	// followed by enough tail pages to collect maxManagedIssueComments.
	maxManagedIssueCommentPageFetches = 1 + (maxManagedIssueComments+managedIssueCommentPageSize-1)/managedIssueCommentPageSize
	// Two one-million-character GitLab notes can exceed eight MiB when their
	// Unicode is encoded as UTF-8 (and more when JSON escapes control codepoints).
	// Sixteen MiB keeps that legal page bounded without relaxing all REST calls.
	maxManagedIssueCommentJSONResponseBytes = 16 << 20
	// Marker recovery must also bound the aggregate retained across pages. A PR
	// participant can create many provider-legal multi-megabyte comments; keeping
	// the newest 100 full bodies would otherwise multiply into hundreds of MiB
	// per worker. Exceeding this budget fails closed so callers retry instead of
	// creating a duplicate managed comment from an incomplete recovery scan.
	maxManagedIssueCommentListBodyBytes = 8 << 20
)

// ErrInvalidIssueCommentID means a caller supplied a comment ID that cannot be
// represented safely in the numeric REST paths used by all supported hosts, or
// a provider returned an unusable ID.
var (
	ErrInvalidIssueCommentID           = errors.New("invalid issue comment id")
	ErrManagedIssueCommentListTooLarge = errors.New("managed issue comment recovery window exceeds byte budget")
)

func addManagedIssueCommentBodyBytes(used int, bodies ...string) (int, error) {
	for _, body := range bodies {
		if len(body) > maxManagedIssueCommentListBodyBytes-used {
			return used, ErrManagedIssueCommentListTooLarge
		}
		used += len(body)
	}
	return used, nil
}

func managedIssueCommentLimit(limit int) int {
	if limit < 1 || limit > maxManagedIssueComments {
		return maxManagedIssueComments
	}
	return limit
}

func parseIssueCommentID(commentID string) (int64, error) {
	id, err := strconv.ParseInt(commentID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != commentID {
		return 0, ErrInvalidIssueCommentID
	}
	return id, nil
}

func canonicalProviderNumericID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func canonicalProviderNumericIDString(id string) string {
	parsed, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil || parsed <= 0 {
		return ""
	}
	return strconv.FormatInt(parsed, 10)
}

func newProviderUserIdentity(id int64, login string) (ProviderUserIdentity, error) {
	identity := ProviderUserIdentity{
		ID:    canonicalProviderNumericID(id),
		Login: strings.TrimSpace(login),
	}
	if identity.ID == "" && identity.Login == "" {
		return ProviderUserIdentity{}, errors.New("provider returned no usable current-user identity")
	}
	return identity, nil
}

func newIssueComment(id int64, commentURL, body string, authorID int64, authorLogin string, authorAppID int64) (*IssueComment, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: provider returned a non-positive id", ErrInvalidIssueCommentID)
	}
	return &IssueComment{
		ID: strconv.FormatInt(id, 10), URL: commentURL, Body: body,
		AuthorID:    canonicalProviderNumericID(authorID),
		AuthorLogin: strings.TrimSpace(authorLogin),
		AuthorAppID: canonicalProviderNumericID(authorAppID),
	}, nil
}

// escapedRepositoryPath builds the two distinct path segments GitHub and Gitea
// expect. Escaping each segment prevents a malformed repository identity from
// injecting another API path component.
func escapedRepositoryPath(owner, repo string) string {
	return url.PathEscape(owner) + "/" + url.PathEscape(repo)
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

// Branch is one repository branch available to the authenticated credential.
// It deliberately omits provider-specific commit metadata: the task composer
// only needs a stable ref name and the protection hint for display.
type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected,omitempty"`
}

// BranchLister lists branches of a repository that is already bound to a
// Service. It is separate from RepoLister so the service creation picker and
// task-time branch selector stay read-only, narrowly scoped operations.
type BranchLister interface {
	ListBranches(ctx context.Context, owner, repo string, page, limit int) ([]Branch, error)
}

// InstallationRepoLister lists the repositories granted to an App
// installation. GitHub installation access tokens cannot call /user/repos, so
// their repository picker must use the installation-scoped API instead.
type InstallationRepoLister interface {
	ListInstallationRepos(ctx context.Context, query string, page, limit int) ([]Repo, error)
}

// SCMWebhookManager owns the provider-side hook used by the unified SCM
// Automation ingress. It is intentionally separate from Provider: webhook
// registration is a control-plane operation and must be performed only with a
// Project Plugin installation credential, never with a run credential or a
// legacy cluster token.
//
// GitHub deliberately does not implement this interface. GitHub App webhooks
// are configured once for the App at cluster scope; GitLab and Gitea need a
// repository hook for each Service that has enabled SCM Automations.
type SCMWebhookManager interface {
	EnsureSCMWebhook(ctx context.Context, owner, repo, hookURL, secret string) error
	DeleteSCMWebhook(ctx context.Context, owner, repo, hookURL string) error
}

// CurrentUser reports the username the authenticated token acts as. It backs the
// integration connectivity check + bot_username discovery (D19 / F5): an
// integration create/rotate calls it with the supplied token, so a bad/expired
// token fails visibly (400 with the provider's error) and the returned username is
// stored as the integration's bot_username. All three concrete clients implement
// it — callers type-assert the client.
type CurrentUser interface {
	// CurrentUser returns the token's account username, or an error when the token
	// is rejected / the host is unreachable.
	CurrentUser(ctx context.Context) (string, error)
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

// FrozenFactory is the optional factory capability for clients that must use an
// append-only Provider base URL captured with a Run. The production factory and
// reconciler fakes implement it; callers may fall back to
// IntegrationClientWithScheme for older custom factories.
type FrozenFactory interface {
	FrozenPRClient(prov domain.GitProvider, baseURL, token, scheme string) (Provider, error)
}

// ErrNotConfigured is returned by a factory when the provider credentials/URL
// are absent, so the reconciler can degrade gracefully (leave the run as a
// diff-only success) rather than crash.
var ErrNotConfigured = errors.New("git provider not configured")
var ErrMultipleOpenPRs = errors.New("multiple open pull requests use the run head branch")
var ErrUnsupportedPRTransition = errors.New("git provider cannot reliably change the pull request state")

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

func (f *httpFactory) FrozenPRClient(prov domain.GitProvider, baseURL, token, scheme string) (Provider, error) {
	return IntegrationClientWithScheme(prov, baseURL, token, scheme)
}

// IntegrationClient builds a REST client for an integration's host + token
// (D19 / F5). Unlike Factory.PRClient (fixed public/cluster hosts), it derives the
// base URL from the integration host so a self-hosted gitea or an enterprise
// github/gitlab is reachable. The returned value satisfies Provider and — for all
// three concrete clients — RepoLister + CurrentUser (type-assert as needed). A PAT
// authenticates with the "token" scheme on gitea; github/gitlab clients Bearer the
// token internally. ErrNotConfigured when host/token is empty or the provider is
// unknown.
func IntegrationClient(prov domain.GitProvider, host, token string) (Provider, error) {
	return IntegrationClientWithScheme(prov, host, token, "")
}

// IntegrationClientWithScheme is the Plugin-aware variant of IntegrationClient.
// Gitea OAuth access tokens require Bearer authentication, while legacy PAT
// integrations use the historical token scheme.
func IntegrationClientWithScheme(prov domain.GitProvider, host, token, scheme string) (Provider, error) {
	base := integrationBaseURL(host)
	if base == "" || strings.TrimSpace(token) == "" {
		return nil, ErrNotConfigured
	}
	switch prov {
	case domain.ProviderGitea:
		if scheme == "" {
			scheme = "token"
		}
		return NewGiteaClientWithScheme(base, token, scheme)
	case domain.ProviderGitHub:
		return NewGitHubClient(githubAPIBase(base), token)
	case domain.ProviderGitLab:
		return NewGitLabClient(base+"/api/v4", token)
	default:
		return nil, ErrNotConfigured
	}
}

// integrationBaseURL turns an integration host into a base URL: a value already
// carrying a scheme is used verbatim (trailing slash trimmed); a bare host defaults
// to https. Returns "" for empty input.
func integrationBaseURL(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		return strings.TrimRight(h, "/")
	}
	return "https://" + strings.TrimRight(h, "/")
}

// githubAPIBase maps a github base URL to its REST API base: public github.com →
// api.github.com; an enterprise host → <base>/api/v3.
func githubAPIBase(base string) string {
	if domain.NormalizeGitHost(base) == "github.com" {
		return "https://api.github.com"
	}
	return strings.TrimRight(base, "/") + "/api/v3"
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
