// Package api exposes the orchestrator's HTTP surface using the standard-library
// router (Go 1.22 http.ServeMux, which supports method + wildcard patterns).
//
// Justification for std net/http over chi: the 1.22 mux covers everything we
// need — `POST /api/v1/...` method routing and `{id}` path wildcards via
// r.PathValue — so a third-party router would add a dependency for no gain. The
// std-lib-first directive applies.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/gitcli"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// Server holds the API dependencies.
type Server struct {
	st       store.Store
	cfg      *config.Config
	log      *slog.Logger
	hub      *sse.Hub
	launcher k8s.JobLauncher // used to delete Jobs on cancel; may be nil in API-only mode

	// Auth (M2). cipher encrypts identity tokens; oauth is the set of configured
	// login providers keyed by id; stateKey signs the OAuth CSRF state. All are
	// zero/empty when no OAuth provider is configured — the system then runs on
	// CONSOLE_TOKEN alone (backward compatible).
	cipher   *auth.Cipher
	oauth    map[domain.GitProvider]provider.OAuthProvider
	stateKey []byte

	// M3 runner-contract deps: creds resolves the per-run provider token (source
	// bundle + reconcile push/review), git builds source bundles, srcCache caches
	// them. Built in New from cfg + cipher + oauth so no signature churn.
	creds    *credentials.Resolver
	git      *gitcli.Git
	srcCache *sourceCache

	// factory builds a PR client per resolved token for the live PR-status lookup
	// (M5 GET /runs/{id}/pr). Same seam the reconciler uses; a test overrides it
	// with a fake. Never nil in production (built from cfg.GiteaURL in New).
	factory provider.Factory

	// models resolves (and caches) the effective LLM configuration (Feature A).
	// Shared with the reconciler via Models() so a console PUT/DELETE's
	// Invalidate() is immediately visible to Job scheduling. Never nil.
	models *modelcfg.Resolver

	// jtypeBoard validates a board (fetches its columns) before creating a
	// kanban_link. It is the *jtype.Client in production; nil when the
	// integration is off (column validation is then skipped). Typed as an
	// interface so tests inject a fake without HTTP.
	jtypeBoard boardValidator
}

// boardValidator is the slice of *jtype.Client the admin link API needs to
// validate trigger/done column names against a live board.
type boardValidator interface {
	GetBoard(ctx context.Context, workspace, boardRef string) (*jtype.Board, error)
}

// New builds a Server. launcher may be nil (K8s disabled). The token cipher and
// OAuth provider registry are built from cfg, so no OAuth config => empty
// registry => auth endpoints report no providers and CONSOLE_TOKEN still works.
func New(st store.Store, cfg *config.Config, log *slog.Logger, hub *sse.Hub, launcher k8s.JobLauncher) *Server {
	s := &Server{st: st, cfg: cfg, log: log, hub: hub, launcher: launcher}

	// Token cipher (nil when AUTH_TOKEN_KEY is unset). config.Load has already
	// validated the key when any provider is configured.
	if c, err := auth.NewCipher(cfg.AuthTokenKey); err != nil {
		log.Error("auth token cipher disabled: invalid AUTH_TOKEN_KEY", "err", err)
	} else {
		s.cipher = c
	}

	// OAuth provider registry.
	s.oauth = buildOAuthProviders(cfg.OAuthProviders)

	// Derive the HMAC key that signs OAuth state from the token key so it is
	// stable across restarts (a cookie mid-flow survives a rollout). Falls back to
	// a per-process random key when no token key is set (no providers => unused).
	if kb, err := auth.DecodeTokenKey(cfg.AuthTokenKey); err == nil {
		h := sha256.Sum256(append(kb, []byte("jcloud-oauth-state")...))
		s.stateKey = h[:]
	} else {
		rk := make([]byte, 32)
		_, _ = rand.Read(rk)
		s.stateKey = rk
	}

	// M3 runner-contract stack: the credential resolver (shared with the
	// reconciler via Credentials()), the git CLI wrapper, and the source-bundle
	// cache. cipher/oauth may be nil/empty; the resolver then offers only the
	// gitea PAT fallback.
	s.creds = credentials.NewResolver(st, s.cipher, s.oauth, cfg.GiteaToken, log)
	s.git = gitcli.New()
	s.srcCache = newSourceCache(cfg.SourceBundleTTL)
	// PR-status client factory (M5). Shares the same builder the reconciler uses;
	// a deployment without a provider simply reports state="unknown" per PR.
	s.factory = provider.NewFactory(cfg.GiteaURL)
	// Effective-model resolver (Feature A): one cached instance for every gate
	// (run create/retry/review, webhook, and — via Models() — the reconciler).
	s.models = modelcfg.NewResolver(st, s.cipher, cfg)
	// Feature E — jtype kanban client (nil when the integration is off). Used by
	// the admin link API to validate board columns at create time.
	if cfg.JtypeBaseURL != "" && cfg.JtypeToken != "" {
		s.jtypeBoard = jtype.NewClient(cfg.JtypeBaseURL, cfg.JtypeToken, 0)
	}
	return s
}

