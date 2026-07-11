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
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/gitcli"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/jtypeoauth"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/kanbancfg"
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

	// kanban resolves the EFFECTIVE cluster jtype kanban config (base URL +
	// optional cluster fallback token) at REQUEST time from the console-managed DB
	// row, falling back to the JTYPE_* env (D27). Shared with the reconciler +
	// poller via Kanban() so a console PUT/DELETE's Invalidate() takes effect
	// WITHOUT a restart and all three build clients from one HTTP pool. Never nil.
	kanban *kanbancfg.Resolver
	// boardValidatorFor builds a board validator (column fetch) from a resolved
	// jtype Factory + token, used to validate a kanban_link's trigger/done columns
	// at create time. Production wraps *jtype.Factory.Client; a test injects a fake
	// (ignoring the factory) so column validation is exercised without HTTP.
	boardValidatorFor func(f *jtype.Factory, token string) boardValidator
	// jtypeDiscoveryFor builds a jtype discovery client (workspace + board pickers,
	// D29) from a resolved Factory + token. Production wraps *jtype.Factory.Client;
	// a test injects a fake so the owner-only discovery endpoints are exercised
	// without HTTP. The effective token is used but NEVER serialized to the caller.
	jtypeDiscoveryFor func(f *jtype.Factory, token string) jtypeDiscovery
	// boardProxyFor builds a raw jtype document-API proxy (the member+ board embed,
	// D31) from a resolved Factory + token. Production wraps *jtype.Factory.Client;
	// a test injects a fake so the proxy endpoints are exercised without HTTP. The
	// effective token is applied as a Bearer header inside ProxyDocumentAPI but is
	// NEVER serialized to the caller.
	boardProxyFor func(f *jtype.Factory, token string) jtypeBoardProxy

	// connects is the in-memory registry of pending "Connect with jtype" OAuth
	// device flows (D28); no DB persistence — a restart drops in-flight flows.
	connects *connectRegistry
	// oauthClientFor builds a jtype OAuth device-flow client for a base URL.
	// Production wires jtypeoauth.NewClient; a test injects a fake with a poll spy.
	oauthClientFor func(baseURL string) oauthClient

	// Session next-prompt long-poll timings (D22). Zero => the package defaults
	// (25s hold / 500ms poll). Overridable by tests that need a fast hold.
	nextPromptHold time.Duration
	nextPromptPoll time.Duration
}

// boardValidator is the slice of *jtype.Client the admin link API needs to
// validate trigger/done column names against a live board.
type boardValidator interface {
	GetBoard(ctx context.Context, workspace, boardRef string) (*jtype.Board, error)
}

// jtypeDiscovery is the slice of *jtype.Client the owner-only discovery endpoints
// use to populate the console's workspace + board pickers (D29).
type jtypeDiscovery interface {
	ListWorkspaces(ctx context.Context) ([]jtype.Workspace, error)
	ListDocuments(ctx context.Context, workspace string) ([]jtype.Doc, error)
	GetBoard(ctx context.Context, workspace, boardRef string) (*jtype.Board, error)
	GetBoardByDoc(ctx context.Context, workspace, docID string) (*jtype.Board, error)
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
	// D27 — effective cluster jtype kanban config resolver (DB row set from the
	// console > JTYPE_* env). One cached instance shared with the poller +
	// reconciler via Kanban(); a console write Invalidate()s it so a stored base
	// URL takes effect without a restart (fail-visible: never a silent no-op).
	s.kanban = kanbancfg.NewResolver(st, s.cipher, cfg)
	// Default board validator: build a token-bound jtype client off the resolved
	// factory. Overridden by tests with a fake that ignores the factory.
	s.boardValidatorFor = func(f *jtype.Factory, token string) boardValidator { return f.Client(token) }
	// Default jtype discovery client (D29 pickers): same token-bound client; tests
	// override with a fake.
	s.jtypeDiscoveryFor = func(f *jtype.Factory, token string) jtypeDiscovery { return f.Client(token) }
	// Default board embed proxy (D31): the same token-bound client, which carries
	// ProxyDocumentAPI; tests override with a fake.
	s.boardProxyFor = func(f *jtype.Factory, token string) jtypeBoardProxy { return f.Client(token) }
	// D28 — "Connect with jtype" OAuth device flow: an in-memory registry of
	// pending flows + the jtype OAuth device-flow client seam (overridden by tests).
	s.connects = newConnectRegistry()
	s.oauthClientFor = func(baseURL string) oauthClient { return jtypeoauth.NewClient(baseURL, nil) }
	return s
}

