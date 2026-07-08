package jtype

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Factory builds jtype Clients that share ONE HTTP connection pool but bind
// different PATs, so each kanban link authorises jtype reads/writes with its own
// credential (the per-link encrypted token, or the cluster fallback) rather than
// a single process-wide token (D25 / F6). The base URL and timeout are fixed at
// construction; only the token varies per Client.
type Factory struct {
	baseURL string
	http    *http.Client
}

// NewFactory builds a Factory rooted at baseURL. An empty baseURL returns nil so
// callers can gate the whole integration on `factory != nil` (the fail-visible
// "jtype off" state — never a mock). timeout<=0 defaults to 20s (same as
// NewClient); the pool has no response-body deadline so SSE-style streams are
// unaffected (jtype REST calls are short, so the fixed Client timeout is fine).
func NewFactory(baseURL string, timeout time.Duration) *Factory {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Factory{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Client returns a Client bound to token, reusing the factory's shared HTTP
// pool. An empty token yields a Client whose calls carry no Authorization header
// (jtype then 401s) — callers resolve a non-empty token via ResolveToken first.
func (f *Factory) Client(token string) *Client {
	return &Client{baseURL: f.baseURL, token: token, http: f.http}
}

// TokenSource records which credential ResolveToken selected for a link, so the
// caller can emit the one-time cluster-fallback deprecation notice (D25).
type TokenSource int

const (
	// TokenNone means no credential was available (fail-visible: skip the link).
	TokenNone TokenSource = iota
	// TokenPerLink means the link's own encrypted PAT was decrypted and used.
	TokenPerLink
	// TokenClusterFallback means the link had no per-link PAT and fell back to the
	// cluster JTYPE_TOKEN env (deprecated path).
	TokenClusterFallback
)

// ErrNoToken is returned by ResolveToken when a link has neither a per-link token
// nor a cluster fallback — the poller/writeback then skip it fail-visibly instead
// of calling jtype with an empty credential.
var ErrNoToken = errors.New("jtype: no credential for link (no per-link token and no cluster JTYPE_TOKEN fallback)")

// ErrNoCipher is returned when a link carries an encrypted per-link token but no
// cipher is configured (AUTH_TOKEN_KEY unset) to decrypt it — a visible config
// error, never a silent fallback to the cluster token.
var ErrNoCipher = errors.New("jtype: link has an encrypted token but AUTH_TOKEN_KEY is not configured")

// ResolveToken selects the PAT for a link (D25 three-state selection, shared by
// the poller, the writeback pass, and — for validation — the admin API):
//
//	tokenEnc non-empty -> decrypt it (TokenPerLink); decrypt is the cipher's
//	    DecryptString, or nil when no cipher is configured (=> ErrNoCipher).
//	tokenEnc empty + clusterToken set -> the cluster fallback (TokenClusterFallback).
//	both empty -> ErrNoToken (TokenNone).
//
// A decrypt failure (tampering / wrong key) bubbles up rather than falling back,
// so a broken per-link token is loud, not silently downgraded.
func ResolveToken(tokenEnc []byte, decrypt func([]byte) (string, error), clusterToken string) (string, TokenSource, error) {
	if len(tokenEnc) > 0 {
		if decrypt == nil {
			return "", TokenNone, ErrNoCipher
		}
		tok, err := decrypt(tokenEnc)
		if err != nil {
			return "", TokenNone, fmt.Errorf("jtype: decrypt link token: %w", err)
		}
		return tok, TokenPerLink, nil
	}
	if clusterToken != "" {
		return clusterToken, TokenClusterFallback, nil
	}
	return "", TokenNone, ErrNoToken
}
