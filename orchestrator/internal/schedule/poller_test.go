package schedule

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeModels is a ModelResolver stub returning a fixed selection/outcome.
type fakeModels struct {
	sel     modelcfg.Selection
	outcome modelcfg.SelectOutcome
	err     error
}

func (f *fakeModels) SelectModel(_ context.Context, _, _, _ string) (modelcfg.Selection, modelcfg.SelectOutcome, error) {
	return f.sel, f.outcome, f.err
}

// fakeHostGate is a HostGate stub.
type fakeHostGate struct {
	allowed bool
	host    string
	err     error
}

func (f fakeHostGate) IntegrationHostAllowed(_ context.Context, _ *domain.Service) (bool, string, error) {
	return f.allowed, f.host, f.err
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seed returns a MemStore with a project+service and installs one schedule
// (created via the store) so the poller sees it.
func seed(t *testing.T, sc *domain.Schedule) (*store.MemStore, *domain.Service) {
	t.Helper()
	m := store.NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}
	sc.ServiceID = svc.ID
	if err := m.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return m, svc
}

// okModels returns a resolver that always selects a concrete model.
func okModels() *fakeModels {
	return &fakeModels{sel: modelcfg.Selection{ModelID: "mid", ModelName: "prov/model"}, outcome: modelcfg.SelectOK}
}

func runsFor(t *testing.T, m *store.MemStore, svcID string) []domain.Run {
	t.Helper()
	runs, err := m.ListRunsByService(context.Background(), svcID, 100)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	return runs
}

// TestDispatchOnceWhenDue: a due schedule dispatches exactly one run, stamps the
// model, advances last_fired_at, and does NOT re-dispatch on the next tick.
func TestDispatchOnceWhenDue(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "nightly",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())

	runs := runsFor(t, m, svc.ID)
	if len(runs) != 1 {
		t.Fatalf("after first tick: %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Origin != domain.RunOriginSchedule {
		t.Errorf("run origin = %q, want schedule", r.Origin)
	}
	if r.Kind != domain.RunKindAgent || r.Status != domain.StatusQueued || r.Prompt != "nightly" {
		t.Errorf("run shape wrong: %+v", r)
	}
	if r.ModelName != "prov/model" || r.ModelID == nil || *r.ModelID != "mid" {
		t.Errorf("run model not stamped: name=%q id=%v", r.ModelName, r.ModelID)
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(now) {
		t.Errorf("last_fired_at = %v, want %v", got.LastFiredAt, now)
	}
	if got.LastError != "" {
		t.Errorf("last_error = %q, want empty on success", got.LastError)
	}
	// The dispatch is recorded on the run's initial event with the schedule id.
	evs, _ := m.ListEvents(context.Background(), r.ID, 0, 100)
	found := false
	for _, e := range evs {
		if e.Type == domain.EventRunStatus && e.Payload["schedule_id"] == sc.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no run.status event carrying schedule_id=%s", sc.ID)
	}

	// Second tick at the same instant must NOT dispatch again (Next(last_fired) is
	// strictly in the future).
	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("after second tick: %d runs, want 1 (no double dispatch)", len(got))
	}
}

// TestNotDueNoDispatch: a schedule whose next fire is in the future is skipped.
func TestNotDueNoDispatch(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "0 0 * * *", Prompt: "daily",
		Enabled: true, CreatedAt: now} // next midnight is ~12h away
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("not-due schedule dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastFiredAt != nil {
		t.Fatalf("not-due schedule advanced last_fired_at to %v", got.LastFiredAt)
	}
}

// TestRestartNoBackfill: a schedule that missed several windows while the process
// was down dispatches exactly ONE run (advanced to now), not one per missed window.
func TestRestartNoBackfill(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	// Hourly, created 3h ago, never fired: 3 windows were "missed".
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "0 * * * *", Prompt: "hourly",
		Enabled: true, CreatedAt: now.Add(-3 * time.Hour)}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("restart backfilled %d runs, want exactly 1", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(now) {
		t.Fatalf("last_fired_at = %v, want now (%v) — advanced to current, not a cron boundary", got.LastFiredAt, now)
	}
	// A follow-up tick does not fire again (next window is future).
	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("after follow-up tick: %d runs, want 1", len(got))
	}
}

// TestConcurrentPollersSingleWinner: N pollers ticking the same due schedule
// concurrently dispatch exactly one run — the conditional advance elects a single
// winner (anti-double-dispatch under two orchestrator instances).
func TestConcurrentPollersSingleWinner(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "race",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
			p.now = func() time.Time { return now }
			p.Tick(context.Background())
		}()
	}
	wg.Wait()

	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("concurrent pollers dispatched %d runs, want exactly 1", len(got))
	}
}

