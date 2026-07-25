package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/safehttp"
)

// GitLabClient talks to the GitLab REST API (v4). It implements Provider using
// GitLab's merge-request vocabulary (source/target branch, iid, Draft: title
// prefix, notes for review comments). Not integration tested locally; httptest
// covered so the multi-provider seam is complete (blueprint §2).
type GitLabClient struct {
	apiBase string // e.g. https://gitlab.com/api/v4
	token   string
	http    *http.Client
}

// GitLabDraftPrefix is GitLab's marker for a draft merge request (title prefix).
const GitLabDraftPrefix = "Draft: "

// NewGitLabClient builds a client. apiBase defaults to https://gitlab.com/api/v4
// when empty (tests pass an httptest base ending without /api/v4 — we append it
// only for the default). token is an OAuth access token. ErrNotConfigured when
// token is empty.
func NewGitLabClient(apiBase, token string) (*GitLabClient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrNotConfigured
	}
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = "https://gitlab.com/api/v4"
	}
	return &GitLabClient{apiBase: apiBase, token: token, http: safehttp.NewProviderClient(apiBase, 15*time.Second)}, nil
}

type gitlabMR struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"` // opened|closed|merged|locked
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

func (c *GitLabClient) auth() string { return "Bearer " + c.token }

// projectPath url-encodes "owner/name" into the ":id" segment GitLab expects.
func projectPath(owner, repo string) string { return url.PathEscape(owner + "/" + repo) }

func gitlabState(s string) string {
	switch strings.ToLower(s) {
	case "opened", "open":
		return "open"
	case "merged":
		return "merged"
	case "closed", "locked":
		return "closed"
	default:
		return strings.ToLower(s)
	}
}

func (c *GitLabClient) FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error) {
	u := fmt.Sprintf("%s/projects/%s/merge_requests?state=opened&source_branch=%s",
		c.apiBase, projectPath(owner, repo), url.QueryEscape(head))
	var mrs []gitlabMR
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), "application/json", nil, &mrs); err != nil {
		return nil, err
	}
	for _, m := range mrs {
		if m.SourceBranch == head {
			return &PR{Number: m.IID, URL: m.WebURL}, nil
		}
	}
	return nil, nil
}

func (c *GitLabClient) CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error) {
	title := in.Title
	if !strings.HasPrefix(title, GitLabDraftPrefix) {
		title = GitLabDraftPrefix + title
	}
	u := fmt.Sprintf("%s/projects/%s/merge_requests", c.apiBase, projectPath(in.Owner, in.Repo))
	body := map[string]any{
		"source_branch": in.Head, "target_branch": in.Base,
		"title": title, "description": in.Body,
	}
	var mr gitlabMR
	if err := doJSON(ctx, c.http, http.MethodPost, u, c.auth(), "application/json", body, &mr); err != nil {
		return nil, err
	}
	return &PR{Number: mr.IID, URL: mr.WebURL}, nil
}

func (c *GitLabClient) CreatePRReview(ctx context.Context, owner, repo string, prNumber int, body string) error {
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes", c.apiBase, projectPath(owner, repo), prNumber)
	return doJSON(ctx, c.http, http.MethodPost, u, c.auth(), "application/json", map[string]any{"body": body}, nil)
}

func (c *GitLabClient) PRStatus(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d", c.apiBase, projectPath(owner, repo), prNumber)
	var mr gitlabMR
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), "application/json", nil, &mr); err != nil {
		return nil, err
	}
	return &PR{Number: mr.IID, URL: mr.WebURL, State: gitlabState(mr.State)}, nil
}

func (c *GitLabClient) PRByNumber(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d", c.apiBase, projectPath(owner, repo), prNumber)
	var mr gitlabMR
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), "application/json", nil, &mr); err != nil {
		return nil, err
	}
	return &PR{Number: mr.IID, URL: mr.WebURL, State: gitlabState(mr.State),
		HeadRef: mr.SourceBranch, BaseRef: mr.TargetBranch}, nil
}

func (c *GitLabClient) CreateIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes", c.apiBase, projectPath(owner, repo), issueNumber)
	return doJSON(ctx, c.http, http.MethodPost, u, c.auth(), "application/json", map[string]any{"body": body}, nil)
}

// gitlabProject is the subset of GitLab's Project JSON the repo picker consumes.
type gitlabProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Description       string `json:"description"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"` // public|internal|private
	WebURL            string `json:"web_url"`
}

// ListRepos lists projects the token's user is a member of (?membership=true),
// most recently active first, with GitLab's own ?search filter. NOTE: the
// minimal read_user login scope cannot list projects — the token needs
// read_api (list) / api (MRs).
func (c *GitLabClient) ListRepos(ctx context.Context, query string, page, limit int) ([]Repo, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	u := fmt.Sprintf("%s/projects?membership=true&per_page=%d&page=%d&order_by=last_activity_at&sort=desc&search=%s",
		c.apiBase, limit, page, url.QueryEscape(query))
	var raw []gitlabProject
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), "application/json", nil, &raw); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(raw))
	for _, p := range raw {
		repos = append(repos, Repo{
			ID: p.ID, FullName: p.PathWithNamespace, Description: p.Description,
			DefaultBranch: p.DefaultBranch, Private: p.Visibility == "private", HTMLURL: p.WebURL,
		})
	}
	return repos, nil
}

