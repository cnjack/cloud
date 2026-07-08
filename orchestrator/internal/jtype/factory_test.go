package jtype

import (
	"errors"
	"testing"
	"time"
)

func TestNewFactoryNilOnEmptyBase(t *testing.T) {
	if NewFactory("", 0) != nil {
		t.Fatal("empty base URL must yield a nil factory (integration off)")
	}
	if NewFactory("   ", 0) != nil {
		t.Fatal("blank base URL must yield a nil factory")
	}
	f := NewFactory("http://jtype:13345/", 5*time.Second)
	if f == nil {
		t.Fatal("non-empty base URL must yield a factory")
	}
	// Trailing slash trimmed; the shared pool is reused across tokens.
	c1 := f.Client("tok-a")
	c2 := f.Client("tok-b")
	if c1.baseURL != "http://jtype:13345" || c2.baseURL != "http://jtype:13345" {
		t.Fatalf("base URL not trimmed: %q / %q", c1.baseURL, c2.baseURL)
	}
	if c1.token != "tok-a" || c2.token != "tok-b" {
		t.Fatalf("per-client token wrong: %q / %q", c1.token, c2.token)
	}
	if c1.http != c2.http {
		t.Fatal("clients from one factory must share the HTTP pool")
	}
}

// The three-state token selection (D25): per-link decrypt, cluster fallback, and
// the double-empty fail-visible error — plus the decrypt-error and no-cipher edges.
func TestResolveTokenThreeStates(t *testing.T) {
	// A trivial reversible "decrypt": prefix-strip so the test asserts the token
	// actually flowed through decrypt (not the cluster fallback).
	decrypt := func(b []byte) (string, error) { return "dec:" + string(b), nil }

	// 1) per-link token present -> decrypted, source=PerLink (cluster ignored).
	tok, src, err := ResolveToken([]byte("ENC"), decrypt, "cluster-tok")
	if err != nil || tok != "dec:ENC" || src != TokenPerLink {
		t.Fatalf("per-link: got %q src=%d err=%v", tok, src, err)
	}

	// 2) no per-link token, cluster set -> fallback, source=ClusterFallback.
	tok, src, err = ResolveToken(nil, decrypt, "cluster-tok")
	if err != nil || tok != "cluster-tok" || src != TokenClusterFallback {
		t.Fatalf("fallback: got %q src=%d err=%v", tok, src, err)
	}

	// 3) neither -> ErrNoToken (fail-visible; the caller skips the link).
	tok, src, err = ResolveToken(nil, decrypt, "")
	if !errors.Is(err, ErrNoToken) || tok != "" || src != TokenNone {
		t.Fatalf("double-empty: got %q src=%d err=%v, want ErrNoToken", tok, src, err)
	}

	// 4) encrypted token but no cipher -> ErrNoCipher (never a silent fallback).
	_, _, err = ResolveToken([]byte("ENC"), nil, "cluster-tok")
	if !errors.Is(err, ErrNoCipher) {
		t.Fatalf("no cipher: want ErrNoCipher, got %v", err)
	}

	// 5) decrypt failure bubbles up (not downgraded to the cluster token).
	boom := errors.New("bad key")
	_, _, err = ResolveToken([]byte("ENC"), func([]byte) (string, error) { return "", boom }, "cluster-tok")
	if !errors.Is(err, boom) {
		t.Fatalf("decrypt error: want boom, got %v", err)
	}
}
