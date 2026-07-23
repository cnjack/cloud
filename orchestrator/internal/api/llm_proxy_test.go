package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// proxyTestServer builds an orchestrator server whose EFFECTIVE model config
// (env source) points at the given upstream base + key. The proxy resolves this
// same config, so the runner's requests (authed by RUN_TOKEN) are forwarded to
// upstream with the real key. A fresh server per test so the modelcfg cache is
// pristine.
func proxyTestServer(t *testing.T, modelBaseURL, modelKey string) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{
		ConsoleToken: consoleToken,
		ModelBaseURL: modelBaseURL,
		ModelName:    "mock/mock-model",
		ModelAPIKey:  modelKey,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// unconfiguredProxyServer builds a server with NO effective model config, so the
// proxy's fail-visible gate returns 503 model_not_configured instead of guessing.
func unconfiguredProxyServer(t *testing.T) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// proxyRun schedules a run and returns its id + plaintext RUN_TOKEN, the
// credential the runner uses as MODEL_API_KEY against the proxy.
func proxyRun(t *testing.T, st *store.MemStore) (string, string) {
	t.Helper()
	return scheduledRun(t, st, &domain.Service{
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git",
	}, domain.RunKindAgent)
}

// proxyPost POSTs a JSON body to /internal/v1/<suffix> with the given bearer.
// suffix is the path AFTER "/internal/v1/" (e.g. "runs/<id>/llm/v1/chat/completions"),
// and may include a query string.
func proxyPost(t *testing.T, url, token, suffix string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/internal/v1/"+suffix, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestLLMProxyAuth proves the proxy inherits the runToken gate: a request
// without a token, with the wrong token, or for a nonexistent run is 401.
// Auth is checked BEFORE the model is resolved, so the model need not be set.
func TestLLMProxyAuth(t *testing.T) {
	ts, st := proxyTestServer(t, "http://upstream.test/v1", "realkey")
	rid, tok := proxyRun(t, st)

	// No Authorization header at all.
	req, _ := http.NewRequest("POST", ts.URL+"/internal/v1/runs/"+rid+"/llm/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Wrong token.
	resp = proxyPost(t, ts.URL, "not-the-token", "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown run id (token shape irrelevant — the run does not exist).
	resp = proxyPost(t, ts.URL, tok, "runs/run-does-not-exist/llm/v1/chat/completions", []byte("{}"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown run: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestLLMProxyModelNotConfigured is the fail-visible runtime gate: a run that
// was queued while configured but whose config was cleared before the runner
// made its LLM call must get a typed 503 model_not_configured — never a
// fabricated success or a silent mock.
func TestLLMProxyModelNotConfigured(t *testing.T) {
	ts, st := unconfiguredProxyServer(t)
	rid, tok := proxyRun(t, st)

	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if eb.Error.Code != "model_not_configured" {
		t.Fatalf("error code=%q want model_not_configured", eb.Error.Code)
	}
}

// upstreamCapture records the method/path/query/auth/body of one request so the
// forwarding tests can assert exactly what the proxy sent to the real LLM.
type upstreamCapture struct {
	method string
	path   string
	query  string
	auth   string
	body   string
	// optional custom response; defaults to a 200 JSON body.
	respond func(w http.ResponseWriter)
}

func newUpstream(t *testing.T, cap *upstreamCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		cap.body = string(b)
		if cap.respond != nil {
			cap.respond(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
}

// TestLLMProxyForwardsBaseWithV1 proves the core forwarding contract when the
// real model base ends in /v1: the runner's RUN_TOKEN is replaced by the real
// key, the path is reconstructed as /v1/chat/completions (no doubling), the body
// passes through untouched, and the query string is preserved.
func TestLLMProxyForwardsBaseWithV1(t *testing.T) {
	cap := &upstreamCapture{}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/v1", "realkey") // real base ends in /v1
	rid, tok := proxyRun(t, st)

	body := []byte(`{"model":"x","messages":[]}`)
	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions?stream=true", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != `{"ok":true}` {
		t.Fatalf("response body=%q want {\"ok\":true}", string(got))
	}

	// Method + path + query forwarded correctly (no /v1 doubling).
	if cap.method != http.MethodPost {
		t.Fatalf("upstream method=%q want POST", cap.method)
	}
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q want /v1/chat/completions (no /v1 doubling)", cap.path)
	}
	if cap.query != "stream=true" {
		t.Fatalf("upstream query=%q want stream=true", cap.query)
	}
	// Body passed through verbatim.
	if cap.body != string(body) {
		t.Fatalf("upstream body=%q want %q", cap.body, string(body))
	}
	// The RUN_TOKEN was replaced by the real key (never forwarded).
	if cap.auth != "Bearer realkey" {
		t.Fatalf("upstream Authorization=%q want 'Bearer realkey' (RUN_TOKEN must not leak)", cap.auth)
	}
	if cap.auth == "Bearer "+tok {
		t.Fatal("the RUN_TOKEN was forwarded to the upstream as the API key")
	}
}

// TestLLMProxyForwardsBaseWithoutV1 proves the proxy also does the right thing
// when the real model base does NOT end in a version: the /v1 arriving in the
// rest path is appended, still yielding /v1/chat/completions.
func TestLLMProxyForwardsBaseWithoutV1(t *testing.T) {
	cap := &upstreamCapture{}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL, "realkey") // real base has NO /v1
	rid, tok := proxyRun(t, st)

	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q want /v1/chat/completions (rest carries the /v1)", cap.path)
	}
	if cap.auth != "Bearer realkey" {
		t.Fatalf("upstream Authorization=%q want 'Bearer realkey'", cap.auth)
	}
}

// TestLLMProxyForwardsVersionedProviderBase is the regression test for
// OpenAI-compatible providers whose configured base is versioned with something
// other than /v1 (Zhipu Coding Plan currently uses /api/coding/paas/v4). The
// Cloud proxy's compatibility /v1 must not be appended after the upstream /v4.
func TestLLMProxyForwardsVersionedProviderBase(t *testing.T) {
	cap := &upstreamCapture{}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/api/coding/paas/v4", "realkey")
	rid, tok := proxyRun(t, st)

	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if cap.path != "/api/coding/paas/v4/chat/completions" {
		t.Fatalf("upstream path=%q want /api/coding/paas/v4/chat/completions", cap.path)
	}
}

func TestComposeUpstreamPath(t *testing.T) {
	tests := []struct {
		name string
		base string
		rest string
		want string
	}{
		{name: "empty base", rest: "v1/chat/completions", want: "/v1/chat/completions"},
		{name: "unversioned base", base: "/proxy", rest: "/v1/chat/completions", want: "/proxy/v1/chat/completions"},
		{name: "same version", base: "/proxy/v1/", rest: "v1/chat/completions", want: "/proxy/v1/chat/completions"},
		{name: "provider version wins", base: "/api/coding/paas/v4", rest: "v1/chat/completions", want: "/api/coding/paas/v4/chat/completions"},
		{name: "non-version v folder", base: "/proxy/version", rest: "v1/chat/completions", want: "/proxy/version/v1/chat/completions"},
		{name: "rest without version", base: "/proxy/v4", rest: "models", want: "/proxy/v4/models"},
		{name: "root only", base: "", rest: "", want: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeUpstreamPath(tt.base, tt.rest); got != tt.want {
				t.Fatalf("composeUpstreamPath(%q, %q)=%q want %q", tt.base, tt.rest, got, tt.want)
			}
		})
	}
}

// TestLLMProxyNormalizesNonJSONUpstreamError ensures an empty/HTML provider
// failure remains actionable to OpenAI-compatible clients instead of surfacing
// as the misleading "unexpected end of JSON input".
func TestLLMProxyNormalizesNonJSONUpstreamError(t *testing.T) {
	cap := &upstreamCapture{
		respond: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
		},
	}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/v1", "realkey")
	rid, tok := proxyRun(t, st)

	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q want application/json", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode normalized error: %v", err)
	}
	if body.Error.Code != "upstream_http_404" || body.Error.Type != "upstream_error" {
		t.Fatalf("normalized error=%+v", body.Error)
	}
}

// TestLLMProxyPreservesJSONUpstreamError proves the safety net does not rewrite
// a provider's own structured diagnostics.
func TestLLMProxyPreservesJSONUpstreamError(t *testing.T) {
	const upstreamBody = `{"error":{"code":"quota_exceeded","message":"no quota"}}`
	cap := &upstreamCapture{
		respond: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, upstreamBody)
		},
	}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/v1", "realkey")
	rid, tok := proxyRun(t, st)

	resp := proxyPost(t, ts.URL, tok, "runs/"+rid+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || string(got) != upstreamBody {
		t.Fatalf("status/body=%d %q want 429 %q", resp.StatusCode, string(got), upstreamBody)
	}
}

