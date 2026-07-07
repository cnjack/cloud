package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// Publisher is notified after a run's status changes so live SSE subscribers
// can be woken. It is optional (may be nil).
type Publisher interface {
	Publish(runID string, ev domain.RunEvent)
}

// Pusher pushes a runner-produced bundle's branch to a provider (the git-CLI
// seam; gitcli.Git implements it). Injected so the push pass is unit-tested with
// a fake and no real git/remote.
type Pusher interface {
	// PushBundleBranch pushes a NEW branch (draft-PR flow). Returns the tip SHA.
	PushBundleBranch(ctx context.Context, remoteURL, bundlePath, branch string) (string, error)
	// PushBundleBranchFFOnly fast-forward pushes a bundle's branch onto an EXISTING
	// branch (M7 webhook update mode). It NEVER force-pushes. It returns the tip
	// SHA and alreadyPresent=true when the remote branch already contains the
	// bundle tip (nothing to push — treat as done); a genuine non-fast-forward
	// divergence returns an error so the reconciler retries/skips rather than
	// clobbering the PR head.
	PushBundleBranchFFOnly(ctx context.Context, remoteURL, bundlePath, branch string) (sha string, alreadyPresent bool, err error)
}

// Reconciler is the control loop.
type Reconciler struct {
	st       store.Store
	launcher k8s.JobLauncher
	cfg      *config.Config
	log      *slog.Logger
	pub      Publisher
	now      func() time.Time // injectable clock for tests

	// M3 draft-PR / review stack. When any is nil the draft-PR and review passes
	// degrade to no-ops (diff-only), so a deployment without a provider configured
	// behaves exactly like J1-J3.
	factory provider.Factory      // builds PR clients per resolved token
	pusher  Pusher                // pushes the runner bundle's branch
	creds   *credentials.Resolver // resolves the acting token (user OAuth / gitea PAT)

	// models resolves (and caches) the effective LLM config at Job launch
	// (Feature A). New seeds a cipher-less default; main.go overrides it with the
	// API server's shared instance (WithModelResolver) so a console PUT/DELETE's
	// cache invalidation is immediately visible here. Never nil.
	models *modelcfg.Resolver
}

// New builds a Reconciler. pub may be nil. The draft-PR / review stack is set
// separately via WithPRStack so existing callers/tests are unaffected.
func New(st store.Store, launcher k8s.JobLauncher, cfg *config.Config, log *slog.Logger, pub Publisher) *Reconciler {
	return &Reconciler{
		st:       st,
		launcher: launcher,
		cfg:      cfg,
		log:      log,
		pub:      pub,
		now:      func() time.Time { return time.Now().UTC() },
		models:   modelcfg.NewResolver(st, nil, cfg),
	}
}

// WithPRStack wires the token-holding draft-PR / review machinery (M3): a
// provider Factory (builds PR clients per resolved token), a Pusher (git CLI),
// and a credentials Resolver. Any nil leaves the draft-PR / review passes as
// no-ops (diff-only). Returns r for chaining.
func (r *Reconciler) WithPRStack(factory provider.Factory, pusher Pusher, creds *credentials.Resolver) *Reconciler {
	r.factory = factory
	r.pusher = pusher
	r.creds = creds
	return r
}

// WithModelResolver replaces the default model-config resolver with a shared
// instance (the API server's, in main.go) so PUT/DELETE cache invalidation is
// visible to Job scheduling immediately (Feature A). Returns r for chaining.
func (r *Reconciler) WithModelResolver(m *modelcfg.Resolver) *Reconciler {
	if m != nil {
		r.models = m
	}
	return r
}

// Run drives the loop until ctx is cancelled, ticking every cfg.ReconcileInterval.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.ReconcileInterval)
	defer ticker.Stop()
	r.log.Info("reconciler started", "interval", r.cfg.ReconcileInterval)
	// Reconcile once immediately so the first queued run does not wait a tick.
	r.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopping")
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}

