package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// handleRequestReview creates a kind=review run that reviews the pull request an
// agent run opened (multitenant blueprint §4/§5). It requires member on the run's
// project (viewer 403). Preconditions (409): the source run must be a SUCCEEDED
// AGENT run that has an open PR (pr_url set). The review run reuses the ordinary
// create-run pipeline — it is queued, the reconciler schedules it, and jobEnv
// injects PR_HEAD/PR_BASE from the review run's pr_head_branch/pr_base_branch
// columns (M3 contract). Multiple reviews of the same PR are allowed.
func (s *Server) handleRequestReview(w http.ResponseWriter, r *http.Request) {
	src, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	// Requesting a review is a mutation: member+ on the run's project.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), src.ProjectID, domain.RoleMember) {
		return
	}

	// Preconditions — a review only makes sense against a succeeded agent run that
	// opened a PR. All three are state conflicts (409), consistent with cancel/retry.
	if src.Kind == domain.RunKindReview {
		writeError(w, http.StatusConflict, "conflict", "cannot review a review run")
		return
	}
	if src.Status != domain.StatusSucceeded {
		writeError(w, http.StatusConflict, "conflict", "only a succeeded run can be reviewed")
		return
	}
	if src.PRURL == "" {
		writeError(w, http.StatusConflict, "conflict", "this run has no pull request to review")
		return
	}
	// Fail-visible gate: a review run also invokes the LLM — refuse it if none is
	// configured (CLAUDE.md red line #1).
	if !s.modelConfigured(w, r) {
		return
	}

	svc, err := s.st.GetService(r.Context(), src.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	// Guardrail: a review run is a fresh dispatch — honour the project's
	// provider_allowlist (403 if the service's provider is no longer permitted).
	if !s.providerDispatchAllowed(w, r, svc) {
		return
	}

	review := newReviewRun(src, svc, principalFrom(r.Context()).userIDPtr())
	if err := s.st.CreateRun(r.Context(), review); err != nil {
		s.log.Error("create review run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create review run")
		return
	}
	s.emitStatus(r.Context(), review)
	writeJSON(w, http.StatusCreated, review)
}

// newReviewRun builds a queued review run associated with the agent run's PR: it
// diffs pr_base_branch...pr_head_branch (the runner reads these from PR_BASE /
// PR_HEAD env, injected by the reconciler from these columns), and the reconcile
// review pass finds the target PR by pr_head_branch. head is the source run's
// pushed branch (git_branch); base is the service default branch.
func newReviewRun(src *domain.Run, svc *domain.Service, triggeredBy *string) *domain.Run {
	return &domain.Run{
		ID:                domain.NewID(),
		ProjectID:         src.ProjectID,
		ServiceID:         src.ServiceID,
		Prompt:            "AI review of PR " + src.PRURL,
		Status:            domain.StatusQueued,
		Kind:              domain.RunKindReview,
		Phase:             "Queued",
		TriggeredByUserID: triggeredBy,
		PRHeadBranch:      src.GitBranch,
		PRBaseBranch:      svc.DefaultBranch,
		Attempt:           1,
		CreatedAt:         time.Now().UTC(),
	}
}

// reviewRunView is one review run in the PR view, enriched with the triggering
// user's display name so the console needs no second lookup.
type reviewRunView struct {
	ID                     string     `json:"id"`
	Status                 string     `json:"status"`
	ReviewOutput           string     `json:"review_output"`
	ReviewPostedAt         *time.Time `json:"review_posted_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	TriggeredByDisplayName string     `json:"triggered_by_display_name,omitempty"`
}

// prView is the GET /runs/{id}/pr response: the PR link, its live state, the head
// branch, and the review runs targeting it (newest first).
type prView struct {
	URL        string          `json:"url"`
	State      string          `json:"state"`
	HeadBranch string          `json:"head_branch"`
	ReviewRuns []reviewRunView `json:"review_runs"`
}

// handleGetPR returns a run's pull request view (multitenant blueprint §4/§5).
// Readable by any member (viewer+). The PR state is queried live from the
// provider with the triggering user's token (or the gitea PAT fallback); ANY
// failure degrades to state="unknown" — this endpoint never 500s on a provider
// hiccup. The review_runs are the same-service kind=review runs whose head
// branch matches this run's pushed branch, newest first.
func (s *Server) handleGetPR(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleViewer) {
		return
	}
	svc, err := s.st.GetService(r.Context(), run.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}

	out := prView{
		URL:        run.PRURL,
		State:      s.prState(r.Context(), run, svc),
		HeadBranch: run.GitBranch,
		ReviewRuns: s.reviewRunsFor(r.Context(), run, svc),
	}
	writeJSON(w, http.StatusOK, out)
}

// prState queries the provider for the run's PR state, returning "unknown" on any
// error / missing precondition. It NEVER returns an error — the caller treats the
// PR view as best-effort so a provider outage can't fail the page.
func (s *Server) prState(ctx context.Context, run *domain.Run, svc *domain.Service) string {
	const unknown = "unknown"
	if run.PRURL == "" || run.PRNumber <= 0 || svc.RepoKind != domain.RepoKindProvider {
		return unknown
	}
	if s.factory == nil || s.creds == nil {
		return unknown
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		return unknown
	}
	tok, err := s.creds.Resolve(ctx, svc.Provider, run.TriggeredByUserID)
	if err != nil {
		return unknown
	}
	prov, err := s.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
	if err != nil {
		return unknown
	}
	pr, err := prov.PRStatus(ctx, owner, repo, run.PRNumber)
	if err != nil || pr == nil || pr.State == "" {
		return unknown
	}
	return pr.State
}

// reviewRunsFor returns the review runs targeting the run's PR: same service,
// kind=review, pr_head_branch == this run's pushed branch, newest first
// (ListRunsByService already orders newest-first). Returns a non-nil slice.
func (s *Server) reviewRunsFor(ctx context.Context, run *domain.Run, svc *domain.Service) []reviewRunView {
	out := []reviewRunView{}
	if run.GitBranch == "" {
		return out // an agent run with no pushed branch has no reviews to match
	}
	runs, err := s.st.ListRunsByService(ctx, svc.ID, 200)
	if err != nil {
		s.log.Warn("pr view: list review runs", "run", run.ID, "err", err)
		return out
	}
	for i := range runs {
		rr := runs[i]
		if rr.Kind != domain.RunKindReview || rr.PRHeadBranch != run.GitBranch {
			continue
		}
		out = append(out, reviewRunView{
			ID:                     rr.ID,
			Status:                 string(rr.Status),
			ReviewOutput:           rr.ReviewOutput,
			ReviewPostedAt:         rr.ReviewPostedAt,
			CreatedAt:              rr.CreatedAt,
			TriggeredByDisplayName: s.displayNameFor(ctx, rr.TriggeredByUserID),
		})
	}
	return out
}

// displayNameFor best-effort resolves a user's display name (empty for a
// service-principal / legacy run with no user, or on lookup failure).
func (s *Server) displayNameFor(ctx context.Context, userID *string) string {
	if userID == nil || *userID == "" {
		return ""
	}
	if u, err := s.st.GetUser(ctx, *userID); err == nil {
		return u.DisplayName
	}
	return ""
}
