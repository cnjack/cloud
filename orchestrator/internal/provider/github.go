package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/safehttp"
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
	return &GitHubClient{apiBase: apiBase, token: token, http: safehttp.NewProviderClient(apiBase, 15*time.Second)}, nil
}

type githubPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Draft   bool   `json:"draft"`
	NodeID  string `json:"node_id"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

type githubIssueComment struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	User    struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	PerformedViaGitHubApp *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
}

func (c githubIssueComment) issueComment() (*IssueComment, error) {
	var authorAppID int64
	if c.PerformedViaGitHubApp != nil && c.PerformedViaGitHubApp.ID > 0 {
		authorAppID = c.PerformedViaGitHubApp.ID
	}
	return newIssueComment(c.ID, c.HTMLURL, c.Body, c.User.ID, c.User.Login, authorAppID)
}

func (c *GitHubClient) auth() string   { return "Bearer " + c.token }
func (c *GitHubClient) accept() string { return "application/vnd.github+json" }

func (c *GitHubClient) FindOpenPRByHead(ctx context.Context, owner, repo, head string) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=50", c.apiBase, owner, repo)
	var prs []githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &prs); err != nil {
		return nil, err
	}
	var found *PR
	for _, p := range prs {
		if p.Head.Ref == head {
			if found != nil {
				return nil, ErrMultipleOpenPRs
			}
			found = &PR{Number: p.Number, URL: p.HTMLURL, Title: p.Title, State: prState(p.State, p.Merged), Draft: p.Draft}
		}
	}
	return found, nil
}

func (c *GitHubClient) CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error) {
	return c.CreatePR(ctx, CreatePRInput{Owner: in.Owner, Repo: in.Repo, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, Draft: true})
}

func (c *GitHubClient) CreatePR(ctx context.Context, in CreatePRInput) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", c.apiBase, in.Owner, in.Repo)
	body := map[string]any{"title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body, "draft": in.Draft}
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(), body, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, State: prState(pr.State, pr.Merged), Draft: in.Draft}, nil
}

// MarkPRReady uses GitHub's GraphQL mutation; the REST update endpoint does not
// expose the Draft -> Ready transition. The preceding REST read supplies the
// node id and makes already-ready/closed/merged calls idempotent.
func (c *GitHubClient) MarkPRReady(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, prNumber)
	var current githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &current); err != nil {
		return nil, err
	}
	out := &PR{Number: current.Number, URL: current.HTMLURL, Title: current.Title, State: prState(current.State, current.Merged), Draft: current.Draft}
	if out.State != "open" || !out.Draft {
		return out, nil
	}
	if current.NodeID == "" {
		return nil, fmt.Errorf("github pull request has no node_id")
	}
	graphqlURL := c.apiBase + "/graphql"
	if strings.HasSuffix(c.apiBase, "/api/v3") {
		graphqlURL = strings.TrimSuffix(c.apiBase, "/api/v3") + "/api/graphql"
	}
	var response struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	body := map[string]any{
		"query":     "mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}",
		"variables": map[string]any{"id": current.NodeID},
	}
	if err := doJSON(ctx, c.http, http.MethodPost, graphqlURL, c.auth(), c.accept(), body, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("github mark ready: %s", response.Errors[0].Message)
	}
	out.Draft = false
	return out, nil
}

func (c *GitHubClient) CreatePRReview(ctx context.Context, owner, repo string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.apiBase, owner, repo, prNumber)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(),
		map[string]any{"event": "COMMENT", "body": body}, nil)
}

func (c *GitHubClient) CreatePRReviewBatch(ctx context.Context, owner, repo string, prNumber int, review PRReview) error {
	comments := make([]map[string]any, 0, len(review.Comments))
	for _, comment := range review.Comments {
		item := map[string]any{
			"path": comment.Path, "line": comment.Line, "side": "RIGHT", "body": comment.Body,
		}
		if comment.EndLine > comment.Line {
			item["start_line"] = comment.Line
			item["start_side"] = "RIGHT"
			item["line"] = comment.EndLine
		}
		comments = append(comments, item)
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.apiBase, owner, repo, prNumber)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(),
		map[string]any{"event": "COMMENT", "body": review.Body, "comments": comments}, nil)
}

func (c *GitHubClient) PRStatus(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, prNumber)
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, State: prState(pr.State, pr.Merged), Draft: pr.Draft}, nil
}

func (c *GitHubClient) PRByNumber(ctx context.Context, owner, repo string, prNumber int) (*PR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, prNumber)
	var pr githubPR
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &pr); err != nil {
		return nil, err
	}
	return &PR{Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, State: prState(pr.State, pr.Merged), Draft: pr.Draft,
		HeadRef: pr.Head.Ref, BaseRef: pr.Base.Ref, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA}, nil
}

func (c *GitHubClient) CreateIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.apiBase, owner, repo, issueNumber)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(), map[string]any{"body": body}, nil)
}

func (c *GitHubClient) CreateManagedIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) (*IssueComment, error) {
	u := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.apiBase, escapedRepositoryPath(owner, repo), issueNumber)
	var out githubIssueComment
	if err := doJSON(ctx, c.http, http.MethodPost, u, c.auth(), c.accept(), map[string]any{"body": body}, &out); err != nil {
		return nil, err
	}
	return out.issueComment()
}

func (c *GitHubClient) UpdateIssueComment(ctx context.Context, owner, repo string, _ int, commentID, body string) (*IssueComment, error) {
	id, err := parseIssueCommentID(commentID)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/repos/%s/issues/comments/%d", c.apiBase, escapedRepositoryPath(owner, repo), id)
	var out githubIssueComment
	if err := doJSON(ctx, c.http, http.MethodPatch, u, c.auth(), c.accept(), map[string]any{"body": body}, &out); err != nil {
		return nil, err
	}
	return out.issueComment()
}

func (c *GitHubClient) ListIssueComments(ctx context.Context, owner, repo string, issueNumber, limit int) ([]IssueComment, error) {
	limit = managedIssueCommentLimit(limit)
	const pageSize = managedIssueCommentPageSize
	baseURL := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.apiBase, escapedRepositoryPath(owner, repo), issueNumber)
	fetchPage := func(page int) ([]githubIssueComment, http.Header, error) {
		u := fmt.Sprintf("%s?per_page=%d&page=%d", baseURL, pageSize, page)
		var pageComments []githubIssueComment
		header, err := doJSONResponseWithLimit(ctx, c.http, http.MethodGet, u, c.auth(), c.accept(), nil, &pageComments, maxManagedIssueCommentJSONResponseBytes)
		return pageComments, header, err
	}

	firstPage, header, err := fetchPage(1)
	if err != nil {
		return nil, err
	}
	lastPage, err := providerLastPage(header, pageSize)
	if err != nil {
		return nil, err
	}
	raw := make([]githubIssueComment, 0, limit)
	retainedBodyBytes := 0
	appendPage := func(pageComments []githubIssueComment) error {
		for _, comment := range pageComments {
			var budgetErr error
			retainedBodyBytes, budgetErr = addManagedIssueCommentBodyBytes(retainedBodyBytes, comment.Body)
			if budgetErr != nil {
				return budgetErr
			}
			raw = append(raw, comment)
		}
		return nil
	}
	if lastPage > 1 {
		fetches := 1 // page 1 was needed to discover the provider's last page.
		for page := lastPage; page >= 1 && len(raw) < limit; page-- {
			pageComments := firstPage
			if page != 1 {
				if fetches >= maxManagedIssueCommentPageFetches {
					break
				}
				pageComments, _, err = fetchPage(page)
				fetches++
				if err != nil {
					return nil, err
				}
			}
			if err = appendPage(pageComments); err != nil {
				return nil, err
			}
		}
	} else if err = appendPage(firstPage); err != nil {
		return nil, err
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].ID > raw[j].ID })
	if len(raw) > limit {
		raw = raw[:limit]
	}
	comments := make([]IssueComment, 0, len(raw))
	for _, item := range raw {
		comment, err := item.issueComment()
		if err != nil {
			return nil, err
		}
		comments = append(comments, *comment)
	}
	return comments, nil
}

func (c *GitHubClient) CreateIssueCommentReaction(ctx context.Context, owner, repo string, commentID int64, content string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d/reactions", c.apiBase, owner, repo, commentID)
	return doJSON(ctx, c.http, http.MethodPost, url, c.auth(), c.accept(), map[string]any{"content": content}, nil)
}

// githubRepo is the subset of GitHub's Repository JSON the repo picker consumes.
type githubRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}

// ListRepos lists the token user's repositories (/user/repos: owned +
// collaborator + org-member), most recently pushed first. GitHub has no q param
// on this endpoint, so `query` is applied as a client-side substring filter on
// full_name — good enough for a picker, and it avoids the search API's separate
// rate limits. NOTE: listing PRIVATE repos requires the token to carry `repo`
// scope; the minimal read:user login token only surfaces public ones.
func (c *GitHubClient) ListRepos(ctx context.Context, query string, page, limit int) ([]Repo, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	url := fmt.Sprintf("%s/user/repos?per_page=%d&page=%d&sort=pushed&direction=desc", c.apiBase, limit, page)
	var raw []githubRepo
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &raw); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	repos := make([]Repo, 0, len(raw))
	for _, r := range raw {
		if q != "" && !strings.Contains(strings.ToLower(r.FullName), q) {
			continue
		}
		repos = append(repos, Repo{
			ID: r.ID, FullName: r.FullName, Description: r.Description,
			DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL,
		})
	}
	return repos, nil
}

// ListInstallationRepos lists only repositories granted to the GitHub App
// installation represented by c.token. Installation access tokens are not user
// tokens and GitHub rejects them on /user/repos.
func (c *GitHubClient) ListInstallationRepos(ctx context.Context, query string, page, limit int) ([]Repo, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	url := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", c.apiBase, limit, page)
	var body struct {
		Repositories []githubRepo `json:"repositories"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &body); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	repos := make([]Repo, 0, len(body.Repositories))
	for _, r := range body.Repositories {
		if q != "" && !strings.Contains(strings.ToLower(r.FullName), q) {
			continue
		}
		repos = append(repos, Repo{
			ID: r.ID, FullName: r.FullName, Description: r.Description,
			DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL,
		})
	}
	return repos, nil
}

