package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// newScheduleTestStore returns a MemStore seeded with one project + service so a
// schedule's service_id FK resolves.
func newScheduleTestStore(t *testing.T) (*MemStore, *domain.Service) {
	t.Helper()
	m := NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "sched", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}
	return m, svc
}

func TestScheduleCRUD(t *testing.T) {
	m, svc := newScheduleTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sc := &domain.Schedule{
		ID: domain.NewID(), ServiceID: svc.ID, CronExpr: "0 * * * *",
		Prompt: "do it", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := m.GetSchedule(ctx, sc.ID)
	if err != nil || got.CronExpr != "0 * * * *" || got.Prompt != "do it" || !got.Enabled {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	// List by service.
	list, err := m.ListSchedulesByService(ctx, svc.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list by service: len=%d err=%v", len(list), err)
	}
	// Enabled scan includes it.
	en, _ := m.ListEnabledSchedules(ctx)
	if len(en) != 1 {
		t.Fatalf("enabled scan: len=%d", len(en))
	}
	// Update mutates the owner-editable fields; resetWindow=false leaves the
	// poller-owned last_fired_at untouched (nil here).
	got.CronExpr = "0 0 * * *"
	got.Prompt = "changed"
	got.Enabled = false
	if err := m.UpdateSchedule(ctx, got, false); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := m.GetSchedule(ctx, sc.ID)
	if after.CronExpr != "0 0 * * *" || after.Prompt != "changed" || after.Enabled {
		t.Fatalf("update not applied: %+v", after)
	}
	if after.LastFiredAt != nil {
		t.Fatalf("resetWindow=false touched last_fired_at: %v", after.LastFiredAt)
	}
	// resetWindow=true moves last_fired_at to now (C1: cron change / re-enable
	// starts the cadence from the edit instant) and echoes it on the input struct.
	got.Enabled = true
	if err := m.UpdateSchedule(ctx, got, true); err != nil {
		t.Fatalf("update with reset: %v", err)
	}
	if got.LastFiredAt == nil || time.Since(*got.LastFiredAt) > time.Minute {
		t.Fatalf("resetWindow=true did not move last_fired_at to now: %v", got.LastFiredAt)
	}
	after, _ = m.GetSchedule(ctx, sc.ID)
	if after.LastFiredAt == nil || !after.LastFiredAt.Equal(*got.LastFiredAt) {
		t.Fatalf("committed last_fired_at %v != echoed %v", after.LastFiredAt, got.LastFiredAt)
	}
	// Flip back to disabled for the scan-set assertion below.
	got.Enabled = false
	if err := m.UpdateSchedule(ctx, got, false); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	// A disabled schedule drops out of the poller scan set.
	en, _ = m.ListEnabledSchedules(ctx)
	if len(en) != 0 {
		t.Fatalf("disabled schedule still in enabled scan: %d", len(en))
	}
	// Delete.
	if err := m.DeleteSchedule(ctx, sc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetSchedule(ctx, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

// TestAdvanceScheduleConditional is the anti-double-dispatch guard: only the
// instance whose prevFired matches the current last_fired_at wins the CAS.
func TestAdvanceScheduleConditional(t *testing.T) {
	m, svc := newScheduleTestStore(t)
	ctx := context.Background()
	sc := &domain.Schedule{
		ID: domain.NewID(), ServiceID: svc.ID, CronExpr: "0 * * * *",
		Prompt: "p", Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := m.CreateSchedule(ctx, sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	t1 := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 9, 15, 0, 5, 0, time.UTC)

	// First claim from the never-fired (nil) state wins and clears last_error.
	won, err := m.AdvanceSchedule(ctx, sc.ID, nil, t1, "")
	if err != nil || !won {
		t.Fatalf("first advance: won=%v err=%v (want won)", won, err)
	}
	// A racing claim that still thinks last_fired_at is nil LOSES — the row already
	// moved on. This is the exact double-poller scenario.
	won, err = m.AdvanceSchedule(ctx, sc.ID, nil, t2, "")
	if err != nil || won {
		t.Fatalf("racing advance: won=%v err=%v (want lost)", won, err)
	}
	after, _ := m.GetSchedule(ctx, sc.ID)
	if after.LastFiredAt == nil || !after.LastFiredAt.Equal(t1) {
		t.Fatalf("last_fired_at = %v, want %v (loser must not overwrite)", after.LastFiredAt, t1)
	}

	// A correct follow-up claim (prevFired = the current value) wins and can stamp
	// a fail-visible reason.
	won, err = m.AdvanceSchedule(ctx, sc.ID, &t1, t2, "model_not_configured")
	if err != nil || !won {
		t.Fatalf("chained advance: won=%v err=%v (want won)", won, err)
	}
	after, _ = m.GetSchedule(ctx, sc.ID)
	if after.LastError != "model_not_configured" || !after.LastFiredAt.Equal(t2) {
		t.Fatalf("chained advance not applied: %+v", after)
	}
}

func TestSetScheduleLastError(t *testing.T) {
	m, svc := newScheduleTestStore(t)
	ctx := context.Background()
	sc := &domain.Schedule{
		ID: domain.NewID(), ServiceID: svc.ID, CronExpr: "0 * * * *",
		Prompt: "p", Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = m.CreateSchedule(ctx, sc)
	if err := m.SetScheduleLastError(ctx, sc.ID, "boom"); err != nil {
		t.Fatalf("set last error: %v", err)
	}
	got, _ := m.GetSchedule(ctx, sc.ID)
	if got.LastError != "boom" || got.LastFiredAt != nil {
		t.Fatalf("set last error: %+v (last_fired_at must stay nil)", got)
	}
	if err := m.SetScheduleLastError(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set last error on missing: %v", err)
	}
}