// TestLLMProxyAppliesCustomHeaders is the end-to-end FIX A assertion: a catalog
// model whose provider carries custom headers has those headers APPLIED on the
// forwarded upstream request, while the managed API key still wins over any
// custom Authorization header (keyed provider). The runner never sees either
// secret — they live only in the orchestrator + the encrypted columns.
func TestLLMProxyAppliesCustomHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Provider-Header")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(up.Close)

	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken, AuthTokenKey: validTokenKey(t)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Seal the key + custom headers with the SAME cipher the resolver uses.
	keyEnc, err := srv.cipher.EncryptString("realkey")
	if err != nil {
		t.Fatal(err)
	}
	hdrRaw, _ := json.Marshal(map[string]string{
		"X-Provider-Header": "abc",
		// A custom Authorization must LOSE to the managed key for a keyed provider.
		"Authorization": "Bearer custom-should-lose",
	})
	hdrEnc, err := srv.cipher.EncryptString(string(hdrRaw))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	proj := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	model := &domain.Model{
		ID: domain.NewID(), Name: "m", BaseURL: up.URL + "/v1", ModelName: "openai/x",
		ModelID: "x", APIKeyEnc: keyEnc, HeadersEnc: hdrEnc, CreatedAt: time.Now(),
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git", DefaultBranch: "main", CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	mid := model.ID
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: proj.ID, ServiceID: svc.ID, Prompt: "t",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Attempt: 1, ModelID: &mid, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}

	resp := proxyPost(t, ts.URL, tok, "runs/"+run.ID+"/llm/v1/chat/completions", []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if gotCustom != "abc" {
		t.Fatalf("upstream X-Provider-Header=%q want abc (custom header was not applied)", gotCustom)
	}
	if gotAuth != "Bearer realkey" {
		t.Fatalf("upstream Authorization=%q want 'Bearer realkey' (managed key must win over a custom Authorization)", gotAuth)
	}
}

