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

func TestProviderClientIgnoresGenericEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://192.168.10.10:3128")
	t.Setenv("HTTPS_PROXY", "http://192.168.10.10:3128")
	t.Setenv("PROVIDER_HTTP_PROXY", "")
	t.Setenv("PROVIDER_HTTPS_PROXY", "")
	client := NewProviderClient("https://provider.example", time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("provider traffic must not use an environment proxy that bypasses the dial guard")
	}
}

func TestProviderClientUsesOnlyExplicitTrustedProxyWithNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://ignored.example:8080")
	t.Setenv("HTTPS_PROXY", "http://ignored.example:8080")
	t.Setenv("PROVIDER_HTTP_PROXY", "http://192.168.10.236:7890")
	t.Setenv("PROVIDER_HTTPS_PROXY", "http://192.168.10.236:7890")
	t.Setenv("PROVIDER_NO_PROXY", ".svc.cluster.local,10.0.0.0/8")

	client := NewProviderClient("https://api.github.com", time.Second)
	transport := client.Transport.(*http.Transport)
	if transport.Proxy == nil {
		t.Fatal("explicit Provider proxy was not configured")
	}
	githubReq, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	proxyURL, err := transport.Proxy(githubReq)
	if err != nil || proxyURL == nil || proxyURL.String() != "http://192.168.10.236:7890" {
		t.Fatalf("GitHub proxy=(%v,%v), want configured Provider proxy", proxyURL, err)
	}
	internalReq, _ := http.NewRequest(http.MethodGet, "https://jtype.jcode.svc.cluster.local/api", nil)
	proxyURL, err = transport.Proxy(internalReq)
	if err != nil || proxyURL != nil {
		t.Fatalf("internal Provider proxy=(%v,%v), want direct", proxyURL, err)
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
