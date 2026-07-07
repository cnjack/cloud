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

	// Backoff (Symphony formula; carried for future auto-retry)
	BackoffBaseMs int64 // BACKOFF_BASE_MS, default 10000
	BackoffMaxMs  int64 // BACKOFF_MAX_MS, default 300000

	// Kubernetes
	Kubeconfig     string            // KUBECONFIG (empty => in-cluster)
	Namespace      string            // K8S_NAMESPACE, default "jcloud"
	RunnerImage    string            // RUNNER_IMAGE (required)
	OrchBaseURL    string            // ORCH_BASE_URL (required) — reachable from runner pods
	ModelBaseURL   string            // MODEL_BASE_URL — passed to runner
	ModelAPIKey    string            // MODEL_API_KEY — passed to runner
	ModelName      string            // MODEL_NAME — "provider/model" passed to runner (default "mock/mock-model")
	JobTTLSeconds  int32             // JOB_TTL_SECONDS, default 3600
	RunTimeoutSecs int64             // RUN_TIMEOUT_SECONDS, default 1800 (Job activeDeadlineSeconds)
	CPULimit       string            // RUNNER_CPU_LIMIT, default "2"
	MemoryLimit    string            // RUNNER_MEMORY_LIMIT, default "4Gi"
	CPURequest     string            // RUNNER_CPU_REQUEST, default "500m"
	MemoryRequest  string            // RUNNER_MEMORY_REQUEST, default "1Gi"
	ServiceAccount string            // RUNNER_SERVICE_ACCOUNT (optional)
	ExtraJobLabels map[string]string // (reserved) not env-driven yet
	DisableK8s     bool              // DISABLE_K8S=1 — run without a cluster (API-only/dev)

	// Launcher selection. "kubernetes" (default) schedules K8s Jobs; "process"
	// runs each runner as a local `docker run` container for local dev and the
	// full-loop integration test (see runner/test-integration.sh). "process"
	// needs no cluster and RUNNER_IMAGE must be a locally-available image.
	JobLauncher      string   // JOB_LAUNCHER, default "kubernetes"
	RunnerNetwork    string   // RUNNER_NETWORK — docker network for process launcher (optional)
	RunnerDockerArgs []string // RUNNER_DOCKER_ARGS — extra `docker run` args, space-split (optional)

	// Gitea draft-PR integration (ST-1; single-tenant MVP). GiteaURL is the Gitea
	// root; GiteaToken is a personal access token with repo write scope. When a
	// project is git_mode=draft_pr, the reconciler uses these to open the draft
	// PR, and the runner receives GiteaToken (as GIT_TOKEN) to push the branch.
	// Both empty => draft-PR mode degrades to diff-only (never fails a run).
	GiteaURL   string // GITEA_URL — Gitea base URL for the PR API + push origin
	GiteaToken string // GITEA_TOKEN — PAT injected to runner + used by orchestrator

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
		ListenAddr:        getenv("ADDR", ":8080"),
		ConsoleToken:      os.Getenv("CONSOLE_TOKEN"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		ReconcileInterval: getdur("RECONCILE_INTERVAL", 3*time.Second),
		MaxConcurrentRuns: getint("MAX_CONCURRENT_RUNS", 4),
		StallTimeout:      getdur("STALL_TIMEOUT", 10*time.Minute),
		BackoffBaseMs:     getint64("BACKOFF_BASE_MS", 10000),
		BackoffMaxMs:      getint64("BACKOFF_MAX_MS", 300000),
		Kubeconfig:        os.Getenv("KUBECONFIG"),
		Namespace:         getenv("K8S_NAMESPACE", "jcloud"),
		RunnerImage:       os.Getenv("RUNNER_IMAGE"),
		OrchBaseURL:       os.Getenv("ORCH_BASE_URL"),
		ModelBaseURL:      os.Getenv("MODEL_BASE_URL"),
		ModelAPIKey:       os.Getenv("MODEL_API_KEY"),
		ModelName:         getenv("MODEL_NAME", "mock/mock-model"),
		JobTTLSeconds:     int32(getint("JOB_TTL_SECONDS", 3600)),
		RunTimeoutSecs:    getint64("RUN_TIMEOUT_SECONDS", 1800),
		CPULimit:          getenv("RUNNER_CPU_LIMIT", "2"),
		MemoryLimit:       getenv("RUNNER_MEMORY_LIMIT", "4Gi"),
		CPURequest:        getenv("RUNNER_CPU_REQUEST", "500m"),
		MemoryRequest:     getenv("RUNNER_MEMORY_REQUEST", "1Gi"),
		ServiceAccount:    os.Getenv("RUNNER_SERVICE_ACCOUNT"),
		DisableK8s:        getbool("DISABLE_K8S", false),
		JobLauncher:       getenv("JOB_LAUNCHER", "kubernetes"),
		RunnerNetwork:     os.Getenv("RUNNER_NETWORK"),
		RunnerDockerArgs:  strings.Fields(os.Getenv("RUNNER_DOCKER_ARGS")),
		GiteaURL:          os.Getenv("GITEA_URL"),
		GiteaToken:        os.Getenv("GITEA_TOKEN"),
		AuthTokenKey:      os.Getenv("AUTH_TOKEN_KEY"),
		ConsoleURL:        getenv("CONSOLE_URL", "http://localhost:5173"),
		SessionTTL:        getdur("SESSION_TTL", 30*24*time.Hour),
		OAuthProviders:    loadOAuthProviders(),
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

func getbool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
