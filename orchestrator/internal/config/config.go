// Package config loads all runtime configuration from environment variables
// (twelve-factor). No config file is read; see .env.example for the full list.
package config

import (
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
	return c, nil
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
