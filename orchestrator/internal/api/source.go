package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// maxBundleBytes caps an uploaded run bundle (blueprint §3: ≤16MiB). The runner
// self-checks the same limit before POSTing; the server enforces it as a 413.
const maxBundleBytes = 16 << 20

// maxReviewBytes caps a review-output upload (defensive; reviews are small).
const maxReviewBytes = 1 << 20

// sourceCache serves orchestrator-generated source bundles, generating each
// lazily on first request and caching it on disk with a TTL. A per-key mutex
// guards generation so concurrent requests for the same run build the bundle
// once; expired files are swept opportunistically.
type sourceCache struct {
	dir string
	ttl time.Duration

	mu      sync.Mutex
	keyMu   map[string]*sync.Mutex
	entries map[string]srcEntry
}

type srcEntry struct {
	path   string
	expiry time.Time
}

func newSourceCache(ttl time.Duration) *sourceCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	dir := filepath.Join(os.TempDir(), "jcloud-source")
	_ = os.MkdirAll(dir, 0o700)
	return &sourceCache{dir: dir, ttl: ttl, keyMu: map[string]*sync.Mutex{}, entries: map[string]srcEntry{}}
}

// lockFor returns the per-key mutex, creating it under the cache lock. It also
// sweeps expired entries so stale bundle files do not accumulate.
func (c *sourceCache) lockFor(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()
	m, ok := c.keyMu[key]
	if !ok {
		m = &sync.Mutex{}
		c.keyMu[key] = m
	}
	return m
}

func (c *sourceCache) sweepLocked() {
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expiry) {
			_ = os.Remove(e.path)
			delete(c.entries, k)
		}
	}
}

