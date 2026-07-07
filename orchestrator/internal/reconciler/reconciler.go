package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// Publisher is notified after a run's status changes so live SSE subscribers
// can be woken. It is optional (may be nil).
type Publisher interface {
	Publish(runID string, ev domain.RunEvent)
}

// Reconciler is the control loop.
type Reconciler struct {
	st       store.Store
	launcher k8s.JobLauncher
	prov     provider.Provider // git provider for draft PRs (ST-1); nil => diff-only
	cfg      *config.Config
	log      *slog.Logger
	pub      Publisher
	now      func() time.Time // injectable clock for tests
}

// New builds a Reconciler. pub may be nil. The provider is set separately via
// WithProvider so existing callers/tests are unaffected.
func New(st store.Store, launcher k8s.JobLauncher, cfg *config.Config, log *slog.Logger, pub Publisher) *Reconciler {
	return &Reconciler{
		st:       st,
		launcher: launcher,
		cfg:      cfg,
		log:      log,
		pub:      pub,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// WithProvider sets the git provider used to open draft PRs (ST-1). Nil (the
// default) means the draft-PR flow degrades to diff-only. Returns r for
// chaining.
func (r *Reconciler) WithProvider(p provider.Provider) *Reconciler {
	r.prov = p
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
		d := decide(run, jobState, hasCapacity)
		if d.Action == ActionNone {
			continue
		}
		if r.apply(ctx, &run, d) && d.Action == ActionCreateJob {
			capacity-- // consumed a slot this tick
		}
	}

	// Reap Jobs left attached to terminal runs (canceled-racing-schedule, or a
	// cancel whose best-effort delete failed). See cleanupTerminalJobs.
	r.cleanupTerminalJobs(ctx)

	// Open draft PRs for succeeded draft_pr-mode runs that pushed a branch but
	// have no PR yet (ST-1). Runs in its own pass off ListRunsAwaitingPR so it is
	// crash-safe and idempotent: a run stays in the scan until pr_url is stamped.
	r.reconcilePRs(ctx)
}

// reconcilePRs opens a draft PR for each succeeded draft_pr-mode run that has a
// pushed branch but no PR. It is a no-op when no provider is configured. Each
// PR-create is idempotent: it first looks up an existing PR by head branch
// (covering a crash after create-before-persist), and MarkPRCreated is
// first-writer-wins so two ticks cannot double-record.
func (r *Reconciler) reconcilePRs(ctx context.Context) {
	if r.prov == nil {
		return // draft-PR flow disabled (no provider configured) — diff-only.
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

// openDraftPR looks up (idempotency) or creates the draft PR, then persists
// pr_url/pr_number and emits a run.status refresh so the console's live stream
// picks up the link. NEVER merges, NEVER triggers CI (hard gate, D08).
func (r *Reconciler) openDraftPR(ctx context.Context, run *domain.Run, svc *domain.Service) {
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile pr: bad repo_owner_name", "run", run.ID, "repo", svc.RepoOwnerName)
		return
	}

	// Idempotency: an existing open PR for this head branch wins (covers a crash
	// between create and persist, and a human who opened it manually).
	pr, err := r.prov.FindOpenPRByHead(ctx, owner, repo, run.GitBranch)
	if err != nil {
		r.log.Warn("reconcile pr: find existing", "run", run.ID, "err", err)
		return // transient; retry next tick (run stays in the scan)
	}
	if pr == nil {
		pr, err = r.prov.CreateDraftPR(ctx, provider.CreateDraftPRInput{
			Owner: owner,
			Repo:  repo,
			Head:  run.GitBranch,
			Base:  svc.DefaultBranch,
			Title: prTitle(run.Prompt),
			Body:  prBody(run),
		})
		if err != nil {
			r.log.Warn("reconcile pr: create draft", "run", run.ID, "err", err)
			return // transient; retry next tick
		}
		r.log.Info("reconcile pr: opened draft PR", "run", run.ID, "pr", pr.Number, "url", pr.URL)
	}

	committed, err := r.st.MarkPRCreated(ctx, run.ID, pr.URL, pr.Number)
	if err != nil {
		r.log.Error("reconcile pr: mark pr created", "run", run.ID, "err", err)
		return
	}
	// Re-emit run.status so the SSE stream carries pr_url to a live console.
	r.emitStatus(ctx, committed)
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

// apply performs the side effects for one decision. Returns true if it made a
// scheduling change worth decrementing capacity for. Every persistence step goes
// through a targeted store mutator that re-reads the committed row and writes
// only its own fields, so a concurrent cancel/ingest can never be clobbered.
func (r *Reconciler) apply(ctx context.Context, run *domain.Run, d Decision) bool {
	switch d.Action {
	case ActionCreateJob:
		return r.createJob(ctx, run)

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
func (r *Reconciler) createJob(ctx context.Context, run *domain.Run) bool {
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

	spec := k8s.JobSpec{
		Name:           jobName,
		RunID:          run.ID,
		Env:            r.jobEnv(ctx, run, token),
		TimeoutSeconds: r.cfg.RunTimeoutSecs,
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

// jobEnv assembles the runner container environment per the entrypoint contract
// (cloud/docs/11-api.md "Runner Job environment"). token is the freshly-minted
// plaintext RUN_TOKEN, injected here and never persisted in plaintext.
func (r *Reconciler) jobEnv(ctx context.Context, run *domain.Run, token string) map[string]string {
	env := map[string]string{
		"RUN_ID":         run.ID,
		"TASK_PROMPT":    run.Prompt,
		"ORCH_BASE_URL":  r.cfg.OrchBaseURL,
		"MODEL_BASE_URL": r.cfg.ModelBaseURL,
		"MODEL_API_KEY":  r.cfg.ModelAPIKey,
		"MODEL_NAME":     r.cfg.ModelName,
		"RUN_TOKEN":      token,
	}
	// REPO_URL / REPO_BRANCH come from the run's service (blueprint §1).
	if svc, err := r.st.GetService(ctx, run.ServiceID); err == nil {
		env["REPO_URL"] = r.serviceRepoURL(svc)
		env["REPO_BRANCH"] = svc.DefaultBranch
		r.addGitEnv(env, run, svc)
	} else {
		r.log.Error("reconcile: get service for env", "run", run.ID, "err", err)
	}
	return env
}

// serviceRepoURL is the clone/push origin for a service (see
// domain.ServiceCloneURL). Gitea's base is cfg.GiteaURL.
func (r *Reconciler) serviceRepoURL(svc *domain.Service) string {
	return domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
}

// addGitEnv injects the git-integration contract into the runner env. It sets
// GIT_MODE and, when a provider token applies, GIT_TOKEN. Two independent
// concerns are handled here:
//
//   - Clone auth (F1): a PRIVATE repo can only be READ if the runner clones with
//     credentials, and the clone happens BEFORE any push logic — so GIT_TOKEN is
//     injected for BOTH readonly AND draft_pr whenever the token applies.
//     GIT_MODE semantics are unchanged: readonly never pushes/opens a PR.
//   - Push contract (ST-1): draft_pr additionally gets GIT_BRANCH/GIT_PUSH_URL/
//     GIT_BASE_BRANCH so the runner can push agent/run-<id> after a good diff.
//
// TOKEN SAFETY (security): the configured Gitea PAT is a credential for the
// gitea host ONLY. In the service model a "provider" service's repo is addressed
// by owner/name on a known host, so a gitea-provider service is always ON the
// gitea host by construction — there is no repo_url that can point the PAT at an
// unrelated host. We therefore inject GIT_TOKEN iff the service is a gitea
// provider service (raw services, and github/gitlab provider services, never get
// the gitea PAT). github/gitlab push/clone tokens are per-user OAuth and arrive
// in M2.
func (r *Reconciler) addGitEnv(env map[string]string, run *domain.Run, svc *domain.Service) {
	// Default: readonly, no token. draft_pr may upgrade GIT_MODE below.
	env["GIT_MODE"] = string(domain.GitModeReadonly)

	giteaProvider := svc.RepoKind == domain.RepoKindProvider && svc.Provider == domain.ProviderGitea

	// Clone-token injection (F1) — applies to readonly AND draft_pr, gitea only.
	if giteaProvider && r.cfg.GiteaToken != "" {
		env["GIT_TOKEN"] = r.cfg.GiteaToken
	}

	// Push contract (ST-1) — draft_pr only, gitea only in M1.
	if svc.GitMode != domain.GitModeDraftPR {
		return
	}
	if !giteaProvider {
		r.log.Warn("draft_pr service is not a gitea provider; diff-only (M1 pushes gitea only)", "run", run.ID, "provider", svc.Provider)
		return
	}
	if r.cfg.GiteaToken == "" {
		r.log.Warn("draft_pr service but GITEA_TOKEN unset; runner will stay diff-only", "run", run.ID)
		return
	}
	pushURL := r.serviceRepoURL(svc)
	if pushURL == "" {
		r.log.Warn("draft_pr service but push URL could not be derived; diff-only", "run", run.ID)
		return
	}
	env["GIT_MODE"] = string(domain.GitModeDraftPR)
	env["GIT_BRANCH"] = "agent/run-" + run.ID
	env["GIT_PUSH_URL"] = pushURL
	// GIT_TOKEN already set above — used for both clone and push.
	env["GIT_BASE_BRANCH"] = svc.DefaultBranch
}
