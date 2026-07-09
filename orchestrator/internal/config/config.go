// Package config loads all runtime configuration from environment variables
// (twelve-factor). No config file is read; see .env.example for the full list.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// HTTP
	ListenAddr string // ADDR, default :8080

	// Auth
	ConsoleToken string // CONSOLE_TOKEN (required) — static bearer for console/CLI

	// Database
	DatabaseURL string // DATABASE_URL (required) — pgx connection string

	// Reconciler
	ReconcileInterval time.Duration // RECONCILE_INTERVAL, default 3s
	MaxConcurrentRuns int           // MAX_CONCURRENT_RUNS, default 4 (0 = unlimited)
	StallTimeout      time.Duration // STALL_TIMEOUT, default 10m (0 = disabled)

	// Multi-turn session guardrails (D22). Cluster-wide defaults a project may
	// override (nil project guardrail => inherit these).
	MaxLiveSessions        int   // MAX_LIVE_SESSIONS, default 2 (0 = unlimited); cap on live (running+awaiting_input) session runs per project
	SessionIdleTimeoutSecs int64 // SESSION_IDLE_TIMEOUT_SECONDS, default 900; awaiting_input idle before auto-finalize
	SessionTTLSecs         int64 // SESSION_TTL_SECONDS, default 14400; whole-session wall-clock budget (drives RUN_TIMEOUT + activeDeadlineSeconds of a session Job)

	// Backoff (Symphony formula; carried for future auto-retry)
	BackoffBaseMs int64 // BACKOFF_BASE_MS, default 10000
	BackoffMaxMs  int64 // BACKOFF_MAX_MS, default 300000

	// Kubernetes
	Kubeconfig     string            // KUBECONFIG (empty => in-cluster)
	Namespace      string            // K8S_NAMESPACE, default "jcloud"
	RunnerImage    string            // RUNNER_IMAGE (required)
	OrchBaseURL    string            // ORCH_BASE_URL (required) — reachable from runner pods
	ModelBaseURL   string            // MODEL_BASE_URL — env fallback for the effective model config (see internal/modelcfg)
	ModelAPIKey    string            // MODEL_API_KEY — env fallback for the effective model config
	ModelName      string            // MODEL_NAME — "provider/model" env fallback; NO silent mock default (fail-visible red line)
	JobTTLSeconds  int32             // JOB_TTL_SECONDS, default 3600
	RunTimeoutSecs int64             // RUN_TIMEOUT_SECONDS, default 1800 (Job activeDeadlineSeconds)
	CPULimit       string            // RUNNER_CPU_LIMIT, default "2"
	MemoryLimit    string            // RUNNER_MEMORY_LIMIT, default "4Gi"
	CPURequest     string            // RUNNER_CPU_REQUEST, default "500m"
	MemoryRequest  string            // RUNNER_MEMORY_REQUEST, default "1Gi"
	ServiceAccount string            // RUNNER_SERVICE_ACCOUNT (optional)
	ExtraJobLabels map[string]string // (reserved) not env-driven yet
	DisableK8s     bool              // DISABLE_K8S=1 — run without a cluster (API-only/dev)

	// Persistent workspace (Feature C; decision D05). When PersistentWorkspace is
	// on, each service gets a long-lived RWO PVC (ws-<serviceID>) mounted at
	// /workspace (git checkout) + $HOME/.jcode (jcode memory) so successive runs of
	// the SAME service reuse the working copy and memory instead of re-cloning. The
	// PVC is a run-time working copy, NOT the authoritative store (D05/D12). OFF by
	// default: every run then clones fresh (today's behaviour), keeping the e2e rig
	// and existing tests unchanged. On also serializes runs per service (an RWO PVC
	// can only attach to one pod at a time), enforced in the reconciler.
	PersistentWorkspace   bool   // PERSISTENT_WORKSPACE, default false
	WorkspacePVCSize      string // WORKSPACE_PVC_SIZE, default "10Gi"
	WorkspaceStorageClass string // WORKSPACE_STORAGE_CLASS, default "" (cluster default)

	// Launcher selection. "kubernetes" (default) schedules K8s Jobs; "process"
	// runs each runner as a local `docker run` container for local dev and the
	// full-loop integration test (see runner/test-integration.sh). "process"
	// needs no cluster and RUNNER_IMAGE must be a locally-available image.
	JobLauncher      string   // JOB_LAUNCHER, default "kubernetes"
	RunnerNetwork    string   // RUNNER_NETWORK — docker network for process launcher (optional)
	RunnerDockerArgs []string // RUNNER_DOCKER_ARGS — extra `docker run` args, space-split (optional)

	// Gitea draft-PR integration. GiteaURL is the Gitea root; GiteaToken is a PAT
	// with repo write scope. In M3 the CONTROL PLANE (not the runner) uses these to
	// clone the source repo, push the runner's bundle branch, and open the draft
	// PR — as the FALLBACK credential for runs with no triggering user (an OAuth
	// user's own token is preferred). The token is NEVER injected into the runner.
	// Both empty => draft-PR mode degrades to diff-only (never fails a run).
	GiteaURL   string // GITEA_URL — Gitea base URL for the PR API + push origin
	GiteaToken string // GITEA_TOKEN — PAT used by the orchestrator to clone/push/PR on behalf of legacy/service-principal runs (M3; never injected into the runner)

	// AllowedGitHosts is ALLOWED_GIT_HOSTS (D20 / F5): a comma-separated cluster
	// allowlist of git hosts an integration may target. Empty => no restriction
	// (any host). Compared host-normalised (domain.NormalizeGitHost) so a full URL
	// or a bare host both match. Read-only in the console (Cluster page card);
	// integration create rejects a host not in the list with 400 host_not_allowed.
	AllowedGitHosts []string

	// SourceBundleTTL is SOURCE_BUNDLE_TTL (default 10m): how long an
	// orchestrator-generated source bundle is cached on disk before regeneration
	// (M3 fetch path — GET /internal/v1/runs/{id}/source).
	SourceBundleTTL time.Duration

	// WebhookSecret is WEBHOOK_SECRET: the shared secret Gitea signs PR-comment
	// webhook bodies with (HMAC-SHA256, hex, header X-Gitea-Signature). Empty =>
	// the /webhooks/gitea route is NOT registered (returns 404) and the @mention
	// trigger is off; the rest of the system runs normally (M7 / blueprint §8).
	WebhookSecret string
	// WebhookURL is WEBHOOK_URL: the gitea-reachable URL of this orchestrator's
	// /webhooks/gitea endpoint (e.g. an in-cluster Service DNS when gitea shares
	// the cluster). When BOTH WebhookURL and WebhookSecret are set, creating a
	// gitea provider service auto-registers the @mention comment webhook on that
	// repository (Drone-style onboarding); empty => no auto-registration.
	WebhookURL string

	// --- Auth / OAuth (M2; multitenant blueprint §2) ---
	// AuthTokenKey is AUTH_TOKEN_KEY: a base64-encoded 32-byte key for the
	// AES-256-GCM encryption of provider tokens in user_identities. Required once
	// any OAuth provider is configured (see Load); ignored (may be empty) when no
	// provider is configured — the system then runs on CONSOLE_TOKEN alone.
	AuthTokenKey string
	// ConsoleURL is CONSOLE_URL: where /auth/callback 302s back to after login.
	ConsoleURL string
	// SessionTTL is SESSION_TTL (default 30d): how long a login session lasts.
	SessionTTL time.Duration
	// OAuthProviders is the set of OAuth providers whose CLIENT_ID is configured.
	// Empty => the auth endpoints report no providers and login is CONSOLE_TOKEN
	// only (backward compatible).
	OAuthProviders []OAuthProviderConfig

	// --- jtype kanban integration (Feature E) --------------------------------
	// JtypeBaseURL is JTYPE_BASE_URL: the jtype document API root
	// (e.g. http://127.0.0.1:13345 or https://jtype.example.com). Empty => the
	// kanban integration is OFF (no poller, no writeback) — the system runs
	// normally without it. NEVER defaults to a mock (fail-visible red line).
	JtypeBaseURL string
	// JtypeToken is JTYPE_TOKEN: a jtype mcp-scope PAT (editor rights). Since F6 /
	// D25 each kanban link carries its OWN encrypted PAT; this cluster token is now
	// only a FALLBACK for links that have no per-link token. Optional — the
	// integration is enabled by JtypeBaseURL alone (per-link tokens can authorise
	// every read/write without a cluster token).
	JtypeToken string
	// JtypePollInterval is JTYPE_POLL_INTERVAL (default 15s): how often the
	// poller scans enabled kanban_links for cards in their trigger column. <=0
	// with a configured base/token disables the poller (writeback still runs).
	JtypePollInterval time.Duration

	// --- schedule triggers (F11 / D24) ---------------------------------------
	// SchedulePollInterval is SCHEDULE_POLL_INTERVAL (default 30s): how often the
	// schedule poller scans enabled schedules for a due cron window. <=0 disables
	// the poller entirely (no scheduled runs dispatch).
	SchedulePollInterval time.Duration
}

