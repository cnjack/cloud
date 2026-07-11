// Package jtypeoauth is a thin client for jtype's RFC 8628 OAuth 2.0 Device
// Authorization Grant — the two UNauthenticated endpoints the "Connect with
// jtype" console button drives to mint a kanban credential without a hand-pasted
// PAT (D28):
//
//   - POST {base}/api/oauth/device_authorization — start a flow, returning a
//     device_code (SECRET) + a 6-digit user_code + a verification URI the browser
//     opens to approve.
//   - POST {base}/api/oauth/token — poll the device_code grant until the user
//     approves (→ a Bearer access_token) or the flow expires.
//
// The browser leg (login + approve) is handled entirely by jtype's own
// /oauth/device SPA page; jcloud only calls these two endpoints.
//
// CRITICAL — transport is application/x-www-form-urlencoded (jtype's axum
// `Form` extractor), NOT JSON, so the JSON-only internal/jtype.Client cannot be
// reused (D28 §0). jtype's device grant ignores client_id/scope entirely
// (oauth.rs:247-249,310-367), so this client registers no client and persists no
// client_id.
//
// Security: this package NEVER logs — the device_code and access_token are
// credentials (either can mint/be the token). Callers log only non-secret
// context (connect_id, base URL host, status). See D28 §5.
package jtypeoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one jtype instance's OAuth device endpoints. The base URL is
// the SAME root as the document API (the resolved cluster/effective base URL —
// no new config, D28 §1.1); only the two /api/oauth/* paths are exercised.
type Client struct {
	baseURL string // e.g. http://127.0.0.1:13345 (no trailing slash)
	http    *http.Client
}

// NewClient builds a Client. baseURL is trimmed of trailing slashes. A nil hc
// yields a default client with a 20s timeout (matching internal/jtype).
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// DeviceAuth is the device_authorization response (RFC 8628 §3.2). DeviceCode is
// a SECRET — it can mint the token — and must never reach the browser (D28 §1.2).
type DeviceAuth struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int // seconds until the flow expires (jtype: 600)
	Interval                int // minimum seconds between polls (jtype: 2)
}

// Token is a minted access token (device grant success). jtype mints a 90-day
// scoped session (create_scoped_session, scope=mcp) with NO refresh_token
// (MCP_TOKEN_TTL_SECS=90d); it works as a Bearer on the document API.
type Token struct {
	AccessToken string
	ExpiresIn   int64 // seconds until the token expires (jtype: 7776000 = 90d)
}

// Status classifies a device-grant poll (a non-error terminal or interim state).
type Status int

const (
	// StatusPending: the user has not approved yet (authorization_pending) — keep
	// polling.
	StatusPending Status = iota
	// StatusSlowDown: the poll cadence is too fast (slow_down) — back off, then
	// keep polling. Handled defensively: jtype's device grant does not emit it
	// today (D28 §0 F2), but a stricter deployment might.
	StatusSlowDown
	// StatusComplete: the user approved; a Token was minted (terminal success).
	StatusComplete
	// StatusExpired: the flow expired (expired_token) or the device_code is no
	// longer valid (invalid_grant) — terminal. jtype has no explicit "denied"
	// browser action, so a user who never approves ends here (D28 §0 F2).
	StatusExpired
	// StatusDenied: the user rejected the request (access_denied) — terminal.
	// Handled defensively: jtype's approve-only page cannot produce it today.
	StatusDenied
)

// ErrOAuthUnsupported means the base URL does not serve the device-flow routes
// (an older jtype without MCP OAuth): the device_authorization/token path 404s
// or returns a non-JSON page. Callers surface it as a typed jtype_oauth_unsupported
// and fall back to the hand-pasted-PAT path — never a silent mock (D28 §5).
var ErrOAuthUnsupported = errors.New("jtype: OAuth device flow not supported at this base URL")

