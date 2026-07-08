package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// F8b reconciler half: a permission_mode=approval SESSION run gets
// RUN_PERMISSION_MODE=approval + PERMISSION_TIMEOUT_SECONDS injected into its
// Job env; the timeout is min(300, session_ttl/4) so a stalled approval can
// never burn a meaningful share of the session's whole TTL (the F8a doc
// requires PERMISSION_TIMEOUT_SECONDS << RUN_TIMEOUT). Non-approval runs get
// NEITHER var (the runner then defaults to full_access — behaviour unchanged).

// queueApprovalSession creates a queued approval-mode session run.
func queueApprovalSession(t *testing.T, st interface {
	CreateRun(ctx context.Context, r *domain.Run) error
}, pid, sid string) *domain.Run {
	t.Helper()
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true,
		PermissionMode: domain.PermissionModeApproval, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestApprovalSessionJobEnv: with a LARGE session TTL the permission timeout
// caps at the 300s ceiling (min(300, 7200/4=1800) = 300).
func TestApprovalSessionJobEnv(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.SessionTTLSecs = 7200
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	queueApprovalSession(t, st, pid, sid)

	rec.Tick(ctx)

	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	if spec.Env["RUN_SESSION"] != "1" {
		t.Errorf("RUN_SESSION=%q want 1", spec.Env["RUN_SESSION"])
	}
	if spec.Env["RUN_PERMISSION_MODE"] != "approval" {
		t.Errorf("RUN_PERMISSION_MODE=%q want approval", spec.Env["RUN_PERMISSION_MODE"])
	}
	if spec.Env["PERMISSION_TIMEOUT_SECONDS"] != "300" {
		t.Errorf("PERMISSION_TIMEOUT_SECONDS=%q want 300 (min(300, 7200/4))", spec.Env["PERMISSION_TIMEOUT_SECONDS"])
	}
}

// TestApprovalSessionTimeoutScalesWithSmallTTL: a SHORT session TTL pulls the
// permission timeout under the 300s ceiling (min(300, 400/4) = 100) — the
// project override (not just the cluster default) drives the formula.
func TestApprovalSessionTimeoutScalesWithSmallTTL(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.SessionTTLSecs = 7200 // cluster default would cap at 300…
	ttl := int64(400)
	pid, sid := seedSessionProject(t, st, &domain.Project{SessionTTLSecs: &ttl}) // …project override wins
	queueApprovalSession(t, st, pid, sid)

	rec.Tick(ctx)

	spec := fake.Created[0]
	if spec.Env["PERMISSION_TIMEOUT_SECONDS"] != "100" {
		t.Errorf("PERMISSION_TIMEOUT_SECONDS=%q want 100 (min(300, 400/4))", spec.Env["PERMISSION_TIMEOUT_SECONDS"])
	}
	if spec.Env["RUN_TIMEOUT"] != "400s" {
		t.Errorf("RUN_TIMEOUT=%q want 400s (project session TTL)", spec.Env["RUN_TIMEOUT"])
	}
}

// TestNonApprovalRunsGetNoPermissionEnv: neither a full_access session nor a
// plain single-shot run carries the permission vars (the runner must keep
// defaulting to full_access — injecting anything would be a behaviour change).
func TestNonApprovalRunsGetNoPermissionEnv(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	// A full_access session run…
	sess := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// …and a plain single-shot run.
	plain := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "one shot",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().Add(time.Millisecond)}
	if err := st.CreateRun(ctx, plain); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)

	if len(fake.Created) != 2 {
		t.Fatalf("created %d jobs want 2", len(fake.Created))
	}
	for _, spec := range fake.Created {
		if _, ok := spec.Env["RUN_PERMISSION_MODE"]; ok {
			t.Errorf("job %s: RUN_PERMISSION_MODE must not be injected for a non-approval run", spec.Name)
		}
		if _, ok := spec.Env["PERMISSION_TIMEOUT_SECONDS"]; ok {
			t.Errorf("job %s: PERMISSION_TIMEOUT_SECONDS must not be injected for a non-approval run", spec.Name)
		}
	}
}

// TestPermissionTimeoutFormula pins the pure helper, including the degenerate
// floors (a tiny/unset TTL must never emit 0 — the runner would treat 0 as
// its 300s default, silently DEFEATING the "timeout << TTL" invariant).
func TestPermissionTimeoutFormula(t *testing.T) {
	cases := []struct {
		ttl  int64
		want int64
	}{
		{ttl: 14400, want: 300}, // cluster default TTL: ceiling wins
		{ttl: 1200, want: 300},  // 1200/4 == 300: exactly the ceiling
		{ttl: 400, want: 100},   // small TTL: ttl/4 wins
		{ttl: 3, want: 1},       // degenerate: floor at 1s, never 0
		{ttl: 0, want: 300},     // unbounded TTL: plain 300s default
		{ttl: -5, want: 300},    // defensive: negative treated as unbounded
	}
	for _, c := range cases {
		if got := permissionTimeoutSecs(c.ttl); got != c.want {
			t.Errorf("permissionTimeoutSecs(%d) = %d want %d", c.ttl, got, c.want)
		}
	}
}
