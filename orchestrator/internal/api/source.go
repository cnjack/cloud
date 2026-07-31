package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
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
	if binding, bindingErr := s.st.GetServiceRepositoryBinding(r.Context(), svc.ID); bindingErr == nil {
		rawURL = binding.CloneURL
	} else if !errors.Is(bindingErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "could not load repository binding")
		return
	}
	if rawURL == "" {
		writeError(w, http.StatusInternalServerError, "internal", "could not derive repository URL")
		return
	}
	// Resolve a credential (integration bot token when the service is bound, else
	// user OAuth / gitea PAT).
	tok, rerr := s.creds.ResolveForService(r.Context(), svc, run.TriggeredByUserID)
	if rerr != nil && errors.Is(rerr, credentials.ErrIntegrationCredential) {
		// Fail-visible (F5 review C2): an integration-bound service whose bot token
		// cannot be used must fail with the REAL reason — never degrade to an
		// anonymous clone (a private repo would 404 with a misleading error) and
		// never a generic message. Stamp the failure classification now
		// (first-writer-wins, status untouched) so when the runner's clone aborts on
		// this typed error, the run's failure_message carries the credential cause.
		s.log.Error("source: integration credential unavailable", "run", runID, "err", rerr)
		if _, ferr := s.st.SetRunnerFailure(r.Context(), runID, domain.FailureCloneFailed,
			"source unavailable: "+rerr.Error()); ferr != nil {
			s.log.Warn("source: record credential failure", "run", runID, "err", ferr)
		}
		writeError(w, http.StatusConflict, "integration_credential_unavailable",
			"this service's integration credential could not be used: "+rerr.Error())
		return
	}
	// Any OTHER resolution miss (legacy path, no credential at all) is non-fatal:
	// tok stays the zero value → an anonymous URL (public repos still clone; a
	// private one fails visibly at git clone time).
	authed := tok.AuthedURL(rawURL, svc.Provider)

	data, err := s.srcCache.Get(runID, func(dst string) error {
		if run.Kind == domain.RunKindReview && svc.Provider == domain.ProviderGitHub &&
			run.PRNumber > 0 && run.PRHeadBranch != "" {
			return s.git.CreatePullRequestSourceBundle(r.Context(), authed, dst, run.PRNumber, run.PRHeadBranch)
		}
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
	// Record the push branch so the run enters the right scan. It is the run's
	// deterministic branch — jcode/run-<id> for a normal draft-PR run, or the
	// existing PR head branch for a webhook @mention task (M7). git_branch is set
	// by the orchestrator (never runner-reported now) so runner + control plane
	// always agree without the runner reporting it.
	run, err := s.st.GetRun(r.Context(), runID)
	if err != nil {
		s.log.Error("ingest bundle: load run", "run", runID, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	branch := domain.RunPushBranch(run)
	if run.Session {
		if _, err := s.st.PutSessionRunBundle(r.Context(), runID, data); err != nil {
			if errors.Is(err, store.ErrInvalidTransition) {
				writeError(w, http.StatusConflict, "bundle_closed", "the session has already finished accepting bundles")
				return
			}
			s.log.Error("ingest session bundle", "run", runID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not store bundle")
			return
		}
	} else if err := s.st.PutRunBundle(r.Context(), runID, data); err != nil {
		s.log.Error("ingest bundle", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store bundle")
		return
	}
	if _, err := s.st.SetRunGit(r.Context(), runID, branch, ""); err != nil {
		s.log.Warn("ingest bundle: record branch", "run", runID, "err", err)
	}
	// PutSessionRunBundle stores the cumulative bytes and advances bundle_rev in
	// one transaction, serialized with terminal status and Ready reconciliation.
	s.emitArtifactEvent(r.Context(), runID, string(domain.ArtifactBundle), len(data))
	writeJSON(w, http.StatusCreated, map[string]any{"kind": string(domain.ArtifactBundle), "bytes": len(data)})
}

const structuredReviewMediaType = "application/vnd.jcode.review+json"

// handleIngestReview accepts validated structured review output from new
// runners while retaining text/plain for rolling-upgrade compatibility.
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
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "empty review output")
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == structuredReviewMediaType {
		var result domain.ReviewResult
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_review_result", "review output is not valid structured JSON")
			return
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_review_result", "review output must contain exactly one JSON object")
			return
		}
		if err := result.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_review_result", err.Error())
			return
		}
		if _, err := s.st.SetReviewResult(r.Context(), runID, result); err != nil {
			s.writeReviewStoreError(w, runID, err)
			return
		}
		// Keep the existing Console/read API useful during a rolling upgrade.
		if _, err := s.st.SetReviewOutput(r.Context(), runID, result.RenderSummary(false)); err != nil {
			s.writeReviewStoreError(w, runID, err)
			return
		}
		s.emitArtifactEvent(r.Context(), runID, "review", len(data))
		writeJSON(w, http.StatusCreated, map[string]any{"kind": "review", "format": "structured", "bytes": len(data)})
		return
	}
	md := string(data)
	if _, err := s.st.SetReviewOutput(r.Context(), runID, md); err != nil {
		s.writeReviewStoreError(w, runID, err)
		return
	}
	s.emitArtifactEvent(r.Context(), runID, "review", len(md))
	writeJSON(w, http.StatusCreated, map[string]any{"kind": "review", "format": "markdown", "bytes": len(md)})
}

func (s *Server) writeReviewStoreError(w http.ResponseWriter, runID string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	s.log.Error("ingest review", "run", runID, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "could not store review output")
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
