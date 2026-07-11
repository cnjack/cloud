package jtypeoauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeJtypeOAuth is a tiny stand-in for jtype's two device-flow endpoints. It
// mirrors internal/jtype/client_test.go's httptest+ServeMux fake, asserting the
// FORM transport and switching the token endpoint's mode so a single server can
// drive pending → approved (and the terminal-error variants). Everything is
// guarded by a mutex so a test can flip the mode between polls.
type fakeJtypeOAuth struct {
	mux *http.ServeMux

	mu        sync.Mutex
	tokenMode string // "pending" | "approved" | "expired" | "invalid_grant" | "slow_down" | "denied"
	formOK    bool   // the last request decoded as a form (not JSON)
	lastGrant string // grant_type of the last token poll
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
	// The client MUST send application/x-www-form-urlencoded, never JSON.
	f.mu.Lock()
	f.formOK = strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
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
	mode := f.tokenMode
	f.mu.Unlock()

	switch mode {
	case "approved":
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "minted-mcp-token", "token_type": "Bearer",
			"expires_in": 7776000, "scope": "mcp",
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
	if tok.AccessToken != "minted-mcp-token" || tok.ExpiresIn != 7776000 {
		t.Fatalf("token = %+v", tok)
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
