package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
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
	Archive    systemArchive    `json:"archive"`
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

// systemKanban is the jtype kanban integration snapshot (Feature E/F6; D27). It
// reflects the EFFECTIVE config (the console-managed cluster_kanban_config DB row
// if present, else the JTYPE_* env fallback), not just the raw env. Enabled is
// true when a base URL resolves; Source is where it came from ("db"/"env"/"none")
// so the console can render "DB (console)" / "env" / "off". ClusterTokenSet is the
// effective fallback token flag per source (never the token itself). Reason is set
// only when the config is broken (e.g. a DB fallback token stored but AUTH_TOKEN_KEY
// unset) — surfaced honestly rather than silently falling back (D14 fail-visible).
type systemKanban struct {
	Enabled         bool   `json:"enabled"`
	Source          string `json:"source"`
	Reason          string `json:"reason,omitempty"` // why disabled/broken (empty when healthy)
	BaseURL         string `json:"base_url,omitempty"`
	PollInterval    string `json:"poll_interval,omitempty"`
	ClusterTokenSet bool   `json:"cluster_token_set"`
	// TokenExpiresAt is the effective cluster fallback token's expiry (RFC3339)
	// when it was minted by the "Connect with jtype" device flow (D28); omitted for
	// a hand-pasted / env token (unknown expiry). Never the token itself.
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
}

// systemArchive is the persistent-workspace object-storage archive snapshot
// (F10 / D23 ③). Object storage is a FIRST-CLASS dependency (D14): when it is
// not fully configured Enabled is false and Reason explains exactly what to set,
// so the console Cluster page shows an honest "long-term archive not enabled"
// state rather than a silent no-op. Endpoint/Bucket are non-secret addressing
// (shown only when enabled); the S3 access/secret keys are NEVER serialized.
type systemArchive struct {
	Enabled  bool   `json:"enabled"`
	Reason   string `json:"reason,omitempty"`    // why disabled (empty when enabled)
	Endpoint string `json:"endpoint,omitempty"`  // S3_ENDPOINT (non-secret), only when enabled
	Bucket   string `json:"bucket,omitempty"`    // S3_BUCKET, only when enabled
	IdleDays int    `json:"idle_days,omitempty"` // ARCHIVE_IDLE_DAYS, only when enabled
}

// handleGetSystem returns the cluster-admin system snapshot. Read-only: it
// queries the store for live run counts and reflects config/build metadata.
// Requires the console bearer token (registered via s.console).
func (s *Server) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	// A project-scoped API key (F12 / D24) never reaches a cluster-admin
	// surface, even a read-only one — its authority is capped at RoleMember on
	// exactly its own project (principal.go effectiveRole). The other
	// /system/* routes (models, kanban links) are already gated by
	// requireClusterAdmin, which reports false for a scoped principal; this
	// route has no such per-handler check today (any authenticated user may
	// read it), so the scoped-principal exclusion is made explicit here.
	if principalFrom(r.Context()).isAPIKey() {
		writeError(w, http.StatusForbidden, "forbidden", "project-scoped API keys cannot access cluster-admin endpoints")
		return
	}
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
		Kanban:    s.kanbanStatus(r.Context()),
		Archive:   s.archiveStatus(),
		Namespace: s.cfg.Namespace,
		Launcher:  launcherKind(s.cfg.JobLauncher, s.cfg.DisableK8s),
	}
	writeJSON(w, http.StatusOK, resp)
}

// archiveStatus builds the fail-visible archive snapshot (F10 / D23 ③). Enabled
// requires BOTH object storage configured AND persistent workspace on AND a
// positive idle window; otherwise Reason names the missing piece (D14). Non-
// secret addressing (endpoint/bucket/idle days) is exposed only when enabled;
// the S3 keys are never serialized.
func (s *Server) archiveStatus() systemArchive {
	if reason := s.cfg.ArchiveDisabledReason(); reason != "" {
		return systemArchive{Enabled: false, Reason: reason}
	}
	return systemArchive{
		Enabled:  true,
		Endpoint: s.cfg.S3Endpoint,
		Bucket:   s.cfg.S3Bucket,
		IdleDays: s.cfg.ArchiveIdleDays,
	}
}

// kanbanStatus builds the fail-visible jtype kanban snapshot (D27) from the
// effective cluster config: the console-managed DB row if present, else the
// JTYPE_* env fallback. A resolver error (e.g. a DB fallback token stored while
// AUTH_TOKEN_KEY is unset) is reported as Enabled:false + Reason rather than a
// silent env fallback (D14). PollInterval is env-only (informational). The
// plaintext fallback token is NEVER serialized — only ClusterTokenSet.
func (s *Server) kanbanStatus(ctx context.Context) systemKanban {
	pollInterval := s.cfg.JtypePollInterval.String()
	eff, err := s.kanban.Effective(ctx)
	if err != nil {
		// /system is readable by any authenticated user (not just admins), so the
		// reason is a CURATED message, never a raw store error (which could leak
		// driver detail). The cipher sentinel's text is already non-secret and
		// actionable; anything else collapses to a generic line — the full error
		// stays admin-visible on GET /system/kanban and in the logs.
		reason := "kanban configuration unavailable — see orchestrator logs"
		if errors.Is(err, auth.ErrCipherNotConfigured) {
			reason = auth.ErrCipherNotConfigured.Error()
		}
		s.log.Warn("system: kanban config resolve failed", "err", err)
		return systemKanban{Enabled: false, Source: "none", Reason: reason, PollInterval: pollInterval}
	}
	k := systemKanban{
		Enabled:         eff.Enabled(),
		Source:          string(eff.Source),
		BaseURL:         eff.BaseURL,
		PollInterval:    pollInterval,
		ClusterTokenSet: eff.ClusterTokenSet,
	}
	if eff.ClusterTokenExpiresAt != nil {
		k.TokenExpiresAt = eff.ClusterTokenExpiresAt.UTC().Format(time.RFC3339)
	}
	return k
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
