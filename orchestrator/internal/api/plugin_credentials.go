package api

import (
	"errors"
	"net/http"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// runPluginCredentialsResponse is intentionally the complete wire contract
// between the internal endpoint and the credential sidecar. No ciphertext,
// refresh token, private key, or master key is serialised here.
type runPluginCredentialsResponse struct {
	Credentials []credentials.PluginCredential `json:"credentials"`
}

// handleRunPluginCredentials returns credentials for the exact installations
// that were snapshotted when this run was launched. It MUST NOT inspect the
// installation's current enabled/disabled state: disabling blocks new run
// snapshots, but existing jobs are allowed to finish.
func (s *Server) handleRunPluginCredentials(w http.ResponseWriter, r *http.Request, runID string) {
	run := runFromToken(r.Context())
	if run == nil || run.ID != runID {
		writeError(w, http.StatusUnauthorized, "unauthorized", "run token invalid")
		return
	}
	switch run.Status {
	case domain.StatusScheduling, domain.StatusRunning, domain.StatusAwaitingInput:
		// A task may refresh only while its Job/session is still alive.
	default:
		writeError(w, http.StatusConflict, "run_not_active", "plugin credentials are unavailable after the run ends")
		return
	}
	if s.pluginCredentialIssuer == nil {
		writeError(w, http.StatusConflict, "plugin_credentials_unavailable", "plugin credential issuer is not configured")
		return
	}

	snapshots, err := s.st.ListRunPluginSnapshots(r.Context(), runID)
	if err != nil {
		s.log.Error("list run plugin snapshots", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load run plugin snapshots")
		return
	}
	out := runPluginCredentialsResponse{Credentials: []credentials.PluginCredential{}}
	for _, snapshot := range snapshots {
		// Snapshot material is a launch-time security boundary. Rows created by
		// an older release which cannot be backfilled, or malformed rows, are
		// skipped rather than resolving a current Provider configuration and
		// risking an old grant being sent to a new endpoint.
		if !snapshot.HasFrozenRuntimeMaterial() {
			s.log.Warn("skip incomplete run plugin snapshot", "run", runID, "installation", snapshot.InstallationID)
			continue
		}
		installation, err := s.st.GetPluginInstallation(r.Context(), snapshot.InstallationID)
		if errors.Is(err, store.ErrNotFound) {
			// An unrelated Plugin may be uninstalled while this task is running.
			// Its snapshot is retained for audit, but removing it must not prevent
			// the task from refreshing every other still-authorized Plugin.
			continue
		}
		if err != nil {
			// Do not make a transient/broken Plugin snapshot block the other
			// independent credentials attached to this task.
			s.log.Error("load run plugin installation", "run", runID, "installation", snapshot.InstallationID, "err", err)
			continue
		}
		// A snapshot is project-scoped. Treat corrupt cross-project data as an
		// unavailable credential rather than ever handing one project a second
		// project's access token.
		if installation.ProjectID != run.ProjectID || installation.Provider != snapshot.Provider {
			s.log.Error("run plugin snapshot project mismatch", "run", runID, "installation", installation.ID)
			continue
		}
		credential, err := s.pluginCredentialIssuer.IssueRunPluginSnapshotCredential(r.Context(), &snapshot)
		if err != nil {
			// Never log err: issuer implementations may include provider response
			// text, and credentials must never leak into logs. A malformed or
			// expired snapshot is isolated to that Plugin; the remaining snapshot
			// credentials must still be returned to the active task.
			continue
		}
		out.Credentials = append(out.Credentials, credential)
	}
	writeJSON(w, http.StatusOK, out)
}