// TestModelGateFailVisible: when the model gate blocks, no run is dispatched, the
// window is advanced (not retried forever), and last_error records why.
func TestModelGateFailVisible(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "p",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	models := &fakeModels{outcome: modelcfg.SelectNotConfigured}
	p := NewPoller(m, models, nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("blocked schedule dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastError == "" {
		t.Fatalf("model-gate block did not record last_error")
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(now) {
		t.Fatalf("blocked window not advanced: last_fired_at=%v", got.LastFiredAt)
	}
}

// TestModelResolverTransientError: a transient resolver error must NOT burn the
// window (no advance, no last_error) so it is retried next tick.
func TestModelResolverTransientError(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "p",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	models := &fakeModels{err: context.DeadlineExceeded}
	p := NewPoller(m, models, nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("transient error dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastFiredAt != nil {
		t.Fatalf("transient error burned the window: last_fired_at=%v", got.LastFiredAt)
	}
	if got.LastError != "" {
		t.Fatalf("transient error wrote last_error=%q, want empty", got.LastError)
	}
}

// TestHostGateFailVisible: a host no longer in the cluster allowlist blocks the
// window fail-visibly.
func TestHostGateFailVisible(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "p",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	gate := fakeHostGate{allowed: false, host: "evil.example.com"}
	p := NewPoller(m, okModels(), gate, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("host-blocked schedule dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastError == "" || got.LastFiredAt == nil {
		t.Fatalf("host block not fail-visible: %+v", got)
	}
}

// TestDisabledNotDispatched: a disabled schedule is never scanned.
func TestDisabledNotDispatched(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "p",
		Enabled: false, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("disabled schedule dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastFiredAt != nil {
		t.Fatalf("disabled schedule advanced last_fired_at")
	}
}

// TestSuccessClearsPriorError: a schedule carrying a stale last_error clears it on
// the next successful dispatch.
func TestSuccessClearsPriorError(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "*/5 * * * *", Prompt: "p",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute), LastError: "stale error"}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("want 1 run, got %d", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastError != "" {
		t.Fatalf("successful dispatch did not clear last_error: %q", got.LastError)
	}
}

// TestInvalidCronRecordsError: a stored schedule whose cron no longer parses
// records last_error and never advances (there is no valid next fire).
func TestInvalidCronRecordsError(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "totally invalid", Prompt: "p",
		Enabled: true, CreatedAt: now.Add(-10 * time.Minute)}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 0 {
		t.Fatalf("invalid-cron schedule dispatched %d runs, want 0", len(got))
	}
	got, _ := m.GetSchedule(context.Background(), sc.ID)
	if got.LastError == "" || got.LastFiredAt != nil {
		t.Fatalf("invalid cron not recorded correctly: %+v", got)
	}
}

// TestCronEvaluatedInUTC pins the P1 semantics: cron expressions are evaluated
// in UTC regardless of the LOCATION the store returned the base time in
// (MemStore hands back UTC; pgx decodes timestamptz into the process-local
// zone). robfig/cron with no TZ prefix interprets the fields in the input
// time's location, so without the poller's base.UTC() normalization the firing
// hour would drift with the deployment's TZ.
func TestCronEvaluatedInUTC(t *testing.T) {
	// base = 19:00 in a +8 zone == 11:00 UTC; cron fires daily at 12:00.
	// Evaluated in UTC: next fire is 12:00Z, which is <= now (12:30Z) → dispatch.
	// Evaluated in the base's own +8 location (the bug): next fire would be
	// 12:00+08 the NEXT day (04:00Z) → nothing would dispatch.
	zone := time.FixedZone("UTC+8", 8*3600)
	created := time.Date(2026, 7, 9, 19, 0, 0, 0, zone) // 11:00Z
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)
	sc := &domain.Schedule{ID: domain.NewID(), CronExpr: "0 12 * * *", Prompt: "utc",
		Enabled: true, CreatedAt: created}
	m, svc := seed(t, sc)
	p := NewPoller(m, okModels(), nil, testLogger(), time.Minute)
	p.now = func() time.Time { return now }

	p.Tick(context.Background())
	if got := runsFor(t, m, svc.ID); len(got) != 1 {
		t.Fatalf("non-UTC base location: dispatched %d runs, want 1 (cron must be evaluated in UTC)", len(got))
	}

	// And a LastFiredAt carried in a non-UTC location behaves identically: from
	// 11:59Z (expressed in +8) the 12:00Z boundary is due; from 12:01Z it is not.
	sc2 := &domain.Schedule{ID: domain.NewID(), CronExpr: "0 12 * * *", Prompt: "utc2",
		Enabled: true, CreatedAt: created}
	fired := time.Date(2026, 7, 9, 20, 1, 0, 0, zone) // 12:01Z — boundary already consumed
	sc2.LastFiredAt = &fired
	m2, svc2 := seed(t, sc2)
	p2 := NewPoller(m2, okModels(), nil, testLogger(), time.Minute)
	p2.now = func() time.Time { return now }
	p2.Tick(context.Background())
	if got := runsFor(t, m2, svc2.ID); len(got) != 0 {
		t.Fatalf("last_fired_at past the boundary (in +8 form) still dispatched %d runs, want 0", len(got))
	}
}