// OAuthProviderConfig is one configured OAuth provider (multitenant blueprint
// §2). ID is "gitea" | "github" | "gitlab". ExternalURL is browser-reachable
// (authorize redirect); InternalURL is server-to-server (token exchange + API).
type OAuthProviderConfig struct {
	ID           string
	ClientID     string
	ClientSecret string
	ExternalURL  string
	InternalURL  string
}

// Load resolves configuration from the environment, returning an error listing
// every missing required value at once.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:             getenv("ADDR", ":8080"),
		ConsoleToken:           os.Getenv("CONSOLE_TOKEN"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		ReconcileInterval:      getdur("RECONCILE_INTERVAL", 3*time.Second),
		MaxConcurrentRuns:      getint("MAX_CONCURRENT_RUNS", 4),
		StallTimeout:           getdur("STALL_TIMEOUT", 10*time.Minute),
		MaxLiveSessions:        getint("MAX_LIVE_SESSIONS", 2),
		SessionIdleTimeoutSecs: getint64("SESSION_IDLE_TIMEOUT_SECONDS", 900),
		SessionTTLSecs:         getint64("SESSION_TTL_SECONDS", 14400),
		BackoffBaseMs:          getint64("BACKOFF_BASE_MS", 10000),
		BackoffMaxMs:           getint64("BACKOFF_MAX_MS", 300000),
		Kubeconfig:             os.Getenv("KUBECONFIG"),
		Namespace:              getenv("K8S_NAMESPACE", "jcloud"),
		RunnerImage:            os.Getenv("RUNNER_IMAGE"),
		OrchBaseURL:            os.Getenv("ORCH_BASE_URL"),
		ModelBaseURL:           os.Getenv("MODEL_BASE_URL"),
		ModelAPIKey:            os.Getenv("MODEL_API_KEY"),
		ModelName:              os.Getenv("MODEL_NAME"),
		JobTTLSeconds:          int32(getint("JOB_TTL_SECONDS", 3600)),
		RunTimeoutSecs:         getint64("RUN_TIMEOUT_SECONDS", 1800),
		CPULimit:               getenv("RUNNER_CPU_LIMIT", "2"),
		MemoryLimit:            getenv("RUNNER_MEMORY_LIMIT", "4Gi"),
		CPURequest:             getenv("RUNNER_CPU_REQUEST", "500m"),
		MemoryRequest:          getenv("RUNNER_MEMORY_REQUEST", "1Gi"),
		ServiceAccount:         os.Getenv("RUNNER_SERVICE_ACCOUNT"),
		DisableK8s:             getbool("DISABLE_K8S", false),
		PersistentWorkspace:    getbool("PERSISTENT_WORKSPACE", false),
		WorkspacePVCSize:       getenv("WORKSPACE_PVC_SIZE", "10Gi"),
		WorkspaceStorageClass:  os.Getenv("WORKSPACE_STORAGE_CLASS"),
		JobLauncher:            getenv("JOB_LAUNCHER", "kubernetes"),
		RunnerNetwork:          os.Getenv("RUNNER_NETWORK"),
		RunnerDockerArgs:       strings.Fields(os.Getenv("RUNNER_DOCKER_ARGS")),
		GiteaURL:               os.Getenv("GITEA_URL"),
		GiteaToken:             os.Getenv("GITEA_TOKEN"),
		AllowedGitHosts:        splitCSV(os.Getenv("ALLOWED_GIT_HOSTS")),
		SourceBundleTTL:        getdur("SOURCE_BUNDLE_TTL", 10*time.Minute),
		WebhookSecret:          os.Getenv("WEBHOOK_SECRET"),
		WebhookURL:             os.Getenv("WEBHOOK_URL"),
		AuthTokenKey:           os.Getenv("AUTH_TOKEN_KEY"),
		ConsoleURL:             getenv("CONSOLE_URL", "http://localhost:5173"),
		SessionTTL:             getdur("SESSION_TTL", 30*24*time.Hour),
		OAuthProviders:         loadOAuthProviders(),
		JtypeBaseURL:           os.Getenv("JTYPE_BASE_URL"),
		JtypeToken:             os.Getenv("JTYPE_TOKEN"),
		JtypePollInterval:      getdur("JTYPE_POLL_INTERVAL", 15*time.Second),
		SchedulePollInterval:   getdur("SCHEDULE_POLL_INTERVAL", 30*time.Second),
	}

	var missing []string
	if c.ConsoleToken == "" {
		missing = append(missing, "CONSOLE_TOKEN")
	}
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if !c.DisableK8s {
		if c.RunnerImage == "" {
			missing = append(missing, "RUNNER_IMAGE")
		}
		if c.OrchBaseURL == "" {
			missing = append(missing, "ORCH_BASE_URL")
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	// AUTH_TOKEN_KEY is required (and must be a valid base64 32-byte key) ONLY
	// when at least one OAuth provider is configured — the token cipher is used to
	// store identity tokens. With no providers the system runs on CONSOLE_TOKEN
	// and the key is optional (blueprint §2 / backward compatibility).
	if len(c.OAuthProviders) > 0 {
		if c.AuthTokenKey == "" {
			return nil, fmt.Errorf("AUTH_TOKEN_KEY is required when any AUTH_*_CLIENT_ID is configured")
		}
		if err := validateTokenKey(c.AuthTokenKey); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// oauthProviderDefaults lists the supported providers and their public default
// hosts. Gitea has no public default (it is always self-hosted, so its URLs must
// be configured); github/gitlab default to their public hosts.
var oauthProviderDefaults = []struct{ id, extDef, intDef string }{
	{"gitea", "", ""},
	{"github", "https://github.com", "https://github.com"},
	{"gitlab", "https://gitlab.com", "https://gitlab.com"},
}

// loadOAuthProviders reads AUTH_{P}_CLIENT_ID/_CLIENT_SECRET/_EXTERNAL_URL/
// _INTERNAL_URL for each supported provider. A provider is "configured" iff its
// CLIENT_ID is set; unset providers are simply absent from /auth/providers.
func loadOAuthProviders() []OAuthProviderConfig {
	var out []OAuthProviderConfig
	for _, p := range oauthProviderDefaults {
		prefix := "AUTH_" + strings.ToUpper(p.id) + "_"
		clientID := os.Getenv(prefix + "CLIENT_ID")
		if clientID == "" {
			continue
		}
		out = append(out, OAuthProviderConfig{
			ID:           p.id,
			ClientID:     clientID,
			ClientSecret: os.Getenv(prefix + "CLIENT_SECRET"),
			ExternalURL:  getenv(prefix+"EXTERNAL_URL", p.extDef),
			InternalURL:  getenv(prefix+"INTERNAL_URL", p.intDef),
		})
	}
	return out
}

// validateTokenKey checks AUTH_TOKEN_KEY decodes to exactly 32 bytes (AES-256).
func validateTokenKey(b64 string) error {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("AUTH_TOKEN_KEY is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("AUTH_TOKEN_KEY must decode to 32 bytes for AES-256, got %d", len(key))
	}
	return nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getint(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getint64(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// splitCSV splits a comma-separated env value into trimmed, non-empty entries.
// An empty/whitespace input yields nil (no entries).
func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getbool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
