package jtypeoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cnjack/jcloud/internal/safehttp"
)

// fakeJtypeOAuth is a tiny stand-in for jtype's two device-flow endpoints. It
// mirrors internal/jtype/client_test.go's httptest+ServeMux fake, asserting the
// FORM transport and switching the token endpoint's mode so a single server can
// drive pending → approved (and the terminal-error variants). Everything is
// guarded by a mutex so a test can flip the mode between polls.
type fakeJtypeOAuth struct {
	mux *http.ServeMux

	mu               sync.Mutex
	tokenMode        string // "pending" | "approved" | "expired" | "invalid_grant" | "slow_down" | "denied"
	formOK           bool   // the last request decoded as a form (not JSON)
	lastGrant        string // grant_type of the last token poll
	lastScope        string
	lastClientID     string
	lastClientSecret string
	responseScope    string
}

func newFakeJtypeOAuth() *fakeJtypeOAuth {
	f := &fakeJtypeOAuth{mux: http.NewServeMux(), tokenMode: "pending"}
	f.mux.HandleFunc("/api/oauth/device_authorization", f.handleDeviceAuth)
	f.mux.HandleFunc("/api/oauth/token", f.handleToken)
	return f
}

func (f *fakeJtypeOAuth) setMode(mode string) {
	f.mu.Lock()
	f.tokenMode = mode
	f.mu.Unlock()
}

func (f *fakeJtypeOAuth) handleDeviceAuth(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	// The client MUST send application/x-www-form-urlencoded, never JSON.
	f.mu.Lock()
	f.formOK = strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	f.lastScope = r.PostForm.Get("scope")
	f.lastClientID = r.PostForm.Get("client_id")
	f.lastClientSecret = r.PostForm.Get("client_secret")
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               "dev-secret-123",
		"user_code":                 "482913",
		"verification_uri":          "http://jtype.test/oauth/device",
		"verification_uri_complete": "http://jtype.test/oauth/device?code=482913",
		"expires_in":                600,
		"interval":                  2,
	})
}

func (f *fakeJtypeOAuth) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.lastGrant = r.PostForm.Get("grant_type")
	f.lastClientID = r.PostForm.Get("client_id")
	f.lastClientSecret = r.PostForm.Get("client_secret")
	mode := f.tokenMode
	f.mu.Unlock()

	switch mode {
	case "approved":
		scope := f.responseScope
		if scope == "" {
			scope = "mcp"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "minted-token", "token_type": "Bearer",
			"expires_in": 7776000, "scope": scope,
		})
	case "expired":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
	case "invalid_grant":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	case "slow_down":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slow_down"})
	case "denied":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
	default: // pending
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Test 1: Start parses every field and sends form encoding (not JSON).
func TestStartDeviceAuthorization(t *testing.T) {
	f := newFakeJtypeOAuth()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	da, err := c.StartDeviceAuthorization(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if da.DeviceCode != "dev-secret-123" || da.UserCode != "482913" {
		t.Fatalf("device auth = %+v", da)
	}
	if da.VerificationURIComplete != "http://jtype.test/oauth/device?code=482913" {
		t.Fatalf("verification_uri_complete = %q", da.VerificationURIComplete)
	}
	if da.ExpiresIn != 600 || da.Interval != 2 {
		t.Fatalf("expires_in=%d interval=%d", da.ExpiresIn, da.Interval)
	}
	if !f.formOK {
		t.Fatal("device_authorization must be sent as application/x-www-form-urlencoded, not JSON")
	}
}

// Test 2: Poll pending → StatusPending; approved → StatusComplete carrying the
// access_token + expires_in, and the poll uses the RFC device_code grant type.
func TestPollTokenPendingThenApproved(t *testing.T) {
	f := newFakeJtypeOAuth()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	ctx := context.Background()

	if _, st, err := c.PollToken(ctx, "dev-secret-123"); err != nil || st != StatusPending {
		t.Fatalf("pending poll: status=%v err=%v", st, err)
	}
	if f.lastGrant != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("grant_type = %q", f.lastGrant)
	}

	f.setMode("approved")
	tok, st, err := c.PollToken(ctx, "dev-secret-123")
	if err != nil || st != StatusComplete {
		t.Fatalf("approved poll: status=%v err=%v", st, err)
	}
	if tok.AccessToken != "minted-token" || tok.ExpiresIn != 7776000 || tok.Scope != "mcp" {
		t.Fatalf("token = %+v", tok)
	}
}

