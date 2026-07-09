package schedule

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

// ModelResolver runs the D21/F4 resolution chain for a project/service so the
// poller fails visibly (records last_error, abandons the window) instead of
// queueing a run that could never execute. requested is always "" on this
// headless path — a schedule never carries a composer model pick.
type ModelResolver interface {
	SelectModel(ctx context.Context, projectID, defaultModelID, requested string) (modelcfg.Selection, modelcfg.SelectOutcome, error)
}

// HostGate reports whether a service's bound git integration host is still
// permitted by the cluster allowlist (D20 / F5). Mirrors the API's dispatch-time
// gate so a schedule cannot keep firing runs against a host policy has since
// revoked. A service with no integration is always allowed.
type HostGate interface {
	IntegrationHostAllowed(ctx context.Context, svc *domain.Service) (allowed bool, host string, err error)
}

// Poller scans enabled schedules each tick and dispatches an agent run for every
// schedule whose next fire has come due. It owns no in-memory cursor — the
// authoritative state is schedules.last_fired_at, advanced with a conditional
// UPDATE so it is both restart-safe and safe to run in more than one instance.
type Poller struct {
	st       store.Store
	models   ModelResolver
	hostGate HostGate // nil => host gate skipped (no integration binding to check)
	log      *slog.Logger
	interval time.Duration
	now      func() time.Time
}