// Credentials exposes the shared credential resolver so the reconciler resolves
// per-run tokens with the same config the API uses.
func (s *Server) Credentials() *credentials.Resolver { return s.creds }

// Git exposes the git CLI wrapper (source bundle / branch push) so the
// reconciler pushes with the same binary the source endpoint uses.
func (s *Server) Git() *gitcli.Git { return s.git }

// Models exposes the shared model-config resolver so the reconciler resolves
// the effective LLM config through the SAME cache the API invalidates on
// PUT/DELETE (Feature A).
func (s *Server) Models() *modelcfg.Resolver { return s.models }

// buildOAuthProviders constructs the login providers from config. Unknown ids
// are skipped defensively (config only emits gitea/github/gitlab).
func buildOAuthProviders(cfgs []config.OAuthProviderConfig) map[domain.GitProvider]provider.OAuthProvider {
	out := map[domain.GitProvider]provider.OAuthProvider{}
	for _, pc := range cfgs {
		oc := provider.OAuthConfig{
			ClientID:     pc.ClientID,
			ClientSecret: pc.ClientSecret,
			ExternalURL:  pc.ExternalURL,
			InternalURL:  pc.InternalURL,
		}
		switch domain.GitProvider(pc.ID) {
		case domain.ProviderGitea:
			out[domain.ProviderGitea] = provider.NewGiteaOAuth(oc)
		case domain.ProviderGitHub:
			out[domain.ProviderGitHub] = provider.NewGitHubOAuth(oc)
		case domain.ProviderGitLab:
			out[domain.ProviderGitLab] = provider.NewGitLabOAuth(oc)
		}
	}
	return out
}

// Handler builds the full route tree with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health (unauthenticated).
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Gitea @mention webhook (M7 / blueprint §8). Public path, self-authenticated
	// by HMAC signature. Registered ONLY when WEBHOOK_SECRET is configured — with
	// no secret the route is absent (404) and the system runs normally.
	if s.cfg.WebhookSecret != "" {
		mux.HandleFunc("POST /webhooks/gitea", s.handleGiteaWebhook)
	}

	// Auth endpoints (multitenant blueprint §2). Provider list + login start +
	// callback are unauthenticated (they establish the session); link/logout/me
	// require an existing principal.
	mux.HandleFunc("GET /auth/providers", s.handleAuthProviders)
	mux.HandleFunc("GET /auth/login/{provider}", s.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback/{provider}", s.handleAuthCallback)
	mux.Handle("GET /auth/link/{provider}", s.authed(s.handleAuthLink))
	mux.Handle("POST /auth/logout", s.authed(s.handleAuthLogout))
	mux.Handle("GET /api/v1/me", s.authed(s.handleMe))

	// Read-only admin snapshot for the cluster-admin console view (11-api.md §
	// "System / admin"). Never returns a secret.
	mux.Handle("GET /api/v1/system", s.authed(s.handleGetSystem))

	// Cluster model config (Feature A). GET is readable by any logged-in
	// principal (non-admins get only {configured}); PUT/DELETE are cluster-admin
	// only (enforced in the handler). The plaintext API key is never returned.
	mux.Handle("GET /api/v1/system/model", s.authed(s.handleGetModelConfig))
	mux.Handle("PUT /api/v1/system/model", s.authed(s.handlePutModelConfig))
	mux.Handle("DELETE /api/v1/system/model", s.authed(s.handleDeleteModelConfig))

	// Feature E — jtype kanban links. GET (list) is any logged-in principal;
	// POST/DELETE are cluster-admin only (enforced in the handler). Board column
	// validation against the live jtype workspace happens at create time.
	mux.Handle("GET /api/v1/system/kanban/links", s.authed(s.handleListKanbanLinks))
	mux.Handle("POST /api/v1/system/kanban/links", s.authed(s.handleCreateKanbanLink))
	mux.Handle("DELETE /api/v1/system/kanban/links/{id}", s.authed(s.handleDeleteKanbanLink))

	// User search (any logged-in user; for the add-member picker).
	mux.Handle("GET /api/v1/users", s.authed(s.handleSearchUsers))

	mux.Handle("POST /api/v1/projects", s.authed(s.handleCreateProject))
	mux.Handle("GET /api/v1/projects", s.authed(s.handleListProjects))
	mux.Handle("GET /api/v1/projects/{id}", s.authed(s.handleGetProject))
	mux.Handle("PATCH /api/v1/projects/{id}", s.authed(s.handleUpdateProject))
	mux.Handle("DELETE /api/v1/projects/{id}", s.authed(s.handleDeleteProject))

	// Project members (owner/cluster-admin manage).
	mux.Handle("GET /api/v1/projects/{id}/members", s.authed(s.handleListMembers))
	mux.Handle("POST /api/v1/projects/{id}/members", s.authed(s.handleAddMember))
	mux.Handle("DELETE /api/v1/projects/{id}/members/{userID}", s.authed(s.handleRemoveMember))

	// Services (multitenant blueprint §4). A service is a repo config inside a
	// project; runs are created against a service.
	// Repo picker for Drone-style service onboarding (lists what the caller's
	// provider credential can see).
	mux.Handle("GET /api/v1/providers/{provider}/repos", s.authed(s.handleListProviderRepos))

	mux.Handle("POST /api/v1/projects/{id}/services", s.authed(s.handleCreateService))
	mux.Handle("GET /api/v1/projects/{id}/services", s.authed(s.handleListServices))
	mux.Handle("PATCH /api/v1/services/{id}", s.authed(s.handleUpdateService))
	mux.Handle("DELETE /api/v1/services/{id}", s.authed(s.handleDeleteService))
	mux.Handle("POST /api/v1/services/{id}/runs", s.authed(s.handleCreateServiceRun))
	mux.Handle("GET /api/v1/services/{id}/runs", s.authed(s.handleListServiceRuns))

	// Run creation is service-scoped only (above); listing stays project-scoped.
	mux.Handle("GET /api/v1/projects/{id}/runs", s.authed(s.handleListRuns))
	mux.Handle("GET /api/v1/runs", s.authed(s.handleListRuns))
	mux.Handle("GET /api/v1/runs/{id}", s.authed(s.handleGetRun))
	mux.Handle("GET /api/v1/runs/{id}/events", s.authed(s.handleListEvents))
	// SSE stream also accepts a session/console token via ?access_token= because a
	// browser EventSource cannot set an Authorization header (see 11-api.md §2.3).
	mux.Handle("GET /api/v1/runs/{id}/stream", s.authedStream(s.handleStream))
	mux.Handle("GET /api/v1/runs/{id}/artifact", s.authedStream(s.handleGetArtifact))
	mux.Handle("POST /api/v1/runs/{id}/cancel", s.authed(s.handleCancelRun))
	mux.Handle("POST /api/v1/runs/{id}/retry", s.authed(s.handleRetryRun))
	// PR review (M5): request an AI review of a succeeded agent run's PR, and read
	// the PR's live state + its review runs. review is a mutation (member+); the
	// pr view is read-only (viewer+).
	mux.Handle("POST /api/v1/runs/{id}/review", s.authed(s.handleRequestReview))
	mux.Handle("GET /api/v1/runs/{id}/pr", s.authed(s.handleGetPR))

	// Internal endpoints — require the per-run RUN_TOKEN.
	mux.Handle("POST /internal/v1/runs/{id}/events", s.runToken(s.handleIngestEvents))
	mux.Handle("POST /internal/v1/runs/{id}/artifact", s.runToken(s.handleIngestArtifact))
	// M3 runner contract: the runner fetches its source bundle, uploads the
	// draft-PR git bundle, and posts review output — all authed by the RUN_TOKEN.
	mux.Handle("GET /internal/v1/runs/{id}/source", s.runToken(s.handleGetSource))
	mux.Handle("POST /internal/v1/runs/{id}/bundle", s.runToken(s.handleIngestBundle))
	mux.Handle("POST /internal/v1/runs/{id}/review", s.runToken(s.handleIngestReview))
	// Feature D — LLM reverse proxy (architecture O5): the runner's LLM traffic
	// goes through the orchestrator, which injects the real key and forwards to
	// the real model. Method-agnostic so POST /chat/completions and GET /models
	// both work; {rest...} is the OpenAI-style path the client appended. Authed
	// by the same per-run RUN_TOKEN gate as the other internal endpoints.
	mux.Handle("/internal/v1/runs/{id}/llm/{rest...}", s.runToken(s.handleLLMProxy))

	return s.recover(s.logRequests(mux))
}

