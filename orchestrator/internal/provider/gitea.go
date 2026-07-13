package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// GiteaWIPPrefix is Gitea's default "work in progress" title prefix. Gitea has
// no explicit `draft` field on the create-PR API; a PR whose title starts with
// this prefix is treated as a draft/WIP (not mergeable until the prefix is
// removed). Prefixing the title is therefore how we open a DRAFT PR portably
// across Gitea versions. (Gitea's default WIP prefixes are configurable; "WIP:"
// is the built-in default and the one the seed deployment uses.)
const GiteaWIPPrefix = "WIP: "

// GiteaClient talks to a Gitea instance's REST API. It implements Provider. It
// is intentionally tiny, stdlib only, matching the orchestrator's std-lib-first
// posture. The auth scheme is either "token" (a personal access token — the
// fallback GITEA_TOKEN) or "Bearer" (a user's OAuth2 access token).
type GiteaClient struct {
	baseURL string // e.g. http://gitea.jcloud.svc.cluster.local:3000
	token   string
	scheme  string // "token" | "Bearer"
	http    *http.Client
}

// NewGiteaClient builds a client authenticating with the "token" scheme (a
// personal access token). baseURL must be the Gitea root (no /api/v1 suffix).
// Returns ErrNotConfigured if either is empty so the caller can degrade
// gracefully.
func NewGiteaClient(baseURL, token string) (*GiteaClient, error) {
	return NewGiteaClientWithScheme(baseURL, token, "token")
}

// NewGiteaClientWithScheme builds a client with an explicit auth scheme:
// "token" for a PAT, "Bearer" for an OAuth2 access token. An empty scheme
// defaults to "token".
func NewGiteaClientWithScheme(baseURL, token, scheme string) (*GiteaClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil, ErrNotConfigured
	}
	if scheme == "" {
		scheme = "token"
	}
	return &GiteaClient{
		baseURL: baseURL,
		token:   token,
		scheme:  scheme,
		http:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// giteaPR is the subset of Gitea's PullRequest JSON we consume.
type giteaPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// FindOpenPRByHead lists open PRs and returns the one whose head ref matches
// `head`. Matching client-side (rather than relying on the version-specific
// `?head=owner:branch` filter) keeps this robust across Gitea versions.
func (c *GiteaClient) FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&limit=50", owner, repo)
	var prs []giteaPR
	if err := c.do(ctx, http.MethodGet, path, nil, &prs); err != nil {
		return nil, err
	}
	for _, p := range prs {
		if p.Head.Ref == head {
			return &PR{Number: p.Number, URL: p.HTMLURL}, nil
		}
	}
	return nil, nil
}

// CreateDraftPR opens a draft PR by prefixing the title with the WIP marker.
// It NEVER merges and NEVER triggers CI — it only creates the PR.
func (c *GiteaClient) CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error) {
	title := in.Title
	if !strings.HasPrefix(title, GiteaWIPPrefix) {
		title = GiteaWIPPrefix + title
	}
	body := map[string]any{
		"head":  in.Head,
		"base":  in.Base,
		"title": title,
		"body":  in.Body,
	}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls", in.Owner, in.Repo)
	var pr giteaPR
	if err := c.do(ctx, http.MethodPost, path, body, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL}, nil
}

// CreatePRReview posts a COMMENT review carrying the AI review markdown. event
// is fixed to "COMMENT" (never APPROVE / REQUEST_CHANGES) so it can never gate a
// merge — comment-only, honouring the never-auto-merge hard gate.
func (c *GiteaClient) CreatePRReview(ctx context.Context, owner, repo string, prNumber int, body string) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	return c.do(ctx, http.MethodPost, path, map[string]any{"event": "COMMENT", "body": body}, nil)
}

// PRStatus returns the current state of a PR ("open"/"closed"/"merged").
func (c *GiteaClient) PRStatus(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	var pr giteaPR
	if err := c.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, State: prState(pr.State, pr.Merged)}, nil
}

// PRByNumber returns the PR with its head/base branch refs populated (M7 webhook).
func (c *GiteaClient) PRByNumber(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	var pr giteaPR
	if err := c.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, err
	}
	return &PR{
		Number:  pr.Number,
		URL:     pr.HTMLURL,
		State:   prState(pr.State, pr.Merged),
		HeadRef: pr.Head.Ref,
		BaseRef: pr.Base.Ref,
	}, nil
}

// CreateIssueComment posts a plain comment on a PR/issue conversation (M7
// receipt + failure replies). Comment only — never approves or merges.
func (c *GiteaClient) CreateIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", owner, repo, issueNumber)
	return c.do(ctx, http.MethodPost, path, map[string]any{"body": body}, nil)
}

// giteaRepo is the subset of Gitea's Repository JSON the repo picker consumes.
type giteaRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}

