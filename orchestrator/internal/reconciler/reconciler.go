package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
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
	cfg      *config.Config
	log      *slog.Logger
	pub      Publisher
	now      func() time.Time // injectable clock for tests
}

// New builds a Reconciler. pub may be nil.
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
// transiently) and clears the job name once the Job is gone. This is the only
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
			continue // retry next tick; leave the name so we try again
		}
		state, err := r.launcher.GetJobState(ctx, run.K8sJobName)
		if err != nil {
			r.log.Warn("reconcile: cleanup get job state", "run", run.ID, "err", err)
			continue
		}
		if state == k8s.JobMissing {
			if err := r.st.ClearJobName(ctx, run.ID); err != nil {
				r.log.Warn("reconcile: cleanup clear job name", "run", run.ID, "err", err)
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
	// REPO_URL / REPO_BRANCH come from the run's project.
	if proj, err := r.st.GetProject(ctx, run.ProjectID); err == nil {
		env["REPO_URL"] = proj.RepoURL
		env["REPO_BRANCH"] = proj.DefaultBranch
	} else {
		r.log.Error("reconcile: get project for env", "run", run.ID, "err", err)
	}
	return env
}