// Tick performs one reconcile pass over all non-terminal runs. Exported so tests
// (and the integration test) can drive a single deterministic pass.
func (r *Reconciler) Tick(ctx context.Context) {
	runs, err := r.st.ListRunsByStatus(ctx,
		domain.StatusQueued, domain.StatusScheduling, domain.StatusRunning)
	if err != nil {
		r.log.Error("reconcile: list runs", "err", err)
		return
	}

	// Compute remaining capacity once per tick. Active = scheduling+running.
	active, err := r.st.CountActiveRuns(ctx)
	if err != nil {
		r.log.Error("reconcile: count active", "err", err)
		return
	}
	capacity := r.cfg.MaxConcurrentRuns - active
	unlimited := r.cfg.MaxConcurrentRuns <= 0

	// Feature B — per-project concurrency guardrail. Seed each project's active
	// (scheduling+running) count from THIS tick's run set (its sum equals the
	// cluster `active` above, since ListRunsByStatus returned exactly those). A
	// run scheduled this tick then increments its project's count the same way it
	// decrements cluster capacity, so a single tick never over-schedules a project
	// past its max_concurrent_runs. pc memoizes GetProject to one hit per project
	// per tick; the loaded project is carried into createJob (no second lookup).
	projActive := map[string]int{}
	for i := range runs {
		switch runs[i].Status {
		case domain.StatusScheduling, domain.StatusRunning:
			projActive[runs[i].ProjectID]++
		}
	}
	pc := newProjectCache(r.st)

	for i := range runs {
		run := runs[i]
		var jobState k8s.JobState = k8s.JobUnknown
		if run.K8sJobName != "" {
			jobState, err = r.launcher.GetJobState(ctx, run.K8sJobName)
			if err != nil {
				r.log.Warn("reconcile: get job state", "run", run.ID, "job", run.K8sJobName, "err", err)
				continue // transient; retry next tick
			}
		}

		hasCapacity := unlimited || capacity > 0
		// Load the run's project ONCE here for a queued run we might schedule, and
		// carry it into createJob (guardrails: concurrency, timeout, injected env).
		// A load failure means we do NOT schedule blind — the run stays queued and
		// retries next tick (consistent, fail-visible; never a silent default).
		// proj stays nil for scheduling/running runs (createJob is not reached).
		var proj *domain.Project
		if run.Status == domain.StatusQueued && hasCapacity {
			p, perr := pc.get(ctx, run.ProjectID)
			if perr != nil {
				r.log.Warn("reconcile: load project for guardrails — leaving run queued", "run", run.ID, "project", run.ProjectID, "err", perr)
				continue // transient; retry next tick (do not schedule blind)
			}
			proj = p
			if lim := proj.MaxConcurrentRuns; lim != nil && *lim > 0 && projActive[run.ProjectID] >= *lim {
				r.log.Info("reconcile: project at concurrency limit — leaving run queued",
					"run", run.ID, "project", run.ProjectID, "limit", *lim, "active", projActive[run.ProjectID])
				continue
			}
		}
		d := decide(run, jobState, hasCapacity)
		if d.Action == ActionNone {
			continue
		}
		if r.apply(ctx, &run, d, proj) && d.Action == ActionCreateJob {
			capacity--                  // consumed a cluster slot this tick
			projActive[run.ProjectID]++ // …and a per-project slot
		}
	}

	// Reap Jobs left attached to terminal runs (canceled-racing-schedule, or a
	// cancel whose best-effort delete failed). See cleanupTerminalJobs.
	r.cleanupTerminalJobs(ctx)

	// Push branches + open draft PRs for succeeded draft_pr agent runs that
	// uploaded a bundle but have no PR yet (M3). Idempotent: a run stays in the
	// scan until pr_url is stamped.
	r.reconcilePRs(ctx)

	// Fast-forward push webhook @mention task bundles back onto their existing PR
	// head branch (M7 update mode). Idempotent via commit_sha.
	r.reconcileUpdatePushes(ctx)

	// Post AI-review comments for succeeded review runs whose output has not been
	// posted to their target PR yet (M3/M5). Idempotent via review_posted_at.
	r.reconcileReviews(ctx)
}

// reconcilePRs pushes the branch and opens a draft PR for each succeeded
// draft_pr agent run that uploaded a bundle but has no PR yet (M3). It is a
// no-op when the draft-PR stack is not configured. Each run is idempotent: an
// existing open PR for the head branch wins (covering a crash between
// push/create and persist), the push is up-to-date-tolerant, and MarkPRCreated
// is first-writer-wins so two ticks cannot double-record.
func (r *Reconciler) reconcilePRs(ctx context.Context) {
	if r.factory == nil || r.pusher == nil || r.creds == nil {
		return // draft-PR flow disabled — diff-only.
	}
	runs, err := r.st.ListRunsAwaitingPR(ctx)
	if err != nil {
		r.log.Error("reconcile: list runs awaiting pr", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile pr: get service", "run", run.ID, "err", err)
			continue
		}
		if !shouldOpenPR(run, *svc, true) {
			continue
		}
		r.openDraftPR(ctx, &run, svc)
	}
}

