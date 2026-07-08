package api

import (
	"net/http"
	"sort"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/version"
)

// systemResponse is the read-only admin snapshot returned by GET /api/v1/system.
// It is deliberately minimal and NEVER carries a secret: no tokens, no DB DSN,
// no CONSOLE_TOKEN. gitea_enabled is a boolean derived from whether GITEA_TOKEN
// is set — the token itself is never serialized. See 11-api.md § "System / admin".
type systemResponse struct {
	Version    systemVersion    `json:"version"`
	Capacity   systemCapacity   `json:"capacity"`
	Guardrails systemGuardrails `json:"guardrails"`
	Provider   systemProvider   `json:"provider"`
	Runner     systemRunner     `json:"runner"`
	Auth       systemAuth       `json:"auth"`
	Kanban     systemKanban     `json:"kanban"`
	Namespace  string           `json:"namespace"`
	Launcher   string           `json:"launcher"`
}

// systemAuth is the auth snapshot the console (M4) reads to render the login
// page and the admin user count. providers is the list of configured OAuth
// provider ids (never a secret).
type systemAuth struct {
	Providers  []string `json:"providers"`
	UsersCount int      `json:"users_count"`
}

type systemVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type systemCapacity struct {
	MaxConcurrentRuns int `json:"max_concurrent_runs"`
	Running           int `json:"running"`
	Queued            int `json:"queued"`
	Scheduling        int `json:"scheduling"`
}

type systemGuardrails struct {
	RunTimeoutSeconds int64 `json:"run_timeout_seconds"`
	JobTTLSeconds     int32 `json:"job_ttl_seconds"`
}

type systemProvider struct {
	GiteaEnabled bool   `json:"gitea_enabled"`
	GiteaURL     string `json:"gitea_url"`
	// AllowedGitHosts mirrors the cluster ALLOWED_GIT_HOSTS allowlist (D20 / F5):
	// the hosts a project integration may target. Empty => unrestricted. Read-only
	// (the console Cluster page shows it); never a secret.
	AllowedGitHosts []string `json:"allowed_git_hosts"`
}

type systemRunner struct {
	Image string `json:"image"`
	// PersistentWorkspace mirrors the cluster PERSISTENT_WORKSPACE switch (Feature
	// C / D05): when on, each service keeps a persistent workspace PVC (reused
	// checkout + jcode memory) and runs serialize per service. Purely informational.
	PersistentWorkspace bool `json:"persistent_workspace"`
}

// systemKanban is the jtype kanban integration snapshot (Feature E/F6). Enabled
// is true when JTYPE_BASE_URL is configured — per-link tokens (D25) mean the base
// URL alone enables the integration; the cluster JTYPE_TOKEN is only a fallback,
// surfaced as ClusterTokenSet (never the token itself). The base URL is shown so
// the console can render "on / off" + target.
type systemKanban struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url,omitempty"`
	PollInterval    string `json:"poll_interval,omitempty"`
	ClusterTokenSet bool   `json:"cluster_token_set"`
}

// handleGetSystem returns the cluster-admin system snapshot. Read-only: it
// queries the store for live run counts and reflects config/build metadata.
// Requires the console bearer token (registered via s.console).
func (s *Server) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	counts, err := s.st.CountRunsByStatus(r.Context(),
		domain.StatusRunning, domain.StatusQueued, domain.StatusScheduling)
	if err != nil {
		s.log.Error("count runs by status", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read capacity")
		return
	}

	usersCount, err := s.st.CountUsers(r.Context())
	if err != nil {
		s.log.Error("count users", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read users")
		return
	}

	resp := systemResponse{
		Version: systemVersion{
			Version: version.Version,
			Commit:  version.Commit,
		},
		Capacity: systemCapacity{
			MaxConcurrentRuns: s.cfg.MaxConcurrentRuns,
			Running:           counts[domain.StatusRunning],
			Queued:            counts[domain.StatusQueued],
			Scheduling:        counts[domain.StatusScheduling],
		},
		Guardrails: systemGuardrails{
			RunTimeoutSeconds: s.cfg.RunTimeoutSecs,
			JobTTLSeconds:     s.cfg.JobTTLSeconds,
		},
		Provider: systemProvider{
			// gitea_enabled is the trust signal for draft-PR mode: the PAT is set.
			// The token is NEVER serialized — only this boolean and the base URL.
			GiteaEnabled:    s.cfg.GiteaToken != "",
			GiteaURL:        s.cfg.GiteaURL,
			AllowedGitHosts: nonNilStrings(s.cfg.AllowedGitHosts),
		},
		Runner: systemRunner{
			Image:               s.cfg.RunnerImage,
			PersistentWorkspace: s.cfg.PersistentWorkspace,
		},
		Auth: systemAuth{
			Providers:  s.configuredProviderIDs(),
			UsersCount: usersCount,
		},
		Kanban: systemKanban{
			Enabled:         s.cfg.JtypeBaseURL != "",
			BaseURL:         s.cfg.JtypeBaseURL,
			PollInterval:    s.cfg.JtypePollInterval.String(),
			ClusterTokenSet: s.cfg.JtypeToken != "",
		},
		Namespace: s.cfg.Namespace,
		Launcher:  launcherKind(s.cfg.JobLauncher, s.cfg.DisableK8s),
	}
	writeJSON(w, http.StatusOK, resp)
}

// nonNilStrings returns a non-nil slice so the JSON encodes [] not null for an
// unset allowlist (the console treats [] as "unrestricted").
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// configuredProviderIDs returns the sorted ids of the configured OAuth providers
// for the auth snapshot (never a secret — just "gitea"/"github"/"gitlab").
func (s *Server) configuredProviderIDs() []string {
	out := make([]string, 0, len(s.oauth))
	for id := range s.oauth {
		out = append(out, string(id))
	}
	sort.Strings(out)
	return out
}

// launcherKind reports the effective launcher for the admin snapshot. It mirrors
// the selection in cmd/orchestrator/main.go: DISABLE_K8S wins (nothing schedules),
// then JOB_LAUNCHER=process, else the default kubernetes launcher.
func launcherKind(jobLauncher string, disableK8s bool) string {
	switch {
	case disableK8s:
		return "disabled"
	case jobLauncher == "process":
		return "process"
	default:
		return "kubernetes"
	}
}
