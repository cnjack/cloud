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
}

// apply performs the side effects for one decision. Returns true if it made a
// scheduling change worth decrementing capacity for.
func (r *Reconciler) apply(ctx context.Context, run *domain.Run, d Decision) bool {
	switch d.Action {
	case ActionCreateJob:
		// Generate the per-run runner token now, at Job-create time, so the
		// plaintext exists only transiently (in the Job env) and only its hash
		// is persisted. If we crash after CreateJob but before persisting the
		// hash, the next tick regenerates a fresh token and recreates the Job
		// (idempotent by name) — the runner has not started ingesting yet.
		token, err := auth.GenerateRunToken()
		if err != nil {
			r.log.Error("reconcile: gen run token", "run", run.ID, "err", err)
			return false
		}
		jobName := k8s.JobName(run.ID)
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
		run.K8sJobName = jobName
		run.TokenHash = auth.HashToken(token)
		run.Status = domain.StatusScheduling
		run.Phase = "PreparingWorkspace"
		r.persist(ctx, run)
		return true

	case ActionMarkRunning:
		run.Status = domain.StatusRunning
		run.Phase = "StreamingTurn"
		if run.StartedAt == nil {
			t := r.now()
			run.StartedAt = &t
		}
		r.persist(ctx, run)

	case ActionMarkSucceeded:
		run.Status = domain.StatusSucceeded
		run.Phase = "Succeeded"
		r.finish(run)
		r.persist(ctx, run)

	case ActionMarkFailed:
		run.Status = domain.StatusFailed
		run.Phase = "Failed"
		// Preserve a runner-reported reason if one was already set via ingest;
		// only fill from the cluster-derived classification when empty.
		if run.FailureReason == "" {
			run.FailureReason = d.FailureReason
		}
		if run.FailureMessage == "" {
			run.FailureMessage = d.FailureMsg
		}
		run.Error = run.FailureMessage
		r.finish(run)
		r.persist(ctx, run)

	case ActionDeleteJob:
		if run.K8sJobName != "" {
			if err := r.launcher.DeleteJob(ctx, run.K8sJobName); err != nil {
				r.log.Error("reconcile: delete job", "run", run.ID, "err", err)
			}
		}
	}
	return false
}

func (r *Reconciler) finish(run *domain.Run) {
	if run.FinishedAt == nil {
		t := r.now()
		run.FinishedAt = &t
	}
}

// persist writes the run and emits a run.status event so live subscribers and
// the durable event log both reflect the transition.
func (r *Reconciler) persist(ctx context.Context, run *domain.Run) {
	if err := r.st.UpdateRun(ctx, run); err != nil {
		r.log.Error("reconcile: update run", "run", run.ID, "status", run.Status, "err", err)
		return
	}
	r.emitStatus(ctx, run)
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

// emit appends one internally-generated event, assigning the next seq, and
// notifies the publisher.
func (r *Reconciler) emit(ctx context.Context, runID, typ string, payload map[string]any) {
	seq, err := r.st.NextEventSeq(ctx, runID)
	if err != nil {
		r.log.Error("reconcile: next seq", "run", runID, "err", err)
		return
	}
	if _, err := r.st.AppendEvents(ctx, runID, []store.EventInput{{Seq: seq, Type: typ, Payload: payload}}); err != nil {
		r.log.Error("reconcile: append event", "run", runID, "err", err)
		return
	}
	if r.pub != nil {
		r.pub.Publish(runID, domain.RunEvent{
			RunID:   runID,
			Seq:     seq,
			TS:      r.now(),
			Type:    typ,
			Payload: payload,
		})
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