func TestFullClientAuthenticatesStartAndExchange(t *testing.T) {
	f := newFakeJtypeOAuth()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewFullClient(srv.URL, "jcode-cloud", "top-secret", nil)
	if _, err := c.StartDeviceAuthorization(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if f.lastScope != "full" || f.lastClientID != "jcode-cloud" || f.lastClientSecret != "top-secret" {
		t.Fatalf("start form scope=%q client=%q secret=%q", f.lastScope, f.lastClientID, f.lastClientSecret)
	}

	f.setMode("approved")
	// The fake intentionally returns mcp first: a full client must reject a
	// downgraded token rather than marking the connection complete.
	if _, _, err := c.PollToken(context.Background(), "dev-secret-123"); !errors.Is(err, ErrOAuthScopeMismatch) {
		t.Fatalf("downgraded token err=%v want ErrOAuthScopeMismatch", err)
	}
	if f.lastClientID != "jcode-cloud" || f.lastClientSecret != "top-secret" {
		t.Fatalf("poll form client=%q secret=%q", f.lastClientID, f.lastClientSecret)
	}

	f.mu.Lock()
	f.responseScope = "full"
	f.mu.Unlock()
	tok, status, err := c.PollToken(context.Background(), "dev-secret-123")
	if err != nil || status != StatusComplete {
		t.Fatalf("full token poll: status=%v err=%v", status, err)
	}
	if tok.AccessToken != "minted-token" || tok.Scope != "full" {
		t.Fatalf("full token = %+v", tok)
	}
}

func TestFullClientRequiresSecret(t *testing.T) {
	c := NewFullClient("http://jtype.test", "jcode-cloud", "", nil)
	if _, err := c.StartDeviceAuthorization(context.Background()); err != ErrOAuthClientNotConfigured {
		t.Fatalf("err=%v want ErrOAuthClientNotConfigured", err)
	}
}

func TestFullClientReportsRejectedCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client"})
	}))
	defer srv.Close()
	c := NewFullClient(srv.URL, "jcode-cloud", "wrong-secret", nil)
	if _, err := c.StartDeviceAuthorization(context.Background()); !errors.Is(err, ErrOAuthClientRejected) {
		t.Fatalf("err=%v want ErrOAuthClientRejected", err)
	}
}

func TestFullClientRefusesRedirectsAndDoesNotReflectSecret(t *testing.T) {
	const clientSecret = "jtype-client-secret-must-not-leak"
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	c := NewFullClient(redirector.URL, "cloud", clientSecret, nil)
	_, err := c.StartDeviceAuthorization(context.Background())
	if !errors.Is(err, safehttp.ErrRedirectDenied) {
		t.Fatalf("redirect error=%v want ErrRedirectDenied", err)
	}
	if targetHits != 0 {
		t.Fatalf("redirect target received %d requests", targetHits)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("Bearer " + clientSecret))
	}))
	defer failing.Close()
	c = NewFullClient(failing.URL, "cloud", clientSecret, nil)
	_, err = c.StartDeviceAuthorization(context.Background())
	if err == nil || strings.Contains(err.Error(), clientSecret) {
		t.Fatalf("JType OAuth error reflected a client secret: %v", err)
	}
}

// Test 3: the terminal / interim error codes each map to their Status.
func TestPollTokenErrorCodes(t *testing.T) {
	f := newFakeJtypeOAuth()
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	ctx := context.Background()

	cases := []struct {
		mode string
		want Status
	}{
		{"expired", StatusExpired},
		{"invalid_grant", StatusExpired},
		{"slow_down", StatusSlowDown},
		{"denied", StatusDenied},
	}
	for _, tc := range cases {
		f.setMode(tc.mode)
		_, st, err := c.PollToken(ctx, "dev-secret-123")
		if err != nil || st != tc.want {
			t.Errorf("mode=%s: status=%v want=%v err=%v", tc.mode, st, tc.want, err)
		}
	}
}

// Test 4: Start against a bare 404 mux (an old jtype with no OAuth routes) →
// ErrOAuthUnsupported (typed, never a silent guess).
func TestStartUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux()) // no routes → 404 everything
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	if _, err := c.StartDeviceAuthorization(context.Background()); err != ErrOAuthUnsupported {
		t.Fatalf("start on 404 mux: err=%v want ErrOAuthUnsupported", err)
	}
	// The token endpoint likewise reports unsupported when the route is absent.
	if _, _, err := c.PollToken(context.Background(), "dev"); err != ErrOAuthUnsupported {
		t.Fatalf("poll on 404 mux: err=%v want ErrOAuthUnsupported", err)
	}
}

// Test 5: a 5xx is a transient, NON-terminal error — not ErrOAuthUnsupported and
// not a false status.
func TestPollTransient5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	_, st, err := c.PollToken(context.Background(), "dev")
	if err == nil {
		t.Fatal("5xx poll must return a (transient) error")
	}
	if err == ErrOAuthUnsupported || st == StatusComplete {
		t.Fatalf("5xx must be transient, not unsupported/complete: st=%v err=%v", st, err)
	}
	// Start against a 5xx is likewise transient (retryable), not unsupported.
	if _, serr := c.StartDeviceAuthorization(context.Background()); serr == nil || serr == ErrOAuthUnsupported {
		t.Fatalf("5xx start must be a transient error, got %v", serr)
	}
}