// NewPoller builds a Poller. models runs the D21 chain (share the API's resolver
// so a model-config change is immediately visible); hostGate checks the D20
// allowlist for integration-bound services (may be nil). interval<=0 still allows
// a manually-driven Tick (Run() would busy-loop, so main.go only starts Run when
// interval>0).
func NewPoller(st store.Store, models ModelResolver, hostGate HostGate, log *slog.Logger, interval time.Duration) *Poller {
	return &Poller{
		st: st, models: models, hostGate: hostGate, log: log, interval: interval,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Run drives the loop until ctx is cancelled, ticking every interval. It is the
// schedule analogue of the kanban poller's Run (and the reconciler's Run).
func (p *Poller) Run(ctx context.Context) {
	if p.interval <= 0 {
		p.log.Warn("schedule poller disabled: SCHEDULE_POLL_INTERVAL<=0")
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	p.log.Info("schedule poller started", "interval", p.interval)
	p.Tick(ctx) // one immediate pass so a due schedule doesn't wait a full tick
	for {
		select {
		case <-ctx.Done():
			p.log.Info("schedule poller stopping")
			return
		case <-ticker.C:
			p.Tick(ctx)
		}
	}
}

// Tick performs one scan over every enabled schedule. Exported so tests (and a
// manual trigger) can drive a single deterministic pass.
func (p *Poller) Tick(ctx context.Context) {
	schedules, err := p.st.ListEnabledSchedules(ctx)
	if err != nil {
		p.log.Error("schedule poll: list schedules", "err", err)
		return
	}
	for i := range schedules {
		p.fire(ctx, &schedules[i])
	}
}

// fire evaluates one schedule: if its next fire is due it CLAIMS the window
// (atomic advance) and dispatches a run — or, when a gate blocks it, advances the
// window with a fail-visible last_error and dispatches nothing.
//
// Ordering is the crux of exactly-once (single-window, single dispatch even with
// two poller instances): the outcome (dispatch vs. blocked) is decided FIRST, then
// the conditional AdvanceSchedule CLAIMS the window — only the instance that wins
// the CAS proceeds. The window is advanced to the CURRENT time, never the missed
// cron boundary, so a restart never backfills missed windows as a burst.
func (p *Poller) fire(ctx context.Context, sc *domain.Schedule) {
	cronSched, err := ParseCron(sc.CronExpr)
	if err != nil {
		// An invalid cron slipped past the create/update guard (or the guard
		// tightened). There is no valid "next fire" to advance to, so we cannot claim
		// a window — record the reason fail-visibly (throttled) and skip. The owner
		// must fix the expression.
		p.recordInvalidCron(ctx, sc)
		return
	}
	base := sc.CreatedAt
	if sc.LastFiredAt != nil {
		base = *sc.LastFiredAt
	}
	// Cron expressions are evaluated in UTC — always (P1). robfig/cron with no
	// TZ prefix interprets the fields in the INPUT time's location, and base's
	// location depends on the store (MemStore returns UTC, pgx decodes
	// timestamptz into the process-local zone) — without this normalization the
	// firing hour would drift with the deployment's TZ. now() is already UTC.
	base = base.UTC()
	now := p.now()
	if cronSched.Next(base).After(now) {
		return // next fire is still in the future
	}

	// The window is due. Load the service (for its project, default model and
	// integration binding) and decide the outcome BEFORE claiming the window.
	svc, err := p.st.GetService(ctx, sc.ServiceID)
	if err != nil {
		// Transient (or a racing service delete whose cascade will remove this
		// schedule) — skip WITHOUT claiming so the window is not burned.
		p.log.Warn("schedule poll: load service", "schedule", sc.ID, "service", sc.ServiceID, "err", err)
		return
	}

	blockReason, sel, transient := p.evaluateGates(ctx, sc, svc)
	if transient {
		return // a transient gate error skips without claiming — retry next tick
	}

	// Atomically CLAIM this window. last_error carries the block reason ("" on a
	// successful dispatch, which also clears any prior error). The instance that
	// loses the conditional update returns without dispatching — anti-double-fire.
	won, err := p.st.AdvanceSchedule(ctx, sc.ID, sc.LastFiredAt, now, blockReason)
	if err != nil {
		p.log.Error("schedule poll: advance window", "schedule", sc.ID, "err", err)
		return
	}
	if !won {
		return // another poller instance claimed this window
	}
	if blockReason != "" {
		// Fail-visible: the window is spent (not retried forever) and the reason is
		// now on the schedule for the console to show — no run is dispatched.
		p.log.Info("schedule poll: window blocked", "schedule", sc.ID, "reason", blockReason)
		return
	}

	// Gates passed and we own the window — dispatch the run.
	run := &domain.Run{
		ID:        domain.NewID(),
		ProjectID: svc.ProjectID,
		ServiceID: svc.ID,
		Prompt:    sc.Prompt,
		Status:    domain.StatusQueued,
		Kind:      domain.RunKindAgent,
		Phase:     "Queued",
		Origin:    domain.RunOriginSchedule,
		Attempt:   1,
		CreatedAt: now,
	}
	run.ModelName = sel.ModelName
	if sel.ModelID != "" {
		run.ModelID = &sel.ModelID
	}
	if err := p.st.CreateRun(ctx, run); err != nil {
		// The window was already advanced (claimed), so we cannot retry it —
		// exactly-once is deliberately preferred over at-least-once here. Record the
		// failure fail-visibly on the schedule; the NEXT window attempts a fresh
		// dispatch.
		p.log.Error("schedule poll: create run", "schedule", sc.ID, "err", err)
		if serr := p.st.SetScheduleLastError(ctx, sc.ID, "dispatch failed: "+err.Error()); serr != nil && !errors.Is(serr, store.ErrNotFound) {
			// Known last-resort trace (P2): a DOUBLE fault — the run insert failed AND
			// the fail-visible last_error write failed — leaves the claimed window with
			// no run and no recorded reason in the DB. This loud, greppable log line is
			// the only remaining evidence; we accept it rather than un-claiming the
			// window (which would reopen the double-dispatch door the CAS closed).
			p.log.Error("schedule window lost: dispatch failed and the failure could not be recorded on the schedule",
				"schedule", sc.ID, "service", sc.ServiceID, "window", now,
				"create_run_err", err, "record_err", serr)
		}
		return
	}
	// Record the triggering schedule id on the run's initial run.status event — the
	// runs table itself stays unchanged (docs/14 §5.7). Best-effort: a failed event
	// does not undo the dispatch.
	if _, err := p.st.AppendInternalEvent(ctx, run.ID, domain.EventRunStatus, map[string]any{
		"status":      string(run.Status),
		"phase":       run.Phase,
		"origin":      string(domain.RunOriginSchedule),
		"schedule_id": sc.ID,
	}); err != nil {
		p.log.Warn("schedule poll: append dispatch event", "schedule", sc.ID, "run", run.ID, "err", err)
	}
	p.log.Info("schedule poll: dispatched run", "schedule", sc.ID, "service", sc.ServiceID, "run", run.ID)
}

// evaluateGates resolves the model + host gates for a due schedule. It returns
// (blockReason, selection, transient):
//   - transient=true  → a transient error (DB/resolver); the caller skips WITHOUT
//     claiming the window so it is retried next tick rather than burned.
//   - blockReason!=""  → a DEFINITE fail-visible block (no/ambiguous model, host no
//     longer allowed); the caller claims the window with this reason and dispatches
//     nothing.
//   - blockReason=="" && !transient → dispatch is allowed with the returned selection.
func (p *Poller) evaluateGates(ctx context.Context, sc *domain.Schedule, svc *domain.Service) (string, modelcfg.Selection, bool) {
	// Model gate (D21 / F4): resolve the service default; the poller supplies no
	// composer pick. A transient resolver error must not burn the window.
	sel, outcome, err := p.models.SelectModel(ctx, svc.ProjectID, derefStr(svc.DefaultModelID), "")
	if err != nil {
		p.log.Error("schedule poll: resolve model", "schedule", sc.ID, "err", err)
		return "", modelcfg.Selection{}, true
	}
	if outcome != modelcfg.SelectOK {
		return modelBlockReason(outcome), modelcfg.Selection{}, false
	}
	// Dispatch host gate (D20 / F5): a since-tightened allowlist blocks an
	// integration-bound service's runs. A transient lookup error skips without
	// claiming; a definite "not allowed" blocks the window.
	if p.hostGate != nil {
		allowed, host, herr := p.hostGate.IntegrationHostAllowed(ctx, svc)
		if herr != nil {
			p.log.Error("schedule poll: host gate", "schedule", sc.ID, "err", herr)
			return "", modelcfg.Selection{}, true
		}
		if !allowed {
			return "integration host '" + host + "' is no longer in the cluster's allowed hosts", modelcfg.Selection{}, false
		}
	}
	return "", sel, false
}

// recordInvalidCron stamps a fail-visible last_error for a schedule whose cron
// expression no longer parses, throttled so it does not re-write the same reason
// every tick. It never advances last_fired_at (there is no valid next fire).
func (p *Poller) recordInvalidCron(ctx context.Context, sc *domain.Schedule) {
	msg := "invalid cron expression: " + sc.CronExpr
	if sc.LastError == msg {
		return // already recorded — don't spam an identical write each tick
	}
	if err := p.st.SetScheduleLastError(ctx, sc.ID, msg); err != nil && !errors.Is(err, store.ErrNotFound) {
		p.log.Warn("schedule poll: record invalid cron", "schedule", sc.ID, "err", err)
	}
}

// modelBlockReason renders the fail-visible last_error for a non-OK model
// resolution. NotSelected points the owner at the service default (several models
// are granted, a headless schedule can't pick); everything else means no usable
// model is authorized for the project.
func modelBlockReason(outcome modelcfg.SelectOutcome) string {
	if outcome == modelcfg.SelectNotSelected {
		return "no default model is set on this service — several are granted to the project, so a scheduled run cannot pick one"
	}
	return "no model is configured for this project — a cluster admin must grant one before scheduled runs can dispatch"
}

// derefStr returns the pointed-to string, or "" for a nil pointer.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