// --- middleware -------------------------------------------------------------

// authed resolves the request principal (session cookie, Bearer session token,
// or CONSOLE_TOKEN) and places it in the context. A 401 with the machine-readable
// code "unauthorized" (which the console keys off) is returned when unresolved.
func (s *Server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.resolvePrincipal(r, false)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		h(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// authedStream is authed for the read-only stream/download endpoints: it also
// accepts a session or console token via ?access_token= (browser EventSource /
// anchor download cannot attach an Authorization header). Every mutating endpoint
// remains header/cookie only.
func (s *Server) authedStream(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.resolvePrincipal(r, true)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		h(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// runToken wraps an internal handler with per-run token auth. The run whose
// token matches is placed in the request context so the handler need not
// re-resolve it, and the path {id} must match that run.
func (s *Server) runToken(h func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "run token required")
			return
		}
		runID := r.PathValue("id")
		run, err := s.st.GetRun(r.Context(), runID)
		if errors.Is(err, store.ErrNotFound) {
			// Do not leak existence; same 401 as a bad token.
			writeError(w, http.StatusUnauthorized, "unauthorized", "run token invalid")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "lookup failed")
			return
		}
		// Constant-time compare of the presented token's hash against stored hash.
		if run.TokenHash == "" || !auth.ConstantTimeEqual(auth.HashToken(tok), run.TokenHash) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "run token invalid")
			return
		}
		h(w, r, runID)
	})
}

// logRequests logs method, path and status.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status)
	})
}

// recover turns a panic into a 500 rather than crashing the process.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Flush lets SSE handlers stream through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// --- helpers ----------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// errorBody is the uniform error envelope: {"error":{"code","message"}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// decodeJSON strictly decodes the request body into v.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
