// Package api exposes the orchestrator's HTTP surface using the standard-library
// router (Go 1.22 http.ServeMux, which supports method + wildcard patterns).
//
// Justification for std net/http over chi: the 1.22 mux covers everything we
// need — `POST /api/v1/...` method routing and `{id}` path wildcards via
// r.PathValue — so a third-party router would add a dependency for no gain. The
// std-lib-first directive applies.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/k8s"
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
}

// New builds a Server. launcher may be nil (K8s disabled).
func New(st store.Store, cfg *config.Config, log *slog.Logger, hub *sse.Hub, launcher k8s.JobLauncher) *Server {
	return &Server{st: st, cfg: cfg, log: log, hub: hub, launcher: launcher}
}

// Handler builds the full route tree with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health (unauthenticated).
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Console/CLI endpoints — require the static CONSOLE_TOKEN.
	mux.Handle("POST /api/v1/projects", s.console(s.handleCreateProject))
	mux.Handle("GET /api/v1/projects", s.console(s.handleListProjects))
	mux.Handle("GET /api/v1/projects/{id}", s.console(s.handleGetProject))
	mux.Handle("PATCH /api/v1/projects/{id}", s.console(s.handleUpdateProject))
	mux.Handle("DELETE /api/v1/projects/{id}", s.console(s.handleDeleteProject))

	mux.Handle("POST /api/v1/projects/{id}/runs", s.console(s.handleCreateRun))
	mux.Handle("GET /api/v1/projects/{id}/runs", s.console(s.handleListRuns))
	mux.Handle("GET /api/v1/runs", s.console(s.handleListRuns))
	mux.Handle("GET /api/v1/runs/{id}", s.console(s.handleGetRun))
	mux.Handle("GET /api/v1/runs/{id}/events", s.console(s.handleListEvents))
	// SSE stream also accepts the console token via ?access_token= because a
	// browser EventSource cannot set an Authorization header (see 11-api.md §2.3).
	mux.Handle("GET /api/v1/runs/{id}/stream", s.consoleStream(s.handleStream))
	mux.Handle("GET /api/v1/runs/{id}/artifact", s.console(s.handleGetArtifact))
	mux.Handle("POST /api/v1/runs/{id}/cancel", s.console(s.handleCancelRun))
	mux.Handle("POST /api/v1/runs/{id}/retry", s.console(s.handleRetryRun))

	// Internal endpoints — require the per-run RUN_TOKEN.
	mux.Handle("POST /internal/v1/runs/{id}/events", s.runToken(s.handleIngestEvents))
	mux.Handle("POST /internal/v1/runs/{id}/artifact", s.runToken(s.handleIngestArtifact))

	return s.recover(s.logRequests(mux))
}

// --- middleware -------------------------------------------------------------

// console wraps a handler with static-console-token auth.
func (s *Server) console(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
		if !ok || !auth.ConstantTimeEqual(tok, s.cfg.ConsoleToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid console bearer token required")
			return
		}
		h(w, r)
	})
}

// consoleStream authenticates the SSE endpoint, accepting the console token
// EITHER as a Bearer header (CLI/fetch clients) OR as an ?access_token= query
// param. The query-param fallback exists because the browser's native
// EventSource cannot attach an Authorization header. Both use the same
// constant-time compare. Only the read-only stream endpoint allows this; every
// mutating endpoint remains header-only.
func (s *Server) consoleStream(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
		if !ok {
			tok = r.URL.Query().Get("access_token")
		}
		if tok == "" || !auth.ConstantTimeEqual(tok, s.cfg.ConsoleToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid console bearer token or access_token required")
			return
		}
		h(w, r)
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
