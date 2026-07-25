package safehttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestProviderBlockedIPPreservesPrivateButRejectsUnsafeRanges(t *testing.T) {
	for _, raw := range []string{"10.0.0.1", "172.16.1.1", "192.168.1.1", "fd00::1"} {
		if providerBlockedIP(mustAddr(t, raw), false) {
			t.Errorf("private self-hosted address %s was blocked", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1"} {
		if !providerBlockedIP(mustAddr(t, raw), false) {
			t.Errorf("unsafe address %s was allowed", raw)
		}
	}
	if providerBlockedIP(mustAddr(t, "127.0.0.1"), true) {
		t.Fatal("explicit localhost development URL should allow loopback")
	}
}

func TestProviderClientAllowsOnlyExplicitHTTPLocalhostLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	allowed := NewProviderClient(server.URL, time.Second)
	resp, err := allowed.Get(server.URL)
	if err != nil {
		t.Fatalf("explicit local development URL failed: %v", err)
	}
	resp.Body.Close()

	blocked := NewProviderClient("https://provider.example", time.Second)
	_, err = blocked.Get(server.URL)
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("non-local provider client error=%v want blocked destination", err)
	}
}

func TestProviderClientDisablesEnvironmentProxy(t *testing.T) {
	client := NewProviderClient("https://provider.example", time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("provider traffic must not use an environment proxy that bypasses the dial guard")
	}
}

func TestProviderClientRejectsRedirects(t *testing.T) {
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := NewProviderClient(redirector.URL, time.Second)
	_, err := client.Get(redirector.URL)
	if !errors.Is(err, ErrRedirectDenied) {
		t.Fatalf("redirect error=%v want ErrRedirectDenied", err)
	}
	if targetHits != 0 {
		t.Fatalf("redirect target received %d requests", targetHits)
	}
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ip
}

// Compile-time check that the guard is applied at the actual dial path, not
// only by validation at construction time.
func TestGuardedDialContextBlocksMetadata(t *testing.T) {
	_, err := guardedDialContext(false)(context.Background(), "tcp", "169.254.169.254:80")
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("metadata dial error=%v want ErrBlockedDestination", err)
	}
}
