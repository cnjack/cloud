package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GitHubClient talks to the GitHub REST API (api.github.com or, for the unit
// path, an httptest base). It implements Provider. GitHub is NOT integration
// tested locally (blueprint §2: only gitea is exercised for real); this is the
// httptest-covered implementation so the multi-provider seam is complete.
type GitHubClient struct {
	apiBase string // e.g. https://api.github.com
	token   string
	http    *http.Client
}

// NewGitHubClient builds a client. apiBase defaults to https://api.github.com
// when empty (tests pass an httptest URL). token is an OAuth access token / PAT
// with repo scope. Returns ErrNotConfigured when token is empty.
func NewGitHubClient(apiBase, token string) (*GitHubClient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrNotConfigured
	}
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &GitHubClient{apiBase: apiBase, token: token, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

type githubPR struct {
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

func (c *GitHubClient) auth() string   { return "Bearer " + c.token }
func (c *GitHubClient) accept() string { return "application/vnd.github+json" }

func (c *GitHubClient) FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=50", c.apiBase, owner, repo)
	var prs []githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &prs); err != nil {
		return nil, err
	}
	for _, p := range prs {
		if p.Head.Ref == head {
			return &PR{Number: p.Number, URL: p.HTMLURL}, nil
		}
	}
	return nil, nil
}

func (c *GitHubClient) CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", c.apiBase, in.Owner, in.Repo)
	body := map[string]any{"title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body, "draft": true}
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(), body, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL}, nil
}

func (c *GitHubClient) CreatePRReview(ctx context.Context, owner, repo string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.apiBase, owner, repo, prNumber)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(),
		map[string]any{"event": "COMMENT", "body": body}, nil)
}

func (c *GitHubClient) PRStatus(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, prNumber)
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, State: prState(pr.State, pr.Merged)}, nil
}

func (c *GitHubClient) PRByNumber(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, prNumber)
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, State: prState(pr.State, pr.Merged),
		HeadRef: pr.Head.Ref, BaseRef: pr.Base.Ref}, nil
}

func (c *GitHubClient) CreateIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.apiBase, owner, repo, issueNumber)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(), map[string]any{"body": body}, nil)
}

var _ Provider = (*GitHubClient)(nil)
