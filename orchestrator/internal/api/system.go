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
}

type systemRunner struct {
	Image string `json:"image"`
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
			GiteaEnabled: s.cfg.GiteaToken != "",
			GiteaURL:     s.cfg.GiteaURL,
		},
		Runner: systemRunner{
			Image: s.cfg.RunnerImage,
		},
		Auth: systemAuth{
			Providers:  s.configuredProviderIDs(),
			UsersCount: usersCount,
		},
		Namespace: s.cfg.Namespace,
		Launcher:  launcherKind(s.cfg.JobLauncher, s.cfg.DisableK8s),
	}
	writeJSON(w, http.StatusOK, resp)
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