// TestLLMProxyGetAllowed proves a GET (e.g. jcode listing /v1/models) is also
// proxied — the mount is method-agnostic so the OpenAI client's GET path works.
func TestLLMProxyGetAllowed(t *testing.T) {
	cap := &upstreamCapture{}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/v1", "realkey")
	rid, tok := proxyRun(t, st)

	req, _ := http.NewRequest("GET", ts.URL+"/internal/v1/runs/"+rid+"/llm/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d want 200", resp.StatusCode)
	}
	if cap.method != http.MethodGet {
		t.Fatalf("upstream method=%q want GET", cap.method)
	}
	if cap.path != "/v1/models" {
		t.Fatalf("upstream path=%q want /v1/models", cap.path)
	}
}

// TestLLMProxySSEStreaming proves the proxy flushes incrementally for Server-Sent
// Events rather than buffering. The upstream sends one chunk, flushes, then
// BLOCKS on a channel controlled by the test. If the proxy buffered the
// response, the first chunk could not be read until the upstream finished
// (deadlock → timeout). Receiving the first chunk before releasing the upstream
// therefore proves per-chunk flushing, deterministically and without timing.
func TestLLMProxySSEStreaming(t *testing.T) {
	release := make(chan struct{})
	cap := &upstreamCapture{
		respond: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			f := w.(http.Flusher)
			fmt.Fprint(w, "data: one\n\n")
			f.Flush()
			// Block until the test has observed the first chunk (proving it was
			// flushed through the proxy). If the proxy buffered, this block
			// would also block the reader → the test's first-chunk read would
			// time out.
			<-release
			fmt.Fprint(w, "data: two\n\n")
			f.Flush()
		},
	}
	up := newUpstream(t, cap)
	ts, st := proxyTestServer(t, up.URL+"/v1", "realkey")
	rid, tok := proxyRun(t, st)

	req, _ := http.NewRequest("POST", ts.URL+"/internal/v1/runs/"+rid+"/llm/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type=%q want text/event-stream", ct)
	}

	type result struct {
		line string
		err  error
	}
	first := make(chan result, 1)
	go func() {
		s := bufio.NewScanner(resp.Body)
		if !s.Scan() {
			first <- result{"", s.Err()}
			return
		}
		first <- result{s.Text(), nil}
	}()

	select {
	case r := <-first:
		if r.err != nil {
			t.Fatalf("read first chunk: %v", r.err)
		}
		if r.line != "data: one" {
			t.Fatalf("first chunk=%q want %q", r.line, "data: one")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy buffered the SSE stream: the first chunk did not arrive before the second was produced")
	}
	// Release the upstream so it can send the second chunk and end the response.
	close(release)

	// Drain the remainder so the upstream goroutine can finish cleanly.
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "data: two") {
		t.Fatalf("second chunk missing from stream tail: %q", string(rest))
	}
}
