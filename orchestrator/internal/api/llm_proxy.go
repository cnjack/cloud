package api

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/modelcfg"
)

// llmProxyTransport is the reverse proxy's upstream transport. Dial + header
// timeouts are bounded so a misconfigured/unreachable LLM fails fast, but there
// is NO overall response-body timeout: an LLM stream (SSE) may legitimately
// produce tokens for many minutes, and bounding it would abort healthy runs.
var llmProxyTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second, // time to first response byte (SSE included)
}

// handleLLMProxy is the in-process LLM reverse proxy (Feature D / architecture
// O5). The runner's MODEL_BASE_URL points here and authenticates with the
// per-run RUN_TOKEN (s.runToken); the proxy resolves the EFFECTIVE model config
// (the same modelcfg.Resolver cache the API/reconciler share), injects the real
// API key, and forwards the request to the real LLM, streaming the SSE response
// straight back. The decrypted key therefore lives ONLY in the orchestrator
// process memory + the encrypted cluster_model_config row — it NEVER enters the
// runner pod's env, so a prompt injection in the repo cannot exfiltrate it.
//
// Routing: POST/GET /internal/v1/runs/{id}/llm/{rest...} where {rest} is the
// OpenAI-style path the client appended (e.g. "v1/chat/completions"). The proxy
// is method-agnostic on purpose so the OpenAI client's POST completions AND GET
// /models both work.
//
// Path forwarding is transparent w.r.t. /v1: jcode treats base_url as already
// including /v1 and appends a relative path like /chat/completions. The runner's
// proxy base is ${ORCH}/internal/v1/runs/{id}/llm (no /v1) and the entrypoint
// appends /v1, so the request that arrives here carries rest="v1/chat/completions".
// The proxy strips the trailing /v1 from the REAL model.BaseURL (which also ends
// in /v1) and re-attaches the rest — so both bases compose to the same upstream
// path with no /v1 doubling. See stripTrailingV1.
//
// Fail-visible (CLAUDE.md red line #1): an unconfigured model returns a typed
// 503 model_not_configured rather than a fabricated success; a resolve error is
// a 502. Security: the inbound RUN_TOKEN Authorization header is ALWAYS dropped
// before forwarding and replaced with the real key; nothing about the key or the
// request body is ever logged.
func (s *Server) handleLLMProxy(w http.ResponseWriter, r *http.Request, runID string) {
	// s.runToken has already verified the run exists and the bearer == RUN_TOKEN.
	model, err := s.models.Resolve(r.Context())
	if err != nil {
		s.log.Error("llm proxy: resolve model", "run", runID, "err", err)
		writeError(w, http.StatusBadGateway, "model_resolve_failed", "could not resolve the model configuration")
		return
	}
	if !model.Configured() {
		// A run scheduled while configured whose config was cleared before the
		// runner called must fail visibly — never pretend the call succeeded.
		writeError(w, http.StatusServiceUnavailable, "model_not_configured",
			modelcfg.NotConfiguredMessage(""))
		return
	}

	target, err := url.Parse(model.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		s.log.Error("llm proxy: invalid model base url", "run", runID, "base", model.BaseURL, "err", err)
		writeError(w, http.StatusBadGateway, "model_misconfigured", "the configured model base URL is invalid")
		return
	}

	// rest is the path after the "/llm" mount (no leading slash), e.g.
	// "v1/chat/completions". Forward path = real base root (trailing /v1 stripped)
	// + "/" + rest, so a base ending in /v1 composes with rest's /v1 without
	// doubling. RawQuery is inherited by the cloned outgoing URL and preserved.
	rest := r.PathValue("rest")
	root := stripTrailingV1(target.Path)
	forwardPath := root + "/" + rest

	rp := &httputil.ReverseProxy{
		// Flush immediately so SSE token chunks reach the runner as they arrive
		// rather than being buffered into the response.
		FlushInterval: -1,
		Transport:     llmProxyTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = forwardPath
			pr.Out.URL.RawPath = "" // Path is authoritative; rest is plain ASCII
			// Send the real upstream Host (some LLM gateways reject a foreign Host).
			pr.Out.Host = target.Host
			// Drop the runner's RUN_TOKEN Authorization and inject the real key.
			// A keyless endpoint (APIKey == "") sends no Authorization at all.
			pr.Out.Header.Del("Authorization")
			if model.APIKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+model.APIKey)
			}
			// Hop-by-hop headers are stripped by ReverseProxy; keep no extras.
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// Log only method/run/status — never the key, the body, or the full URL.
			s.log.Warn("llm proxy: upstream error", "run", runID, "method", r.Method,
				"status", http.StatusBadGateway, "err", err)
			writeError(w, http.StatusBadGateway, "upstream_unreachable", "the model endpoint could not be reached")
		},
	}
	rp.ServeHTTP(w, r)
}

// stripTrailingV1 removes a trailing "/v1" (and any trailing slashes) from a URL
// path so the proxy can re-attach the "/v1/..." that arrives in the request rest
// without doubling it. Examples:
//
//	"/v1"          -> ""
//	"/v1/"         -> ""
//	"/proxy/v1"    -> "/proxy"
//	""             -> ""
//	"/v2"          -> "/v2"   (no /v1 to strip; transparent)
//
// This makes the proxy correct whether or not the real model.BaseURL terminates
// in /v1, matching jcode's "base already includes /v1" convention.
func stripTrailingV1(path string) string {
	s := strings.TrimRight(path, "/")
	s, _ = strings.CutSuffix(s, "/v1")
	return s
}