// StartDeviceAuthorization begins a device flow (form POST, no client_id). A
// 404/405 (route absent) or a non-JSON / token-less body means an old jtype with
// no OAuth support → ErrOAuthUnsupported. A 5xx or transport failure is a plain
// (transient, non-terminal) error the caller can retry.
func (c *Client) StartDeviceAuthorization(ctx context.Context) (*DeviceAuth, error) {
	resp, err := c.postForm(ctx, "/api/oauth/device_authorization", url.Values{})
	if err != nil {
		return nil, fmt.Errorf("jtype oauth: device_authorization: %w", err) // transient
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, ErrOAuthUnsupported
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("jtype oauth: device_authorization: server status %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		// A 4xx here is not the RFC pending/slow_down loop (that is the token
		// endpoint); an old jtype route may also answer 400 for an unknown path.
		return nil, ErrOAuthUnsupported
	}
	var raw struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil || raw.DeviceCode == "" || raw.UserCode == "" {
		// 2xx but not a device_authorization payload (e.g. an SPA HTML page) →
		// the route is not the OAuth endpoint. Treat as unsupported, never a guess.
		return nil, ErrOAuthUnsupported
	}
	return &DeviceAuth{
		DeviceCode:              raw.DeviceCode,
		UserCode:                raw.UserCode,
		VerificationURI:         raw.VerificationURI,
		VerificationURIComplete: raw.VerificationURIComplete,
		ExpiresIn:               raw.ExpiresIn,
		Interval:                raw.Interval,
	}, nil
}

// PollToken polls the device_code grant once. Mapping (D28 §1.1):
//
//	200                    → (token, StatusComplete, nil)
//	authorization_pending  → StatusPending
//	slow_down              → StatusSlowDown   (defensive)
//	expired_token          → StatusExpired    (terminal)
//	invalid_grant          → StatusExpired    (terminal; device_code consumed/gone)
//	access_denied          → StatusDenied     (defensive terminal)
//	404 / route absent     → ErrOAuthUnsupported
//	5xx / transport / other→ a plain (transient, non-terminal) error
//
// On a transient error the caller keeps the flow pending; on ErrOAuthUnsupported
// or a terminal status it marks the flow terminal — never a silent success.
func (c *Client) PollToken(ctx context.Context, deviceCode string) (*Token, Status, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	resp, err := c.postForm(ctx, "/api/oauth/token", form)
	if err != nil {
		return nil, 0, fmt.Errorf("jtype oauth: token: %w", err) // transient (transport)
	}
	defer drain(resp)

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, 0, ErrOAuthUnsupported
	case resp.StatusCode == http.StatusOK:
		var raw struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil || raw.AccessToken == "" {
			return nil, 0, fmt.Errorf("jtype oauth: token: 200 without an access_token")
		}
		return &Token{AccessToken: raw.AccessToken, ExpiresIn: raw.ExpiresIn}, StatusComplete, nil
	case resp.StatusCode >= 500:
		return nil, 0, fmt.Errorf("jtype oauth: token: server status %d", resp.StatusCode) // transient
	}

	// 4xx: RFC 8628 error envelope {"error": code}.
	switch readErrorCode(resp) {
	case "authorization_pending":
		return nil, StatusPending, nil
	case "slow_down":
		return nil, StatusSlowDown, nil
	case "expired_token", "invalid_grant":
		return nil, StatusExpired, nil
	case "access_denied":
		return nil, StatusDenied, nil
	default:
		// invalid_request or any unrecognised code: our request should be
		// well-formed, so this is unexpected. Surface it (transient, non-terminal)
		// rather than faking a status — the flow's own expiry eventually reaps it.
		return nil, 0, fmt.Errorf("jtype oauth: token: unexpected status %d", resp.StatusCode)
	}
}

// postForm issues an application/x-www-form-urlencoded POST to a path under the
// base URL and returns the raw response (caller drains/closes it).
func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

// readErrorCode extracts the RFC 8628 error code from a 4xx body ({"error":
// code}); "" when absent/unparseable.
func readErrorCode(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		return e.Error
	}
	return ""
}

// drain reads and closes a response body so the HTTP connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
