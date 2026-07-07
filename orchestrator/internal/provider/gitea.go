package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// GiteaClient talks to a Gitea instance's REST API using a personal access
// token. It implements Provider. It is intentionally tiny: two calls, stdlib
// only, matching the orchestrator's std-lib-first posture.
type GiteaClient struct {
	baseURL string // e.g. http://gitea.jcloud.svc.cluster.local:3000
	token   string
	http    *http.Client
}

// NewGiteaClient builds a client. baseURL must be the Gitea root (no /api/v1
// suffix); token is a personal access token with repo write scope. Returns
// ErrNotConfigured if either is empty so the caller can degrade gracefully.
func NewGiteaClient(baseURL, token string) (*GiteaClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil, ErrNotConfigured
	}
	return &GiteaClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// giteaPR is the subset of Gitea's PullRequest JSON we consume.
type giteaPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
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
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
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