// Cipher exposes the token cipher (nil when AUTH_TOKEN_KEY is unset) so callers
// that need to seal/open per-link jtype PATs share the API's instance.
func (s *Server) Cipher() *auth.Cipher { return s.cipher }

// Kanban exposes the shared cluster-kanban-config resolver so the poller +
// reconciler resolve the effective jtype config (base URL + fallback token)
// through the SAME cache the API invalidates on a console PUT/DELETE (D27).
func (s *Server) Kanban() *kanbancfg.Resolver { return s.kanban }

// JtypeDecrypt returns the per-link token decrypt function (the cipher's
// DecryptString), or nil when no cipher is configured — passed to the poller +
// writeback so a link's encrypted PAT is opened the same way it was sealed.
func (s *Server) JtypeDecrypt() func([]byte) (string, error) {
	if s.cipher == nil {
		return nil
	}
	return s.cipher.DecryptString
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

	// @mention webhooks (M7 / blueprint §8 · F13). Public paths, each self-
	// authenticated by its provider's scheme (gitea/github HMAC-sign the body;
	// gitlab echoes the shared token). Registered ONLY when WEBHOOK_SECRET is
	// configured — with no secret the routes are absent (404) and the system runs
	// normally.
	if s.cfg.WebhookSecret != "" {
		mux.HandleFunc("POST /webhooks/gitea", s.handleGiteaWebhook)
		mux.HandleFunc("POST /webhooks/github", s.handleGitHubWebhook)
		mux.HandleFunc("POST /webhooks/gitlab", s.handleGitLabWebhook)
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

	// Model catalog (D21). CRUD + per-model project grants are cluster-admin only
	// (enforced in the handlers). The plaintext API key is never returned; the
	// base_url is admin-only detail. Members read the models granted to a project
	// via GET /projects/{id}/models below.
	mux.Handle("GET /api/v1/system/models", s.authed(s.handleListModels))
	mux.Handle("POST /api/v1/system/models", s.authed(s.handleCreateModel))
	mux.Handle("PATCH /api/v1/system/models/{id}", s.authed(s.handleUpdateModel))
	mux.Handle("DELETE /api/v1/system/models/{id}", s.authed(s.handleDeleteModel))
	mux.Handle("PUT /api/v1/system/models/{id}/grants/{projectID}", s.authed(s.handleGrantModel))
	mux.Handle("DELETE /api/v1/system/models/{id}/grants/{projectID}", s.authed(s.handleRevokeModel))

	// D27 — cluster jtype kanban config (base URL + optional cluster fallback
	// token) a cluster admin sets from the console. Precedence DB > env (see
	// internal/kanbancfg). Cluster-admin only (enforced in the handlers); the
	// fallback token is write-only (never echoed). A PUT/DELETE Invalidate()s the
	// shared resolver so the change takes effect WITHOUT a restart.
	mux.Handle("GET /api/v1/system/kanban", s.authed(s.handleGetKanbanConfig))
	mux.Handle("PUT /api/v1/system/kanban", s.authed(s.handlePutKanbanConfig))
	mux.Handle("DELETE /api/v1/system/kanban", s.authed(s.handleDeleteKanbanConfig))

	// D28 — "Connect with jtype" device flow for the CLUSTER fallback token. POST
	// starts a flow (device_code held server-side; user_code returned); GET polls
	// it, sealing the minted token into cluster_kanban_config on complete.
	// Cluster-admin only (enforced in the handlers).
	mux.Handle("POST /api/v1/system/kanban/connect", s.authed(s.handleStartKanbanConnect))
	mux.Handle("GET /api/v1/system/kanban/connect/{connectID}", s.authed(s.handlePollKanbanConnect))

	// Feature E/F6 — jtype kanban links. Management (create/delete) is downshifted
	// to the project OWNER via the project-scoped routes below (D25). The system
	// route is retained as a cluster-admin READ-ONLY cross-project overview; the
	// old POST/DELETE /system/kanban/links are taken down (console migrated).
	mux.Handle("GET /api/v1/system/kanban/links", s.authed(s.handleListKanbanLinks))

	// User search (any logged-in user; for the add-member picker).
	mux.Handle("GET /api/v1/users", s.authed(s.handleSearchUsers))

	mux.Handle("POST /api/v1/projects", s.authed(s.handleCreateProject))
	mux.Handle("GET /api/v1/projects", s.authed(s.handleListProjects))
	mux.Handle("GET /api/v1/projects/{id}", s.authed(s.handleGetProject))
	mux.Handle("PATCH /api/v1/projects/{id}", s.authed(s.handleUpdateProject))
	mux.Handle("DELETE /api/v1/projects/{id}", s.authed(s.handleDeleteProject))

	// Models a project is granted (D21). Member+; returns only id/name/model_name
	// (never the base_url or key) plus an env_fallback flag for the ModelGate.
	mux.Handle("GET /api/v1/projects/{id}/models", s.authed(s.handleListProjectModels))

	// Integrations (D19 / F5) — project-level git host bindings with a bot
	// credential. Listing + repo discovery are member+ (a member may add a repo off
	// a project's existing integration); create/rotate/delete are owner-managed. The
	// token is write-only (never echoed); create/rotate verify connectivity to the
	// provider (discovering bot_username) and validate the host against the cluster
	// allowlist (D20).
	mux.Handle("GET /api/v1/projects/{id}/integrations", s.authed(s.handleListIntegrations))
	mux.Handle("POST /api/v1/projects/{id}/integrations", s.authed(s.handleCreateIntegration))
	mux.Handle("GET /api/v1/projects/{id}/integrations/{iid}/repos", s.authed(s.handleListIntegrationRepos))
	mux.Handle("PATCH /api/v1/integrations/{iid}", s.authed(s.handleUpdateIntegration))
	mux.Handle("DELETE /api/v1/integrations/{iid}", s.authed(s.handleDeleteIntegration))

	// Kanban links a project owns (F6 / D25). Owner-managed: bind a jtype board
	// column to one of the project's services, with an optional per-link jtype PAT
	// (write-only). Board columns are validated against the live jtype board at
	// create time with that token (or the cluster fallback).
	mux.Handle("GET /api/v1/projects/{id}/kanban/links", s.authed(s.handleListProjectKanbanLinks))
	mux.Handle("POST /api/v1/projects/{id}/kanban/links", s.authed(s.handleCreateProjectKanbanLink))
	// PATCH rotates/clears ONLY the link's per-link token (claims retained, P2).
	mux.Handle("PATCH /api/v1/projects/{id}/kanban/links/{linkID}", s.authed(s.handleUpdateProjectKanbanLink))
	mux.Handle("DELETE /api/v1/projects/{id}/kanban/links/{linkID}", s.authed(s.handleDeleteProjectKanbanLink))
	// D28 — "Connect with jtype" device flow for a link's per-link token (create
	// the link blank first, then connect). Owner only; POST starts, GET polls and
	// seals the minted token into kanban_links.token_enc on complete.
	mux.Handle("POST /api/v1/projects/{id}/kanban/links/{linkID}/connect", s.authed(s.handleStartLinkConnect))
	mux.Handle("GET /api/v1/projects/{id}/kanban/links/{linkID}/connect/{connectID}", s.authed(s.handlePollLinkConnect))
	// D29 — jtype discovery for the console's cascading pickers (owner only). Both
	// use the EFFECTIVE cluster factory + token; the token is NEVER serialized.
	mux.Handle("GET /api/v1/projects/{id}/kanban/jtype/workspaces", s.authed(s.handleListJtypeWorkspaces))
	mux.Handle("GET /api/v1/projects/{id}/kanban/jtype/boards", s.authed(s.handleListJtypeBoards))
	// D31 — member+ board embed proxy: gate the console's Kanban button (board/links)
	// and render the real jtype board through a server-side proxy so the effective
	// jtype token never reaches the browser. Every documents/* handler enforces the
	// confused-deputy guard (the ?workspace= must be one of THIS project's links).
	// Reads and writes are BOTH member+ (write matches run-dispatch authority; a
	// board move is what the poller turns into a run). The token is NEVER serialized.
	mux.Handle("GET /api/v1/projects/{id}/kanban/board/links", s.authed(s.handleListBoardEmbedLinks))
	mux.Handle("GET /api/v1/projects/{id}/kanban/board/documents", s.authed(s.handleBoardListDocuments))
	mux.Handle("GET /api/v1/projects/{id}/kanban/board/documents/{docID}", s.authed(s.handleBoardGetDocument))
	mux.Handle("POST /api/v1/projects/{id}/kanban/board/documents/save", s.authed(s.handleBoardSaveDocument))

	// Project-scoped API keys (F12 / D24) — a revocable automation credential
	// bound to exactly one project, replacing the CONSOLE_TOKEN borrow-pattern
	// for external/CI use. Management is owner-only; a principal authenticated
	// BY a key can never call these routes itself (see api/apikeys.go / D24 "no
	// self-renewal privilege escalation"). The plaintext is returned once, at
	// creation, and never again.
	mux.Handle("GET /api/v1/projects/{id}/apikeys", s.authed(s.handleListAPIKeys))
	mux.Handle("POST /api/v1/projects/{id}/apikeys", s.authed(s.handleCreateAPIKey))
	mux.Handle("DELETE /api/v1/projects/{id}/apikeys/{keyID}", s.authed(s.handleRevokeAPIKey))

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

	// Schedules (F11 / D24) — service-level cron triggers. Listing is member+;
	// create/update/delete are owner-managed. The schedule poller dispatches
	// origin=schedule runs off these; an invalid/too-frequent cron is a
	// fail-visible 400 at write time.
	mux.Handle("GET /api/v1/services/{id}/schedules", s.authed(s.handleListServiceSchedules))
	mux.Handle("POST /api/v1/services/{id}/schedules", s.authed(s.handleCreateServiceSchedule))
	mux.Handle("PATCH /api/v1/schedules/{sid}", s.authed(s.handleUpdateSchedule))
	mux.Handle("DELETE /api/v1/schedules/{sid}", s.authed(s.handleDeleteSchedule))

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
	// Session resume (F9b / D23 ①②): continue a FINISHED session run in a new run
	// that reloads the same ACP session. member+ (same as dispatch/retry).
	mux.Handle("POST /api/v1/runs/{id}/resume", s.authed(s.handleResumeRun))
	// Multi-turn session (D22): feed a follow-up message to a session run, or wind
	// the session down. member+ (same as run dispatch).
	mux.Handle("POST /api/v1/runs/{id}/messages", s.authed(s.handleSendMessage))
	mux.Handle("POST /api/v1/runs/{id}/finish", s.authed(s.handleFinishSession))
	// Session permission approval (F8b / D22): answer a pending permission
	// request of a permission_mode=approval session. member+ (a decision is a
	// mutation; viewers get read-only cards).
	mux.Handle("POST /api/v1/runs/{id}/permission-response", s.authed(s.handlePermissionResponse))
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
	// Multi-turn session (D22): the runner's acpdrive reports each turn's
	// completion and long-polls for the next user message. RUN_TOKEN authed.
	mux.Handle("POST /internal/v1/runs/{id}/turn-complete", s.runToken(s.handleTurnComplete))
	mux.Handle("GET /internal/v1/runs/{id}/next-prompt", s.runToken(s.handleNextPrompt))
	// Session permission approval (F8b): acpdrive's decision poll. Hard
	// constraint: an UNKNOWN request_id answers 204 (pending), never 404 — see
	// handlePermissionDecision.
	mux.Handle("GET /internal/v1/runs/{id}/permissions/{request_id}/decision", s.runToken(s.handlePermissionDecision))
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
		// Stash the already-loaded run so hot internal handlers (the LLM proxy)
		// reuse it instead of re-reading (P4).
		h(w, r.WithContext(withRunToken(r.Context(), run)), runID)
	})
}

// runTokenCtxKey is the context key for the run resolved by the runToken middleware.
type runTokenCtxKey struct{}

// withRunToken stores the run the runToken middleware verified so a handler can
// reuse it without a second GetRun.
func withRunToken(ctx context.Context, run *domain.Run) context.Context {
	return context.WithValue(ctx, runTokenCtxKey{}, run)
}

// runFromToken returns the run stashed by the runToken middleware, or nil.
func runFromToken(ctx context.Context) *domain.Run {
	run, _ := ctx.Value(runTokenCtxKey{}).(*domain.Run)
	return run
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
