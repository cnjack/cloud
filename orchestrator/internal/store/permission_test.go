package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// seedPermissionRun creates a project + service + approval-mode session run in
// st and returns the run id.
func seedPermissionRun(t *testing.T, st Store) string {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "perm", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git",
		GitMode: domain.GitModeReadonly, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "chat",
		Status: domain.StatusRunning, Kind: domain.RunKindAgent, Session: true,
		PermissionMode: domain.PermissionModeApproval, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// permReq builds a fresh two-option permission request for run rid.
func permReq(rid, requestID string) *domain.RunPermission {
	return &domain.RunPermission{
		RequestID:  requestID,
		RunID:      rid,
		ToolCallID: "tc-1",
		Title:      "Run `rm -rf build/`",
		Options: []domain.PermissionOption{
			{OptionID: "allow", Name: "Allow", Kind: "allow_once"},
			{OptionID: "reject", Name: "Reject", Kind: "reject_once"},
		},
		CreatedAt: time.Now().UTC(),
	}
}

// permUser creates a real user (decided_by carries an FK to users.id — the
// SET NULL path is for later user deletion, not for made-up ids).
func permUser(t *testing.T, st Store, uid, name string) string {
	t.Helper()
	u := &domain.User{ID: domain.NewID(), DisplayName: name, CreatedAt: time.Now()}
	idn := &domain.UserIdentity{ID: domain.NewID(), UserID: u.ID, Provider: domain.ProviderGitea, ProviderUID: uid, Username: name,
		AccessTokenEnc: []byte("enc-" + uid), CreatedAt: time.Now().UTC()}
	if _, err := st.CreateUserWithIdentity(context.Background(), u, idn); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// exercisePermissionStore runs the full F8b store contract against any Store
// implementation — the SAME semantics must hold for MemStore and PGStore:
//   - upsert is insert-only idempotent (a duplicate never resets state),
//   - decide is conditional (exactly one winner; blocked once resolved),
//   - resolve is first-writer-wins and tolerates missing rows.
func exercisePermissionStore(t *testing.T, st Store) {
	ctx := context.Background()
	rid := seedPermissionRun(t, st)
	// uid must also be unique per run (identities are unique per provider uid
	// in a persistent dev database).
	userA := permUser(t, st, "pa-"+domain.NewID(), "perm-user-a")
	userB := permUser(t, st, "pb-"+domain.NewID(), "perm-user-b")
	// request_id is a GLOBAL primary key (acpdrive generates UUIDv4s), so the
	// ids must be unique per test run — a fixed literal would collide with a
	// previous run's rows in a persistent dev database.
	req1 := "req-1-" + domain.NewID()
	req2 := "req-2-" + domain.NewID()

	// Upsert against a missing run → ErrNotFound (FK).
	if err := st.UpsertRunPermission(ctx, permReq("no-such-run", "req-orphan-"+domain.NewID())); err != ErrNotFound {
		t.Fatalf("upsert for missing run: err=%v want ErrNotFound", err)
	}

	// Fresh upsert stores the row verbatim; Get finds it.
	if err := st.UpsertRunPermission(ctx, permReq(rid, req1)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRunPermission(ctx, rid, req1)
	if err != nil || got.Title != "Run `rm -rf build/`" || len(got.Options) != 2 || got.Options[1].Kind != "reject_once" {
		t.Fatalf("get after upsert: %+v err=%v", got, err)
	}
	if got.Decided() || got.Resolved() {
		t.Fatalf("fresh row must be pending: %+v", got)
	}
	// Get under the WRONG run id must not leak the row (the decision endpoint
	// is per-run-token scoped).
	if _, err := st.GetRunPermission(ctx, "other-run", req1); err != ErrNotFound {
		t.Fatalf("cross-run get: err=%v want ErrNotFound", err)
	}

	// Decide: first answer wins…
	dec, won, err := st.DecideRunPermission(ctx, rid, req1, "allow", userA, time.Now().UTC())
	if err != nil || !won || dec.DecidedOptionID == nil || *dec.DecidedOptionID != "allow" || dec.DecidedBy == nil || *dec.DecidedBy != userA {
		t.Fatalf("first decide: %+v won=%v err=%v", dec, won, err)
	}
	// …a second answer loses without error and does NOT overwrite.
	dec2, won2, err := st.DecideRunPermission(ctx, rid, req1, "reject", userB, time.Now().UTC())
	if err != nil || won2 || *dec2.DecidedOptionID != "allow" {
		t.Fatalf("second decide: %+v won=%v err=%v (must lose, keep 'allow')", dec2, won2, err)
	}

	// A duplicate request EVENT (at-least-once) re-upserts: decided state stays.
	if err := st.UpsertRunPermission(ctx, permReq(rid, req1)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRunPermission(ctx, rid, req1)
	if !got.Decided() || *got.DecidedOptionID != "allow" {
		t.Fatalf("re-upsert reset the decided state: %+v", got)
	}

	// Resolve stamps the runner outcome; a duplicate resolve is a no-op.
	if err := st.ResolveRunPermission(ctx, rid, req1, "allow", "user", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRunPermission(ctx, rid, req1)
	if !got.Resolved() || *got.ResolvedOptionID != "allow" || *got.Resolution != "user" {
		t.Fatalf("after resolve: %+v", got)
	}
	if err := st.ResolveRunPermission(ctx, rid, req1, "reject", "timeout", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRunPermission(ctx, rid, req1)
	if *got.ResolvedOptionID != "allow" || *got.Resolution != "user" {
		t.Fatalf("duplicate resolve overwrote: %+v", got)
	}

	// A resolve for an unknown request_id is a silent no-op (best-effort event).
	if err := st.ResolveRunPermission(ctx, rid, "req-never-seen-"+domain.NewID(), "", "timeout", time.Now().UTC()); err != nil {
		t.Fatalf("orphan resolve: err=%v want nil", err)
	}

	// Deciding a RESOLVED request must lose (runner already self-resolved).
	if err := st.UpsertRunPermission(ctx, permReq(rid, req2)); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveRunPermission(ctx, rid, req2, "reject", "timeout", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, won, err = st.DecideRunPermission(ctx, rid, req2, "allow", userA, time.Now().UTC())
	if err != nil || won {
		t.Fatalf("decide on resolved: won=%v err=%v (must lose)", won, err)
	}

	// Decide on a missing row → ErrNotFound.
	if _, _, err := st.DecideRunPermission(ctx, rid, "req-ghost-"+domain.NewID(), "allow", "", time.Now().UTC()); err != ErrNotFound {
		t.Fatalf("decide missing: err=%v want ErrNotFound", err)
	}

	// List returns both rows, oldest first.
	list, err := st.ListRunPermissions(ctx, rid)
	if err != nil || len(list) != 2 || list[0].RequestID != req1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
}

// TestMemPermissionStore runs the shared contract against the in-memory store.
func TestMemPermissionStore(t *testing.T) {
	exercisePermissionStore(t, NewMemStore())
}

// TestPGPermissionStore runs the SAME contract against a real Postgres
// (migration 0015: JSONB options round-trip, conditional decide WHERE clause,
// ON CONFLICT DO NOTHING upsert). Skips without a DSN.
func TestPGPermissionStore(t *testing.T) {
	st, _ := pgTestStore(t)
	exercisePermissionStore(t, st)
}

// TestPGRunPermissionModePersists pins that runs.permission_mode (0015) survives
// a CreateRun/GetRun round-trip and defaults to '' for legacy writers.
func TestPGRunPermissionModePersists(t *testing.T) {
	st, _ := pgTestStore(t)
	ctx := context.Background()
	rid := seedPermissionRun(t, st)
	got, err := st.GetRun(ctx, rid)
	if err != nil || got.PermissionMode != domain.PermissionModeApproval {
		t.Fatalf("permission_mode=%q err=%v want approval", got.PermissionMode, err)
	}
}