// openDraftPR resolves the acting token, pushes the run's bundle branch, then
// looks up (idempotency) or creates the draft PR on the triggering user's
// behalf, persists pr_url/pr_number, and emits a run.status refresh. Any step
// failing leaves the run succeeded with pr_url empty so the next tick retries.
// NEVER merges, NEVER triggers CI (hard gate).
func (r *Reconciler) openDraftPR(ctx context.Context, run *domain.Run, svc *domain.Service) {
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile pr: bad repo_owner_name", "run", run.ID, "repo", svc.RepoOwnerName)
		return
	}
	branch := run.GitBranch // recorded when the bundle was received (jcode/run-<id>)

	// Resolve the credential to act with (user OAuth, else gitea PAT). The token
	// value is never logged — only its source label.
	tok, err := r.creds.Resolve(ctx, svc.Provider, run.TriggeredByUserID)
	if err != nil {
		r.log.Warn("reconcile pr: no credential; leaving diff-only", "run", run.ID, "provider", svc.Provider, "err", err)
		return // retry next tick (a user may bind the provider later)
	}
	prov, err := r.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
	if err != nil {
		r.log.Warn("reconcile pr: build client", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}

	// Idempotency: an existing open PR for this head branch wins (covers a crash
	// after push/create but before persist, and a manually opened PR).
	pr, err := prov.FindOpenPRByHead(ctx, owner, repo, branch)
	if err != nil {
		r.log.Warn("reconcile pr: find existing", "run", run.ID, "err", err)
		return
	}
	if pr == nil {
		// Push the branch from the stored bundle, then open the PR.
		sha, perr := r.pushRunBundle(ctx, run, svc, tok, branch)
		if perr != nil {
			r.log.Warn("reconcile pr: push branch", "run", run.ID, "src", tok.Source, "err", perr)
			return // transient; retry next tick
		}
		if sha != "" {
			if _, err := r.st.SetRunGit(ctx, run.ID, branch, sha); err != nil {
				r.log.Warn("reconcile pr: record commit sha", "run", run.ID, "err", err)
			}
			run.CommitSHA = sha
		}
		pr, err = prov.CreateDraftPR(ctx, provider.CreateDraftPRInput{
			Owner: owner, Repo: repo, Head: branch, Base: svc.DefaultBranch,
			Title: prTitle(run.Prompt), Body: prBody(run),
		})
		if err != nil {
			// The branch is pushed; a create failure may be a race with another
			// tick that already opened it — re-find before giving up.
			if found, ferr := prov.FindOpenPRByHead(ctx, owner, repo, branch); ferr == nil && found != nil {
				pr = found
			} else {
				r.log.Warn("reconcile pr: create draft", "run", run.ID, "err", err)
				return
			}
		} else {
			r.log.Info("reconcile pr: opened draft PR", "run", run.ID, "pr", pr.Number, "url", pr.URL, "src", tok.Source)
		}
	}

	committed, err := r.st.MarkPRCreated(ctx, run.ID, pr.URL, pr.Number)
	if err != nil {
		r.log.Error("reconcile pr: mark pr created", "run", run.ID, "err", err)
		return
	}
	// Re-emit run.status so the SSE stream carries pr_url to a live console.
	r.emitStatus(ctx, committed)
}