// ListRepos lists repositories visible to the token's user via /repos/search
// (which scopes results to what the authenticated user may see, including their
// private repos). Most recently updated first.
func (c *GiteaClient) ListRepos(ctx context.Context, query string, page, limit int) ([]Repo, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	path := fmt.Sprintf("/api/v1/repos/search?q=%s&page=%d&limit=%d&sort=updated&order=desc",
		url.QueryEscape(query), page, limit)
	var out struct {
		OK   bool        `json:"ok"`
		Data []giteaRepo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(out.Data))
	for _, r := range out.Data {
		repos = append(repos, Repo{
			ID: r.ID, FullName: r.FullName, Description: r.Description,
			DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL,
		})
	}
	return repos, nil
}

// EnsureCommentWebhook idempotently registers the @mention PR-comment webhook on
// a repository: it lists the repo's hooks and creates one only when no hook with
// the same target URL exists (Gitea masks hook secrets on read, so the URL is
// the identity key — same convention as the old bootstrap Job). Events cover
// both issue_comment and pull_request_comment: Gitea fires the LATTER for
// comments on PRs (M7 live find).
func (c *GiteaClient) EnsureCommentWebhook(ctx context.Context, owner, repo, hookURL, secret string) error {
	return c.ensureWebhook(ctx, owner, repo, hookURL, secret,
		[]string{"issue_comment", "pull_request_comment"}, false)
}

// EnsureReviewWebhook reconciles the same repository hook used by comment
// commands with the PR lifecycle events event-driven review Automations need.
// Existing hooks are PATCHed in place instead of being treated as complete just
// because their target URL matches.
func (c *GiteaClient) EnsureReviewWebhook(ctx context.Context, owner, repo, hookURL, secret string) error {
	return c.ensureWebhook(ctx, owner, repo, hookURL, secret,
		[]string{"issue_comment", "pull_request_comment", "pull_request", "pull_request_sync"}, true)
}

func (c *GiteaClient) ensureWebhook(ctx context.Context, owner, repo, hookURL, secret string, required []string, reconcileEvents bool) error {
	listPath := fmt.Sprintf("/api/v1/repos/%s/%s/hooks", owner, repo)
	var hooks []struct {
		ID     int64             `json:"id"`
		Active bool              `json:"active"`
		Events []string          `json:"events"`
		Config map[string]string `json:"config"`
	}
	if err := c.do(ctx, http.MethodGet, listPath, nil, &hooks); err != nil {
		return err
	}
	for _, h := range hooks {
		if h.Config["url"] == hookURL {
			if !reconcileEvents {
				return nil
			}
			events := unionWebhookEvents(h.Events, required)
			if h.Active && len(events) == len(h.Events) {
				return nil
			}
			if h.ID == 0 {
				return fmt.Errorf("existing webhook at target URL has no provider id")
			}
			body := webhookBody(hookURL, secret, events)
			return c.do(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", listPath, h.ID), body, nil)
		}
	}
	return c.do(ctx, http.MethodPost, listPath, webhookBody(hookURL, secret, required), nil)
}

func webhookBody(hookURL, secret string, events []string) map[string]any {
	return map[string]any{
		"type":   "gitea",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          hookURL,
			"content_type": "json",
			"secret":       secret,
		},
	}
}

func unionWebhookEvents(existing, required []string) []string {
	set := make(map[string]bool, len(existing)+len(required))
	for _, event := range existing {
		set[event] = true
	}
	for _, event := range required {
		set[event] = true
	}
	out := make([]string, 0, len(set))
	for event := range set {
		out = append(out, event)
	}
	sort.Strings(out)
	return out
}

// CurrentUser returns the authenticated user's login (D19 / F5 connectivity check
// + bot_username discovery). Gitea's /api/v1/user carries both "login" and
// "username"; login is the canonical handle.
func (c *GiteaClient) CurrentUser(ctx context.Context) (string, error) {
	var u struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login != "" {
		return u.Login, nil
	}
	return u.Username, nil
}

// prState normalises a provider's (state, merged) pair to our vocabulary.
func prState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	switch strings.ToLower(state) {
	case "open", "opened":
		return "open"
	case "closed":
		return "closed"
	default:
		return strings.ToLower(state)
	}
}

// do performs one authenticated JSON request and decodes a 2xx body into out
// (out may be nil to discard). Non-2xx responses become an error carrying the
// status and a truncated body for diagnosis.
func (c *GiteaClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", c.scheme+" "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	// 4MiB cap: PR payloads are tiny, but /repos/search on a big instance (an
	// admin PAT sees everything) easily blows a smaller cap — a truncated body
	// fails JSON decode with a misleading "unexpected end of JSON input".
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gitea %s %s: status %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

var _ Provider = (*GiteaClient)(nil)
var _ RepoLister = (*GiteaClient)(nil)
var _ CurrentUser = (*GiteaClient)(nil)
