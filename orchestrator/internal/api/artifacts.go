package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// handleGetArtifact returns a run's artifact. Kind defaults to "diff". If
// ?download=1 (or Accept: text/plain) the raw content is returned with a
// filename so the console's "download .diff" works; otherwise JSON.
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	run, err := s.st.GetRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleViewer) {
		return
	}
	kind := domain.ArtifactKind(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = domain.ArtifactDiff
	}
	art, err := s.st.GetArtifact(r.Context(), runID, kind)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "artifact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get artifact")
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+runID+"."+string(kind)+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(art.Content))
		return
	}
	writeJSON(w, http.StatusOK, art)
}

type ingestArtifactReq struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// handleIngestArtifact stores an artifact posted by the runner (per-run token
// auth). Upserts by (run_id, kind).
func (s *Server) handleIngestArtifact(w http.ResponseWriter, r *http.Request, runID string) {
	var req ingestArtifactReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Kind == "" {
		req.Kind = string(domain.ArtifactDiff)
	}
	art := &domain.RunArtifact{
		RunID:     runID,
		Kind:      domain.ArtifactKind(req.Kind),
		Content:   req.Content,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.PutArtifact(r.Context(), art); err != nil {
		s.log.Error("ingest artifact", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store artifact")
		return
	}
	// Emit a run.artifact event so the stream signals availability. The store
	// allocates the seq atomically (no NextEventSeq race with runner ingest).
	payload := map[string]any{"kind": req.Kind, "bytes": len(req.Content)}
	if ev, err := s.st.AppendInternalEvent(r.Context(), runID, domain.EventRunArtifact, payload); err != nil {
		s.log.Warn("ingest artifact: emit event", "run", runID, "err", err)
	} else if s.hub != nil {
		s.hub.Publish(runID, ev)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"kind": req.Kind, "bytes": len(req.Content)})
}