// pushRunBundle writes the run's stored bundle to a temp file and pushes its
// branch to the provider using an authenticated clone/push URL. The temp file
// (and the URL's credential) never persist. Returns the pushed branch tip SHA.
func (r *Reconciler) pushRunBundle(ctx context.Context, run *domain.Run, svc *domain.Service, tok credentials.Token, branch string) (string, error) {
	bundle, err := r.st.GetRunBundle(ctx, run.ID)
	if err != nil {
		return "", fmt.Errorf("load bundle: %w", err)
	}
	f, err := os.CreateTemp("", "jcloud-bundle-*.bundle")
	if err != nil {
		return "", fmt.Errorf("temp bundle: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		return "", fmt.Errorf("write bundle: %w", err)
	}
	f.Close()

	rawURL := domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
	if rawURL == "" {
		return "", fmt.Errorf("could not derive push URL for service %s", svc.ID)
	}
	authed := tok.AuthedURL(rawURL, svc.Provider)
	return r.pusher.PushBundleBranch(ctx, authed, f.Name(), branch)
}

// reconcileUpdatePushes fast-forward pushes each succeeded webhook @mention task
// bundle back onto its existing PR head branch (M7 update mode). No-op when the
// draft-PR stack is not configured. Idempotent: a run stays in the scan until its
// commit_sha is stamped (on a successful push, or when the remote already carries
// the change). It NEVER opens a new PR — the existing PR auto-updates.
func (r *Reconciler) reconcileUpdatePushes(ctx context.Context) {
	if r.factory == nil || r.pusher == nil || r.creds == nil {
		return
	}
	runs, err := r.st.ListRunsAwaitingUpdatePush(ctx)
	if err != nil {
		r.log.Error("reconcile: list runs awaiting update push", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile update: get service", "run", run.ID, "err", err)
			continue
		}
		if !shouldUpdatePush(run, *svc, true) {
			continue
		}
		r.updatePushRun(ctx, &run, svc)
	}
}

// updatePushRun ff-only pushes the run's bundle branch onto the existing PR head
// and stamps commit_sha (which removes it from the scan). A non-fast-forward
// divergence (someone pushed to the PR head after the run cloned) is logged Warn
// and left for a later tick / manual resolution — it never force-pushes and never
// spins (it stays in the scan but each tick just re-Warns until it ff-applies or
// the remote already contains the change). NEVER opens a PR.
func (r *Reconciler) updatePushRun(ctx context.Context, run *domain.Run, svc *domain.Service) {
	branch := run.GitBranch // recorded when the bundle was received (= PR head branch)
	tok, err := r.creds.Resolve(ctx, svc.Provider, run.TriggeredByUserID)
	if err != nil {
		r.log.Warn("reconcile update: no credential; leaving for retry", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}

	bundle, err := r.st.GetRunBundle(ctx, run.ID)
	if err != nil {
		r.log.Warn("reconcile update: load bundle", "run", run.ID, "err", err)
		return
	}
	f, err := os.CreateTemp("", "jcloud-update-*.bundle")
	if err != nil {
		r.log.Warn("reconcile update: temp bundle", "run", run.ID, "err", err)
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		r.log.Warn("reconcile update: write bundle", "run", run.ID, "err", err)
		return
	}
	f.Close()

	rawURL := domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
	if rawURL == "" {
		r.log.Warn("reconcile update: could not derive push URL", "run", run.ID)
		return
	}
	authed := tok.AuthedURL(rawURL, svc.Provider)

	sha, alreadyPresent, err := r.pusher.PushBundleBranchFFOnly(ctx, authed, f.Name(), branch)
	if err != nil {
		// Non-fast-forward (or transient): leave in the scan and retry next tick.
		// This does NOT spin CPU — the reconciler ticks on its interval.
		r.log.Warn("reconcile update: ff-only push failed (retry next tick)", "run", run.ID, "branch", branch, "src", tok.Source, "err", err)
		return
	}
	if alreadyPresent {
		r.log.Info("reconcile update: PR head already contains the change", "run", run.ID, "branch", branch)
	} else {
		r.log.Info("reconcile update: pushed onto PR head branch", "run", run.ID, "branch", branch, "src", tok.Source)
	}
	// Stamp commit_sha so the run drops out of the update scan (idempotency).
	if _, err := r.st.SetRunGit(ctx, run.ID, branch, sha); err != nil {
		r.log.Warn("reconcile update: record commit sha", "run", run.ID, "err", err)
		return
	}
	run.CommitSHA = sha
	r.emitStatus(ctx, run)
}

// reconcileReviews posts the AI-review comment for each succeeded review run
// whose output has not been posted to its target PR yet (M3/M5). No-op when the
// review stack is not configured. Idempotent: after a successful post the run is
// stamped review_posted_at and drops out of the scan.
func (r *Reconciler) reconcileReviews(ctx context.Context) {
	if r.factory == nil || r.creds == nil {
		return
	}
	runs, err := r.st.ListReviewRunsAwaitingPost(ctx)
	if err != nil {
		r.log.Error("reconcile: list review runs", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile review: get service", "run", run.ID, "err", err)
			continue
		}
		r.postReview(ctx, &run, svc)
	}
}

// postReview finds the run's target PR (by its head branch) and posts the review
// output as a comment, then stamps review_posted_at. A failure leaves the run in
// the scan for the next tick.
func (r *Reconciler) postReview(ctx context.Context, run *domain.Run, svc *domain.Service) {
	if svc.RepoKind != domain.RepoKindProvider || strings.TrimSpace(run.PRHeadBranch) == "" {
		return // not associated with a provider PR (misconfigured review run)
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile review: bad repo_owner_name", "run", run.ID)
		return
	}
	tok, err := r.creds.Resolve(ctx, svc.Provider, run.TriggeredByUserID)
	if err != nil {
		r.log.Warn("reconcile review: no credential", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}
	prov, err := r.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
	if err != nil {
		r.log.Warn("reconcile review: build client", "run", run.ID, "err", err)
		return
	}
	pr, err := prov.FindOpenPRByHead(ctx, owner, repo, run.PRHeadBranch)
	if err != nil {
		r.log.Warn("reconcile review: find target PR", "run", run.ID, "err", err)
		return
	}
	if pr == nil {
		r.log.Warn("reconcile review: target PR not found (closed?)", "run", run.ID, "head", run.PRHeadBranch)
		return // retry next tick; PR may reopen or this stays unposted
	}
	if err := prov.CreatePRReview(ctx, owner, repo, pr.Number, run.ReviewOutput); err != nil {
		r.log.Warn("reconcile review: post comment", "run", run.ID, "pr", pr.Number, "err", err)
		return
	}
	if _, err := r.st.MarkReviewPosted(ctx, run.ID); err != nil {
		r.log.Warn("reconcile review: mark posted", "run", run.ID, "err", err)
		return
	}
	r.log.Info("reconcile review: posted review comment", "run", run.ID, "pr", pr.Number, "src", tok.Source)
}

// prBody is the PR description linking the run for traceability.
func prBody(run *domain.Run) string {
	var b strings.Builder
	b.WriteString("Draft PR opened by jcode Cloud Agent for run `")
	b.WriteString(run.ID)
	b.WriteString("`.\n\n")
	b.WriteString("**Task**\n\n")
	b.WriteString(strings.TrimSpace(run.Prompt))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Branch `%s` @ `%s`.\n\n", run.GitBranch, run.CommitSHA)
	b.WriteString("_Not auto-merged and CI is not auto-triggered — review and iterate._\n")
	return b.String()
}

// projectCache memoizes GetProject for the span of ONE tick so the per-project
// guardrail lookups (concurrency limit in Tick, timeout + injected_env in
// createJob/jobEnv) cost a single store hit per project regardless of how many of
// its runs are in the pass. It is not shared across ticks — a fresh one is built
// each Tick so a mid-flight PATCH is picked up on the next pass.
type projectCache struct {
	st    store.Store
	cache map[string]*domain.Project
}

func newProjectCache(st store.Store) *projectCache {
	return &projectCache{st: st, cache: map[string]*domain.Project{}}
}

// get returns the project (memoized). Errors are NOT cached so a transient DB
// blip is retried on the next call/tick.
func (pc *projectCache) get(ctx context.Context, id string) (*domain.Project, error) {
	if p, ok := pc.cache[id]; ok {
		return p, nil
	}
	p, err := pc.st.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	pc.cache[id] = p
	return p, nil
}

// apply performs the side effects for one decision. Returns true if it made a
// scheduling change worth decrementing capacity for. Every persistence step goes
// through a targeted store mutator that re-reads the committed row and writes
// only its own fields, so a concurrent cancel/ingest can never be clobbered. proj
// is the run's project (loaded once in Tick), non-nil for the ActionCreateJob
// path so createJob applies the guardrails without a second store hit.
func (r *Reconciler) apply(ctx context.Context, run *domain.Run, d Decision, proj *domain.Project) bool {
	switch d.Action {
	case ActionCreateJob:
		return r.createJob(ctx, run, proj)

	case ActionMarkRunning:
		if committed, err := r.st.MarkRunning(ctx, run.ID, "StreamingTurn", r.now()); err != nil {
			r.log.Error("reconcile: mark running", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionMarkSucceeded:
		if committed, err := r.st.MarkSucceeded(ctx, run.ID, "Succeeded", r.now()); err != nil {
			r.log.Error("reconcile: mark succeeded", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionMarkFailed:
		// MarkFailed preserves any runner-reported failure_reason/message set via
		// a concurrent ingest; d.* fill only where the stored fields are empty.
		committed, err := r.st.MarkFailed(ctx, run.ID, "Failed", d.FailureReason, d.FailureMsg, r.now())
		if err != nil {
			r.log.Error("reconcile: mark failed", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionDeleteJob:
		if run.K8sJobName != "" {
			if err := r.launcher.DeleteJob(ctx, run.K8sJobName); err != nil {
				r.log.Error("reconcile: delete job", "run", run.ID, "err", err)
			}
		}
	}
	return false
}

// createJob mints a per-run token, creates the runner Job, and persists the job
// name + token hash via ScheduleRun.
//
// Token/idempotency correctness: this path runs only for a QUEUED run, which
// should have no live Job. But if a PRIOR tick created a Job with token1 and
// then failed to persist (transient DB error / crash before commit), the run is
// still queued with NO hash while a token1 Job lingers. Because CreateJob is
// idempotent-by-name and an existing Job keeps its ORIGINAL env, a naive retry
// would generate token2, no-op against the token1 Job, and persist hash(token2)
// — so the runner (token1) would 401 forever. To make the persisted hash always
// authoritative, we DELETE any same-named Job first so CreateJob always produces
// a fresh Job carrying the token we are about to persist. Deleting a
// non-existent Job is a no-op, so this is safe on the normal first-create path.
// See finding "token regen + idempotent CreateJob mismatch".
func (r *Reconciler) createJob(ctx context.Context, run *domain.Run, proj *domain.Project) bool {
	// Guardrails are a hard input: Tick loads the run's project before scheduling
	// and passes it here. A nil project would mean scheduling blind (no timeout /
	// injected-env / concurrency guardrail) — a silent downgrade (CLAUDE.md red
	// line #1), so refuse and retry next tick instead. This is unreachable on the
	// normal path (ActionCreateJob ⟹ Tick loaded the project) but guards a future
	// caller.
	if proj == nil {
		r.log.Error("reconcile: createJob without a loaded project — refusing to schedule blind", "run", run.ID)
		return false
	}
	// Fail-visible gate (CLAUDE.md red line #1): resolve the EFFECTIVE model
	// config at Job-launch time. The API gate blocks creation, but a run can be
	// queued while configured and then have its config cleared before it is
	// scheduled — in that window we must NOT silently launch a runner with no
	// model. A definitively-unconfigured model fails the run visibly; a resolve
	// ERROR is transient (DB blip, key rotation mid-flight) and is retried next
	// tick, consistent with the other transient-error paths in Tick.
	model, err := r.models.Resolve(ctx)
	if err != nil {
		r.log.Error("reconcile: resolve model config", "run", run.ID, "err", err)
		return false // transient; retry next tick
	}
	if !model.Configured() {
		msg := modelcfg.NotConfiguredMessage("")
		if committed, merr := r.st.MarkFailed(ctx, run.ID, "Failed", domain.FailureSetupFailed, msg, r.now()); merr != nil {
			r.log.Error("reconcile: mark failed (model not configured)", "run", run.ID, "err", merr)
		} else {
			r.log.Warn("reconcile: run failed — model not configured", "run", run.ID)
			r.emitStatus(ctx, committed)
		}
		return false
	}

	token, err := auth.GenerateRunToken()
	if err != nil {
		r.log.Error("reconcile: gen run token", "run", run.ID, "err", err)
		return false
	}
	jobName := k8s.JobName(run.ID)

	// Clear any leftover same-named Job from a prior failed-persist tick so the
	// Job we create carries the token whose hash we persist below.
	if err := r.launcher.DeleteJob(ctx, jobName); err != nil {
		r.log.Warn("reconcile: delete leftover job before create", "run", run.ID, "err", err)
	}

	// Feature B — project guardrails (timeout + injected env). run_timeout_secs
	// overrides the cluster default only when set and positive (NULL/≤0 means
	// "inherit"). `timeout` is the runner's INTERNAL budget (RUN_TIMEOUT, which
	// bounds `jcode acp`).
	timeout := r.cfg.RunTimeoutSecs
	if proj.RunTimeoutSecs != nil && *proj.RunTimeoutSecs > 0 {
		timeout = *proj.RunTimeoutSecs
	}
	// The Job's activeDeadlineSeconds is a HARD backstop and counts from pod start
	// (including clone/setup), whereas RUN_TIMEOUT only bounds the agent turn. If we
	// set them equal, k8s would SIGKILL the pod at the same instant the runner's own
	// graceful timeout fires — pre-empting the "timeout" failure classification and
	// discarding the partial diff.patch / REVIEW.md. So give the Job deadline a
	// grace margin on top of RUN_TIMEOUT (clone/setup + graceful-exit headroom).
	jobDeadline := timeout
	if timeout > 0 {
		jobDeadline = timeout + timeoutGrace(timeout)
	}

	spec := k8s.JobSpec{
		Name:           jobName,
		RunID:          run.ID,
		Env:            r.jobEnv(ctx, run, token, model, proj, timeout),
		TimeoutSeconds: jobDeadline,
	}
	if err := r.launcher.CreateJob(ctx, spec); err != nil {
		r.log.Error("reconcile: create job", "run", run.ID, "err", err)
		return false
	}
	committed, err := r.st.ScheduleRun(ctx, run.ID, jobName, auth.HashToken(token), "PreparingWorkspace")
	if err != nil {
		// A concurrent cancel may have committed queued->canceled first; the Job
		// we just created is now orphaned. Delete it so it does not run to
		// completion unreferenced. The terminal-with-job cleanup path also covers
		// this, but deleting eagerly is cheap and immediate.
		r.log.Error("reconcile: schedule run", "run", run.ID, "err", err)
		if delErr := r.launcher.DeleteJob(ctx, jobName); delErr != nil {
			r.log.Warn("reconcile: delete orphaned job after schedule failure", "run", run.ID, "err", delErr)
		}
		return false
	}
	r.emitStatus(ctx, committed)
	return true
}

// cleanupTerminalJobs deletes Jobs still attached to terminal runs (e.g. a
// cancel that raced Job creation, or a cancel whose best-effort DeleteJob failed
// transiently) and stamps job_cleaned_at once the Job is confirmed gone.
// k8s_job_name is preserved as the run's historical record. This is the only
// path that reaps orphaned Jobs, since decide() never returns ActionDeleteJob
// for terminal runs.
func (r *Reconciler) cleanupTerminalJobs(ctx context.Context) {
	runs, err := r.st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		r.log.Error("reconcile: list terminal runs with job", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		if err := r.launcher.DeleteJob(ctx, run.K8sJobName); err != nil {
			r.log.Warn("reconcile: cleanup delete job", "run", run.ID, "job", run.K8sJobName, "err", err)
			continue // retry next tick; job_cleaned_at stays null so we try again
		}
		state, err := r.launcher.GetJobState(ctx, run.K8sJobName)
		if err != nil {
			r.log.Warn("reconcile: cleanup get job state", "run", run.ID, "err", err)
			continue
		}
		if state == k8s.JobMissing {
			// Stamp the cleanup marker; k8s_job_name is KEPT as historical record
			// (audit + e2e verification) — the marker alone removes the run from
			// the next tick's cleanup scan.
			if err := r.st.MarkJobCleaned(ctx, run.ID); err != nil {
				r.log.Warn("reconcile: cleanup mark job cleaned", "run", run.ID, "err", err)
			}
		}
	}
}

// emitStatus appends a run.status event (and run.failure when failed).
func (r *Reconciler) emitStatus(ctx context.Context, run *domain.Run) {
	payload := map[string]any{
		"status": string(run.Status),
		"phase":  run.Phase,
	}
	if run.Status == domain.StatusFailed {
		payload["failure_reason"] = string(run.FailureReason)
		payload["failure_message"] = run.FailureMessage
	}
	// Carry the draft-PR link so a live console updates its header without an
	// extra GET (ST-1). Only present once the PR has been opened.
	if run.PRURL != "" {
		payload["pr_url"] = run.PRURL
		payload["pr_number"] = run.PRNumber
	}
	r.emit(ctx, run.ID, domain.EventRunStatus, payload)
	if run.Status == domain.StatusFailed {
		r.emit(ctx, run.ID, domain.EventRunFailure, map[string]any{
			"reason":  string(run.FailureReason),
			"message": run.FailureMessage,
		})
	}
}

// emit appends one internally-generated event, letting the store allocate the
// next global seq atomically (no NextEventSeq race with runner ingest), and
// notifies the publisher.
func (r *Reconciler) emit(ctx context.Context, runID, typ string, payload map[string]any) {
	ev, err := r.st.AppendInternalEvent(ctx, runID, typ, payload)
	if err != nil {
		r.log.Error("reconcile: emit event", "run", runID, "type", typ, "err", err)
		return
	}
	if r.pub != nil {
		r.pub.Publish(runID, ev)
	}
}

// jobEnv assembles the runner container environment per the M3 runner contract
// (blueprint §3). token is the freshly-minted plaintext RUN_TOKEN; model is the
// EFFECTIVE model config resolved at Job-launch (Feature A) — the runner is
// pointed at exactly what the admin set (DB) or the env fallback, never a mock
// default. NO provider token is ever injected: the runner reads (via a source
// bundle it fetches over the RUN_TOKEN) and writes a bundle it uploads; the
// orchestrator pushes.
func (r *Reconciler) jobEnv(ctx context.Context, run *domain.Run, token string, model modelcfg.Resolved, proj *domain.Project, timeoutSecs int64) map[string]string {
	kind := run.Kind
	if kind == "" {
		kind = domain.RunKindAgent
	}
	env := map[string]string{
		"RUN_ID":         run.ID,
		"TASK_PROMPT":    run.Prompt,
		"ORCH_BASE_URL":  r.cfg.OrchBaseURL,
		"MODEL_BASE_URL": model.BaseURL,
		"MODEL_API_KEY":  model.APIKey,
		"MODEL_NAME":     model.ModelName,
		"RUN_TOKEN":      token,
		"RUN_KIND":       string(kind),
	}
	// Feature B — set the runner's internal RUN_TIMEOUT (bounds `jcode acp`) to the
	// effective per-run budget. The Job's activeDeadlineSeconds is set SEPARATELY to
	// this value PLUS a grace margin (see createJob/timeoutGrace) so the runner's
	// own graceful timeout fires first and the "timeout" failure + partial artifact
	// survive. entrypoint.sh otherwise defaults RUN_TIMEOUT to 300s.
	if timeoutSecs > 0 {
		env["RUN_TIMEOUT"] = fmt.Sprintf("%ds", timeoutSecs)
	}
	svc, err := r.st.GetService(ctx, run.ServiceID)
	if err != nil {
		r.log.Error("reconcile: get service for env", "run", run.ID, "err", err)
		return env
	}
	r.addGitEnv(env, run, svc)
	// Feature B — project-scoped injected env, applied LAST (on top of the system
	// contract). Reserved keys are refused at the PATCH API; jobEnv drops (and
	// Warns on) any that slipped through a stale/legacy row so a system variable
	// can never be overridden (double insurance; CLAUDE.md fail-visible).
	r.applyInjectedEnv(env, run, proj)
	return env
}

// timeoutGrace is the headroom added to the runner's RUN_TIMEOUT to form the
// Job's hard activeDeadlineSeconds. The Job clock includes clone/setup (which
// RUN_TIMEOUT does not) plus the runner's own graceful-exit window, so the
// backstop must sit strictly later than the internal timeout. max(120s,
// timeout/10) scales with long runs while keeping a floor for short ones.
func timeoutGrace(timeoutSecs int64) int64 {
	grace := timeoutSecs / 10
	if grace < 120 {
		grace = 120
	}
	return grace
}

// applyInjectedEnv merges a project's injected_env into env, skipping any
// reserved system key (defensive — the API rejects these) and any key that would
// collide with a system variable already set (belt-and-braces: all system keys
// are reserved, so this is unreachable in practice). Nil proj / empty map is a
// no-op.
func (r *Reconciler) applyInjectedEnv(env map[string]string, run *domain.Run, proj *domain.Project) {
	if proj == nil {
		return
	}
	for k, v := range proj.InjectedEnv {
		if domain.IsReservedEnvKey(k) {
			r.log.Warn("reconcile: dropping reserved injected_env key", "run", run.ID, "project", proj.ID, "key", k)
			continue
		}
		if _, exists := env[k]; exists {
			r.log.Warn("reconcile: injected_env key collides with a system variable — skipping", "run", run.ID, "key", k)
			continue
		}
		env[k] = v
	}
}

// addGitEnv injects the M3 clone/produce contract (blueprint §3). It sets:
//
//   - BASE_BRANCH: the service default branch (checkout target / bundle base).
//   - SOURCE_MODE: "fetch" for every PROVIDER service (a UNIFIED path — the
//     orchestrator pre-clones and serves a source bundle over the RUN_TOKEN, so
//     the runner never needs a git credential and public/private is not guessed);
//     "clone" for RAW services (git://, file://, opaque http — the runner clones
//     the raw URL directly, as J1-J3). REPO_URL is set only for clone mode.
//   - GIT_MODE: "draft_pr" for a draft_pr provider service (the runner will
//     produce a bundle after a good diff), else "readonly". BRANCH_NAME is the
//     deterministic push branch for draft_pr.
//   - PR_HEAD / PR_BASE: for a review run, the branches the runner diffs.
//
// NO provider token is injected — the runner is credential-free (blueprint §0).
func (r *Reconciler) addGitEnv(env map[string]string, run *domain.Run, svc *domain.Service) {
	env["BASE_BRANCH"] = svc.DefaultBranch
	// M7 webhook @mention task: the baseline IS the PR head branch, so the agent
	// builds on the existing PR and the produced branch pushes back to it (§8).
	if run.Kind == domain.RunKindAgent && run.PRHeadBranch != "" {
		env["BASE_BRANCH"] = run.PRHeadBranch
	}

	if svc.RepoKind == domain.RepoKindProvider {
		// Unified path: fetch a source bundle from the orchestrator (no token in pod).
		env["SOURCE_MODE"] = "fetch"
	} else {
		// Raw repo: clone the opaque URL directly (anonymous / native protocol).
		env["SOURCE_MODE"] = "clone"
		env["REPO_URL"] = domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
		env["REPO_BRANCH"] = env["BASE_BRANCH"]
	}

	// Produce a bundle only for a draft_pr PROVIDER service (raw can only be
	// readonly by construction). readonly stays diff-only. For a webhook task the
	// push branch is the PR head (BRANCH_NAME == BASE_BRANCH → the entrypoint
	// bundles startSHA..HEAD onto that same branch); otherwise it is jcode/run-<id>.
	env["GIT_MODE"] = string(domain.GitModeReadonly)
	if svc.GitMode == domain.GitModeDraftPR && svc.RepoKind == domain.RepoKindProvider {
		env["GIT_MODE"] = string(domain.GitModeDraftPR)
		env["BRANCH_NAME"] = domain.RunPushBranch(run)
	}

	// Review runs diff PR_BASE...PR_HEAD (blueprint §3). Empty for agent runs.
	if run.Kind == domain.RunKindReview {
		env["PR_HEAD"] = run.PRHeadBranch
		env["PR_BASE"] = run.PRBaseBranch
	}
}