// ListBranches lists repository branches visible to this credential. GitHub's
// endpoint is paginated and has no useful search filter, so callers own any
// UI-side filtering without ever expanding the credential's repository scope.
func (c *GitHubClient) ListBranches(ctx context.Context, owner, repo string, page, limit int) ([]Branch, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	u := fmt.Sprintf("%s/repos/%s/%s/branches?per_page=%d&page=%d", c.apiBase, owner, repo, limit, page)
	var raw []struct {
		Name      string `json:"name"`
		Protected bool   `json:"protected"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, u, c.auth(), c.accept(), nil, &raw); err != nil {
		return nil, err
	}
	branches := make([]Branch, 0, len(raw))
	for _, branch := range raw {
		if strings.TrimSpace(branch.Name) != "" {
			branches = append(branches, Branch{Name: branch.Name, Protected: branch.Protected})
		}
	}
	return branches, nil
}

// EnsureCommentWebhook idempotently registers the @mention PR-comment webhook on
// a repository (F13). It lists the repo's hooks and creates one only when no hook
// with the same target URL exists (GitHub returns the target under config.url;
// the secret is never read back, so the URL is the identity key — same convention
// as the gitea client). The event set is [issue_comment]: GitHub delivers PR
// conversation comments as issue_comment with issue.pull_request populated (the
// receiver filters on that), so a single event covers the @jcode trigger.
func (c *GitHubClient) EnsureCommentWebhook(ctx context.Context, owner, repo, hookURL, secret string) error {
	listURL := fmt.Sprintf("%s/repos/%s/%s/hooks", c.apiBase, owner, repo)
	var hooks []struct {
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, listURL, c.auth(), c.accept(), nil, &hooks); err != nil {
		return err
	}
	for _, h := range hooks {
		if h.Config.URL == hookURL {
			return nil // already registered
		}
	}
	body := map[string]any{
		"name":   "web",
		"active": true,
		"events": []string{"issue_comment"},
		"config": map[string]any{
			"url":          hookURL,
			"content_type": "json",
			"secret":       secret,
		},
	}
	return doJSON(ctx, c.http, http.MethodPost, listURL, c.auth(), c.accept(), body, nil)
}

// CurrentUser returns the token account's login (GET /user; D19 / F5).
func (c *GitHubClient) CurrentUser(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/user", c.apiBase)
	var u struct {
		Login string `json:"login"`
	}
	if err := doJSON(ctx, c.http, http.MethodGet, url, c.auth(), c.accept(), nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

var _ Provider = (*GitHubClient)(nil)
var _ ManagedIssueCommentProvider = (*GitHubClient)(nil)
var _ RepoLister = (*GitHubClient)(nil)
var _ BranchLister = (*GitHubClient)(nil)
var _ CurrentUser = (*GitHubClient)(nil)