// EnsureCommentWebhook idempotently registers the @mention MR-comment webhook on
// a project (F13). It lists the project's hooks and creates one only when no hook
// with the same target URL exists (GitLab returns the target under url; the token
// is never read back, so the URL is the identity key — same convention as the
// gitea/github clients). note_events=true delivers the "Note Hook" GitLab fires
// for MR comments; the shared secret goes in `token`, which GitLab echoes back as
// the X-Gitlab-Token header the receiver checks.
func (c *GitLabClient) EnsureCommentWebhook(ctx context.Context, owner, repo, hookURL, secret string) error {
	return c.ensureWebhook(ctx, owner, repo, hookURL, secret, false)
}

// EnsureSCMWebhook reconciles the complete hook event set required by the
// unified SCM Automation model. GitLab uses booleans rather than an event list;
// this includes only standard, non-administrative repository events. It is
// safe to call whenever an enabled SCM Automation is created or edited.
func (c *GitLabClient) EnsureSCMWebhook(ctx context.Context, owner, repo, hookURL, secret string) error {
	return c.ensureWebhook(ctx, owner, repo, hookURL, secret, true)
}

func (c *GitLabClient) ensureWebhook(ctx context.Context, owner, repo, hookURL, secret string, full bool) error {
	listURL := fmt.Sprintf("%s/projects/%s/hooks", c.apiBase, projectPath(owner, repo))
	var hooks []struct {
		ID                  int64  `json:"id"`
		URL                 string `json:"url"`
		PushEvents          bool   `json:"push_events"`
		MergeRequestsEvents bool   `json:"merge_requests_events"`
		NoteEvents          bool   `json:"note_events"`
		IssuesEvents        bool   `json:"issues_events"`
		PipelineEvents      bool   `json:"pipeline_events"`
		TagPushEvents       bool   `json:"tag_push_events"`
		ReleasesEvents      bool   `json:"releases_events"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, listURL, c.auth(), "application/json", nil, &hooks); err != nil {
		return err
	}
	for _, h := range hooks {
		if h.URL == hookURL {
			if !full || (h.PushEvents && h.MergeRequestsEvents && h.NoteEvents && h.IssuesEvents && h.PipelineEvents && h.TagPushEvents && h.ReleasesEvents) {
				return nil // already registered with everything this Automation needs
			}
			if h.ID == 0 {
				return fmt.Errorf("existing webhook at target URL has no provider id")
			}
			return doJSON(ctx, c.http, http.MethodPut, listURL+"/"+fmt.Sprint(h.ID), c.auth(), "application/json", gitLabWebhookBody(hookURL, secret, true), nil)
		}
	}
	return doJSON(ctx, c.http, http.MethodPost, listURL, c.auth(), "application/json", gitLabWebhookBody(hookURL, secret, full), nil)
}

func gitLabWebhookBody(hookURL, secret string, full bool) map[string]any {
	body := map[string]any{"url": hookURL, "note_events": true, "token": secret}
	if full {
		body["push_events"] = true
		body["merge_requests_events"] = true
		body["issues_events"] = true
		body["pipeline_events"] = true
		body["tag_push_events"] = true
		body["releases_events"] = true
	}
	return body
}

// DeleteSCMWebhook removes only the hook registered for this Cloud public URL.
// It is idempotent: a missing hook already satisfies the lifecycle contract.
func (c *GitLabClient) DeleteSCMWebhook(ctx context.Context, owner, repo, hookURL string) error {
	listURL := fmt.Sprintf("%s/projects/%s/hooks", c.apiBase, projectPath(owner, repo))
	var hooks []struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, listURL, c.auth(), "application/json", nil, &hooks); err != nil {
		return err
	}
	for _, h := range hooks {
		if h.URL != hookURL {
			continue
		}
		if h.ID == 0 {
			return fmt.Errorf("existing webhook at target URL has no provider id")
		}
		return doJSON(ctx, c.http, http.MethodDelete, listURL+"/"+fmt.Sprint(h.ID), c.auth(), "application/json", nil, nil)
	}
	return nil
}

// CurrentUser returns the token account's username (GET /user; D19 / F5).
func (c *GitLabClient) CurrentUser(ctx context.Context) (string, error) {
	u := fmt.Sprintf("%s/user", c.apiBase)
	var out struct {
		Username string `json:"username"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), "application/json", nil, &out); err != nil {
		return "", err
	}
	return out.Username, nil
}

var _ Provider = (*GitLabClient)(nil)
var _ RepoLister = (*GitLabClient)(nil)
var _ CurrentUser = (*GitLabClient)(nil)
var _ SCMWebhookManager = (*GitLabClient)(nil)
