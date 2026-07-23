package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
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
// process memory + the encrypted model_configs.api_key_enc column — it NEVER
// enters the runner pod's env, so a prompt injection in the repo cannot
// exfiltrate it. The model is resolved PER RUN (by run.model_id; D21).
//
// Routing: POST/GET /internal/v1/runs/{id}/llm/{rest...} where {rest} is the
// OpenAI-style path the client appended (e.g. "v1/chat/completions"). The proxy
// is method-agnostic on purpose so the OpenAI client's POST completions AND GET
// /models both work.
//
// Path forwarding is transparent across provider API versions. jcode calls the
// Cloud proxy through its OpenAI-compatible /v1 surface, so the request arriving
// here carries rest="v1/chat/completions". Most providers either have no version
// in BaseURL or end in /v1; some compatible providers instead expose the same
// operation below a different versioned base such as /v4. composeUpstreamPath
// keeps the provider's terminal version authoritative, preventing both /v1/v1
// and /v4/v1 path corruption.
//
// Fail-visible (CLAUDE.md red line #1): an unconfigured model returns a typed
// 503 model_not_configured rather than a fabricated success; a resolve error is
// a 502. Security: the inbound RUN_TOKEN Authorization header is ALWAYS dropped
// before forwarding and replaced with the real key; nothing about the key or the
// request body is ever logged.
func (s *Server) handleLLMProxy(w http.ResponseWriter, r *http.Request, runID string) {
	// s.runToken has already verified the run exists and the bearer == RUN_TOKEN,
	// and stashed the loaded run in the context (P4 — no second GetRun on this hot
	// path). Materialise the model THIS run was dispatched with (D21) from its
	// stamped model_id (or the env fallback when NULL). Per-run resolution keeps
	// the D16 invariant — the real key lives only here, never in the pod.
	run := runFromToken(r.Context())
	if run == nil {
		// Defensive: the middleware always stashes the run; a nil here means a
		// misconfigured route. Fail visibly rather than resolve blind.
		s.log.Error("llm proxy: run missing from context", "run", runID)
		writeError(w, http.StatusBadGateway, "model_resolve_failed", "could not resolve the run")
		return
	}
	model, err := s.models.ResolveModel(r.Context(), deref(run.ModelID))
	if err != nil {
		s.log.Error("llm proxy: resolve model", "run", runID, "err", err)
		writeError(w, http.StatusBadGateway, "model_resolve_failed", "could not resolve the model configuration")
		return
	}
	s.proxyResolvedModel(w, r, model, "run", runID)
}

// proxyResolvedModel is the shared credential-injecting proxy for run-token and
// device-token callers. Authorization happens before this function; keeping the
// forwarding rules here prevents the two surfaces from drifting on secret
// stripping, custom-header filtering, /v1 composition, SSE flushing or timeout
// behavior.
func (s *Server) proxyResolvedModel(w http.ResponseWriter, r *http.Request, model modelcfg.Resolved, subjectKind, subjectID string) {
	if !model.Configured() {
		// A run scheduled while configured whose config was cleared before the
		// runner called must fail visibly — never pretend the call succeeded.
		writeError(w, http.StatusServiceUnavailable, "model_not_configured",
			modelcfg.NotConfiguredMessage(""))
		return
	}

	target, err := url.Parse(model.BaseURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		s.log.Error("llm proxy: invalid model base url", "subject_kind", subjectKind, "subject", subjectID, "err", err)
		writeError(w, http.StatusBadGateway, "model_misconfigured", "the configured model base URL is invalid")
		return
	}

	// rest is the path after the "/llm" mount (no leading slash), e.g.
	// "v1/chat/completions". A terminal API version in the real provider base is
	// authoritative; otherwise the proxy's /v1 is retained. RawQuery is inherited
	// by the cloned outgoing URL and preserved.
	rest := r.PathValue("rest")
	forwardPath := composeUpstreamPath(target.Path, rest)

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
			// Always drop the runner's inbound RUN_TOKEN Authorization first so it
			// never reaches the upstream, regardless of what replaces it below.
			pr.Out.Header.Del("Authorization")
			// Apply the provider's custom headers (jcode advanced-form parity). Set
			// BEFORE the managed key so a keyed provider's managed Authorization wins
			// (set last), while a keyless provider's custom Authorization survives.
			// Skip Host + hop-by-hop headers; header VALUES are never logged.
			for k, v := range model.Headers {
				if skipCustomHeader(k) {
					continue
				}
				pr.Out.Header.Set(k, v)
			}
			// Inject the real key LAST so it always wins for a keyed provider. A
			// keyless endpoint (APIKey == "") keeps whatever the custom headers set
			// (an explicit Authorization) or nothing at all.
			if model.APIKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+model.APIKey)
			}
			// Hop-by-hop headers are stripped by ReverseProxy; keep no extras.
		},
		ModifyResponse: normalizeUpstreamError,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// Log only method/subject/status — never the key, body, or full URL.
			s.log.Warn("llm proxy: upstream error", "subject_kind", subjectKind, "subject", subjectID, "method", r.Method,
				"status", http.StatusBadGateway, "err", err)
			writeError(w, http.StatusBadGateway, "upstream_unreachable", "the model endpoint could not be reached")
		},
	}
	rp.ServeHTTP(w, r)
}