// Get returns the cached bundle bytes for key, generating them via gen(dstPath)
// on a miss/expiry. gen must write a git bundle to the given path.
func (c *sourceCache) Get(key string, gen func(dst string) error) ([]byte, error) {
	km := c.lockFor(key)
	km.Lock()
	defer km.Unlock()

	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && time.Now().Before(e.expiry) {
		if data, err := os.ReadFile(e.path); err == nil {
			return data, nil
		}
		// File vanished — fall through and regenerate.
	}

	path := filepath.Join(c.dir, key+".bundle")
	if err := gen(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[key] = srcEntry{path: path, expiry: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return data, nil
}

// handleGetSource serves the orchestrator-pre-clone source bundle for a run's
// provider service (blueprint §3, SOURCE_MODE=fetch). The runner GETs this with
// its RUN_TOKEN, then `git clone`s the bundle locally — so a PRIVATE repo is
// readable without any credential ever entering the pod. The bundle is built
// with the triggering user's token (or the fallback gitea PAT, or anonymously
// for a public repo when neither is available).
func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request, runID string) {
	run, err := s.st.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}
	svc, err := s.st.GetService(r.Context(), run.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if svc.RepoKind != domain.RepoKindProvider {
		writeError(w, http.StatusBadRequest, "bad_request", "source bundle is only available for provider services")
		return
	}
	if s.git == nil || !s.git.Available() {
		writeError(w, http.StatusInternalServerError, "internal", "git is not available on the orchestrator")
		return
	}
	rawURL := domain.ServiceCloneURL(*svc, s.cfg.GiteaURL)
	if rawURL == "" {
		writeError(w, http.StatusInternalServerError, "internal", "could not derive repository URL")
		return
	}
	// Resolve a credential; on none, tok is the zero value → an anonymous URL
	// (public repos still clone). Any resolution error is non-fatal here.
	tok, _ := s.creds.Resolve(r.Context(), svc.Provider, run.TriggeredByUserID)
	authed := tok.AuthedURL(rawURL, svc.Provider)

	data, err := s.srcCache.Get(runID, func(dst string) error {
		return s.git.CreateSourceBundle(r.Context(), authed, dst)
	})
	if err != nil {
		s.log.Error("source: build bundle", "run", runID, "err", err)
		writeError(w, http.StatusBadGateway, "source_failed", "could not build the source bundle")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleIngestBundle stores a draft_pr agent run's git bundle (raw
// application/octet-stream body, ≤16MiB → 413 otherwise) and records the branch
// the orchestrator will push, which is what puts the run in the PR-open scan
// (blueprint §3). The runner no longer pushes.
func (s *Server) handleIngestBundle(w http.ResponseWriter, r *http.Request, runID string) {
	// Read one byte past the limit so an over-size upload is detectable as 413.
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBundleBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read bundle body")
		return
	}
	if int64(len(data)) > maxBundleBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "bundle exceeds the 16MiB limit")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "empty bundle")
		return
	}
	if err := s.st.PutRunBundle(r.Context(), runID, data); err != nil {
		s.log.Error("ingest bundle", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store bundle")
		return
	}
	// Record the push branch so the run enters the right scan. It is the run's
	// deterministic branch — jcode/run-<id> for a normal draft-PR run, or the
	// existing PR head branch for a webhook @mention task (M7). git_branch is set
	// by the orchestrator (never runner-reported now) so runner + control plane
	// always agree without the runner reporting it.
	branch := domain.RunBranchName(runID)
	var isSession bool
	if run, err := s.st.GetRun(r.Context(), runID); err == nil {
		branch = domain.RunPushBranch(run)
		isSession = run.Session
	} else {
		s.log.Warn("ingest bundle: load run for push branch", "run", runID, "err", err)
	}
	if _, err := s.st.SetRunGit(r.Context(), runID, branch, ""); err != nil {
		s.log.Warn("ingest bundle: record branch", "run", runID, "err", err)
	}
	// Session (D22): every turn that produces new changes re-uploads a fresh
	// cumulative bundle. Bump bundle_rev so the session-push reconcile pass
	// opens the draft PR on the first bundle and ff-updates the same branch on
	// each subsequent one (bundle_rev > pushed_rev is the "a new bundle awaits a
	// push" signal). A single-shot run never enters that pass (git_branch alone
	// puts it in the ordinary PR scan).
	if isSession {
		if _, err := s.st.BumpBundleRev(r.Context(), runID); err != nil {
			s.log.Warn("ingest bundle: bump bundle rev", "run", runID, "err", err)
		}
	}
	s.emitArtifactEvent(r.Context(), runID, string(domain.ArtifactBundle), len(data))
	writeJSON(w, http.StatusCreated, map[string]any{"kind": string(domain.ArtifactBundle), "bytes": len(data)})
}

// handleIngestReview stores a review run's markdown output (text/plain body).
// The reconcile review pass posts it to the PR (blueprint §3).
func (s *Server) handleIngestReview(w http.ResponseWriter, r *http.Request, runID string) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxReviewBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read review body")
		return
	}
	if int64(len(data)) > maxReviewBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "review exceeds the size limit")
		return
	}
	md := string(data)
	if len(md) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "empty review output")
		return
	}
	if _, err := s.st.SetReviewOutput(r.Context(), runID, md); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		s.log.Error("ingest review", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store review output")
		return
	}
	s.emitArtifactEvent(r.Context(), runID, "review", len(md))
	writeJSON(w, http.StatusCreated, map[string]any{"kind": "review", "bytes": len(md)})
}

// emitArtifactEvent appends a run.artifact event so a live stream signals the
// new payload landed. Best-effort (durability already done by the caller).
func (s *Server) emitArtifactEvent(ctx context.Context, runID, kind string, n int) {
	payload := map[string]any{"kind": kind, "bytes": n}
	if ev, err := s.st.AppendInternalEvent(ctx, runID, domain.EventRunArtifact, payload); err != nil {
		s.log.Warn("emit artifact event", "run", runID, "kind", kind, "err", err)
	} else if s.hub != nil {
		s.hub.Publish(runID, ev)
	}
}
