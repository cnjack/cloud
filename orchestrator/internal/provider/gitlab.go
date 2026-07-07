package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	return &GitLabClient{apiBase: apiBase, token: token, http: &http.Client{Timeout: 15 * time.Second}}, nil
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

var _ Provider = (*GitLabClient)(nil)