// composeUpstreamPath joins the configured provider base path and the relative
// path presented by Cloud's OpenAI-compatible /v1 proxy. If the provider base
// already terminates in an API version, that version wins:
//
//	("", "v1/chat/completions")                    -> "/v1/chat/completions"
//	("/proxy", "v1/chat/completions")              -> "/proxy/v1/chat/completions"
//	("/proxy/v1", "v1/chat/completions")           -> "/proxy/v1/chat/completions"
//	("/api/coding/paas/v4", "v1/chat/completions") -> "/api/coding/paas/v4/chat/completions"
//
// This preserves jcode's stable /v1 Cloud contract without assuming that every
// OpenAI-compatible upstream also locates its operations below /v1.
func composeUpstreamPath(basePath, rest string) string {
	base := strings.TrimRight(basePath, "/")
	rest = strings.TrimLeft(rest, "/")

	lastBaseSegment := base
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		lastBaseSegment = base[i+1:]
	}
	if isAPIVersionSegment(lastBaseSegment) {
		if firstRestSegment, remainder, ok := strings.Cut(rest, "/"); ok && isAPIVersionSegment(firstRestSegment) {
			rest = remainder
		} else if isAPIVersionSegment(rest) {
			rest = ""
		}
	}

	if rest == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	return base + "/" + rest
}

// isAPIVersionSegment deliberately recognizes only the unambiguous v<digits>
// form used by provider URL paths. It must not treat an arbitrary final folder
// beginning with "v" as an API version and silently discard part of a request.
func isAPIVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, r := range segment[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

const maxUpstreamErrorBody = 64 << 10

// normalizeUpstreamError keeps valid JSON provider errors untouched, but turns
// empty, HTML, truncated or otherwise non-JSON failures into a stable JSON
// envelope. Without this guard an OpenAI-compatible client reports an unrelated
// "unexpected end of JSON input" and hides the actionable upstream HTTP status.
// Success responses (including SSE streams) never pass through this buffering.
func normalizeUpstreamError(resp *http.Response) error {
	if resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBody+1))
	if err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(raw) <= maxUpstreamErrorBody && len(trimmed) > 0 && json.Valid(trimmed) {
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    "upstream_http_" + strconv.Itoa(resp.StatusCode),
			"message": "the model provider returned HTTP " + strconv.Itoa(resp.StatusCode),
			"type":    "upstream_error",
		},
	})
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(payload))
	resp.ContentLength = int64(len(payload))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Transfer-Encoding")
	return nil
}

// skipCustomHeader reports whether a provider-configured custom header must NOT
// be applied to an outbound request. The proxy controls transport framing and
// the upstream Host, so a custom header may never override Host, Content-Length,
// or the hop-by-hop set — those are the proxy's / transport's to manage.
func skipCustomHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}
