package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// TestIsBlockedIP asserts the model-provider dial guard's blocklist: every
// internal/reserved range an SSRF oracle would target is blocked, ordinary public
// IPs are allowed, and an invalid address fails closed.
func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.5.5.5", "::1",
		"169.254.169.254",        // cloud metadata
		"169.254.1.1", "fe80::1", // link-local
		"10.0.0.5", "172.16.9.9", "192.168.1.1", // RFC1918
		"fc00::1", "fd12:3456::1", // unique-local
		"0.0.0.0", "::", // unspecified
		"::ffff:10.0.0.1", // IPv4-mapped private
	}
	for _, s := range blocked {
		if !isBlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("isBlockedIP(%s) = false, want blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "203.0.113.7", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if isBlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("isBlockedIP(%s) = true, want allowed", s)
		}
	}
	if !isBlockedIP(netip.Addr{}) {
		t.Error("the zero addr should fail closed (blocked)")
	}
}

// TestGuardedDialContext proves the guard refuses a loopback dial with
// errBlockedHost when on, and lets the same dial through when allowPrivate is set.
func TestGuardedDialContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String() // 127.0.0.1:PORT

	dialOn := guardedDialContext(func() bool { return false })
	if _, err := dialOn(context.Background(), "tcp", addr); !errors.Is(err, errBlockedHost) {
		t.Fatalf("guarded dial to %s: err=%v, want errBlockedHost", addr, err)
	}

	dialOff := guardedDialContext(func() bool { return true })
	conn, err := dialOff(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("unguarded dial to %s: %v", addr, err)
	}
	conn.Close()
}

// catalogServerGuarded builds a catalog server with the SSRF guard ON (unlike
// catalogServer, which opts out for its httptest upstream). Used to prove the
// probe refuses a loopback target.
func catalogServerGuarded(t *testing.T) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken, AuthTokenKey: validTokenKey(t)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// TestModelProviderVerifyBlocksLoopbackSSRF is the end-to-end FIX B assertion: a
// verify probe whose base_url resolves to a loopback IP is refused by the dial
// guard and surfaces as a generic provider_unreachable — never a success, and
// never leaking the internal IP into the error.
func TestModelProviderVerifyBlocksLoopbackSSRF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"x"}]}`)
	}))
	t.Cleanup(upstream.Close)

	ts, st := catalogServerGuarded(t)
	_ = mkUser(t, st, "admin")
	provider := createProvider(t, ts, map[string]any{
		"name": "Loopback", "kind": "openai", "base_url": upstream.URL + "/v1",
		"auth_type": "none", "catalog_mode": "auto",
	})

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/model-providers/"+provider.ID+"/verify", consoleToken, nil)
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("verify loopback: status=%d want 502 body=%s", resp.StatusCode, body)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "provider_unreachable" {
		t.Fatalf("code=%q want provider_unreachable", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "127.0.0.1") {
		t.Fatalf("error message leaked the internal IP: %q", body.Error.Message)
	}
}
