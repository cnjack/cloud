package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeArchiver records presign calls and returns deterministic URLs, so tests
// can assert which object key was signed AND that only the URL (never an S3
// credential) reaches a pod.
type fakeArchiver struct {
	putKeys []string
	getKeys []string
	putErr  error
	getErr  error
}

func (f *fakeArchiver) PresignPut(key string, _ time.Duration) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	f.putKeys = append(f.putKeys, key)
	return "https://obj.test/PUT/" + key + "?sig=abc", nil
}

func (f *fakeArchiver) PresignGet(key string, _ time.Duration) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	f.getKeys = append(f.getKeys, key)
	return "https://obj.test/GET/" + key + "?sig=abc", nil
}

// testRecArchive builds a reconciler with persistent workspace + archive on.
func testRecArchive(t *testing.T, arch Archiver) (*Reconciler, *store.MemStore, *k8s.FakeLauncher) {
	t.Helper()
	rec, st, fake := testRec(t, 4)
	rec.cfg.PersistentWorkspace = true
	rec.cfg.ArchiveIdleDays = 14
	rec.archiver = arch
	return rec, st, fake
}

// seedIdleService creates a project + persistent 'default' service whose only
// run is terminal and lastRunAge old (so it looks idle). Returns service id.
func seedIdleService(t *testing.T, st *store.MemStore, lastRunAge time.Duration) string {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git",
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x",
		Status: domain.StatusSucceeded, Attempt: 1, CreatedAt: time.Now().Add(-lastRunAge),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return svc.ID
}

func TestReconcileArchive_NoOpWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(r *Reconciler)
	}{
		{"nil archiver", func(r *Reconciler) { r.archiver = nil }},
		{"persistent off", func(r *Reconciler) { r.cfg.PersistentWorkspace = false }},
		{"idle days <= 0", func(r *Reconciler) { r.cfg.ArchiveIdleDays = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, st, fake := testRecArchive(t, &fakeArchiver{})
			svc := seedIdleService(t, st, 30*24*time.Hour)
			fake.SetPVCExists(svc, true)
			tc.setup(rec)
			rec.reconcileArchive(ctx)
			if len(fake.Created) != 0 {
				t.Fatalf("%s: expected no archive job, got %v", tc.name, fake.CreatedNames())
			}
		})
	}
}

func TestReconcileArchive_LaunchesJobForIdleService(t *testing.T) {
	ctx := context.Background()
	arch := &fakeArchiver{}
	rec, st, fake := testRecArchive(t, arch)
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)

	rec.reconcileArchive(ctx)

	jobName := k8s.ArchiveJobName(svc)
	spec, ok := fake.LiveSpec(jobName)
	if !ok {
		t.Fatalf("archive job %s not created; created=%v", jobName, fake.CreatedNames())
	}
	if spec.Env["RUN_ARCHIVE"] != "1" {
		t.Errorf("archive job missing RUN_ARCHIVE=1: %v", spec.Env)
	}
	wantURL := "https://obj.test/PUT/" + workspaceArchiveKey(svc) + "?sig=abc"
	if spec.Env["ARCHIVE_UPLOAD_URL"] != wantURL {
		t.Errorf("ARCHIVE_UPLOAD_URL = %q, want %q", spec.Env["ARCHIVE_UPLOAD_URL"], wantURL)
	}
	if spec.WorkspacePVC != k8s.WorkspacePVCName(svc) {
		t.Errorf("archive job must mount the service PVC, got WorkspacePVC=%q", spec.WorkspacePVC)
	}
	// D16 red line: NO S3 credential ever enters the pod.
	assertNoS3Creds(t, spec.Env)
	if len(arch.putKeys) != 1 || arch.putKeys[0] != workspaceArchiveKey(svc) {
		t.Errorf("PresignPut keys = %v, want [%s]", arch.putKeys, workspaceArchiveKey(svc))
	}
}

func TestReconcileArchive_Idempotent(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)

	rec.reconcileArchive(ctx) // Missing -> create (fake marks Pending)
	rec.reconcileArchive(ctx) // Pending -> wait (no re-create)

	if got := len(fake.Created); got != 1 {
		t.Fatalf("archive job created %d times, want exactly 1 (idempotent)", got)
	}
}

func TestReconcileArchive_SkipsAlreadyArchived(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)
	if err := st.MarkServiceArchived(ctx, svc, workspaceArchiveKey(svc), time.Now()); err != nil {
		t.Fatal(err)
	}
	rec.reconcileArchive(ctx)
	if len(fake.Created) != 0 {
		t.Fatalf("already-archived service must be skipped, got %v", fake.CreatedNames())
	}
}

func TestReconcileArchive_SkipsWhenNoPVC(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	_ = seedIdleService(t, st, 30*24*time.Hour) // note: no SetPVCExists => PVC absent
	rec.reconcileArchive(ctx)
	if len(fake.Created) != 0 {
		t.Fatalf("service with no PVC must be skipped, got %v", fake.CreatedNames())
	}
}

func TestReconcileArchive_SkipsRecentlyActive(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 3*24*time.Hour) // 3d < 14d idle threshold
	fake.SetPVCExists(svc, true)
	rec.reconcileArchive(ctx)
	if len(fake.Created) != 0 {
		t.Fatalf("recently-active service must not archive, got %v", fake.CreatedNames())
	}
}

func TestReconcileArchive_SkipsWithLiveRun(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)
	// A currently-queued run makes the service not idle.
	live := &domain.Run{ID: domain.NewID(), ProjectID: "p", ServiceID: svc, Prompt: "x",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, live); err != nil {
		t.Fatal(err)
	}
	rec.reconcileArchive(ctx)
	if len(fake.Created) != 0 {
		t.Fatalf("service with a live run must not archive, got %v", fake.CreatedNames())
	}
}

func TestReconcileArchive_FinalizesOnSuccess(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)
	jobName := k8s.ArchiveJobName(svc)
	// Simulate the archive Job having completed the upload.
	fake.SetState(jobName, k8s.JobSucceeded)

	rec.reconcileArchive(ctx)

	// PVC deleted.
	if !contains(fake.DeletedPVCs, svc) {
		t.Errorf("expected DeleteWorkspacePVC(%s); deleted=%v", svc, fake.DeletedPVCs)
	}
	// Service marked archived with the deterministic key.
	got, err := st.GetService(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == nil {
		t.Fatal("service ArchivedAt not set after successful archive")
	}
	if got.ArchiveKey != workspaceArchiveKey(svc) {
		t.Errorf("ArchiveKey = %q, want %q", got.ArchiveKey, workspaceArchiveKey(svc))
	}
	// Archive Job deleted.
	if !contains(fake.Deleted, jobName) {
		t.Errorf("expected archive Job %s deleted after finalize; deleted=%v", jobName, fake.Deleted)
	}
}

func TestReconcileArchive_FailureLeavesUnarchived(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)
	jobName := k8s.ArchiveJobName(svc)
	fake.SetState(jobName, k8s.JobFailed)

	rec.reconcileArchive(ctx)

	got, err := st.GetService(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt != nil {
		t.Fatal("a failed archive upload must NOT mark the service archived (fail-visible)")
	}
	if contains(fake.DeletedPVCs, svc) {
		t.Error("failed archive must not delete the PVC")
	}
	// The failed Job is left in place (its TTL reaps it → natural backoff).
	if contains(fake.Deleted, jobName) {
		t.Error("failed archive Job should be left for its TTL, not deleted immediately")
	}
}

func TestRestore_InjectsURLAndClearsArchive(t *testing.T) {
	ctx := context.Background()
	arch := &fakeArchiver{}
	rec, st, fake := testRecArchive(t, arch)
	svc := seedIdleService(t, st, 30*24*time.Hour)
	key := "workspaces/custom-key.tar.zst"
	if err := st.MarkServiceArchived(ctx, svc, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A new run arrives for the archived service.
	proj, _ := firstProjectOfService(t, st, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: proj, ServiceID: svc, Prompt: "wake up",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)

	// The run was scheduled with a restore URL for the stored archive key.
	spec, ok := fake.LiveSpec(k8s.JobName(run.ID))
	if !ok {
		t.Fatalf("run Job not created; created=%v", fake.CreatedNames())
	}
	wantURL := "https://obj.test/GET/" + key + "?sig=abc"
	if spec.Env["RESTORE_ARCHIVE_URL"] != wantURL {
		t.Errorf("RESTORE_ARCHIVE_URL = %q, want %q", spec.Env["RESTORE_ARCHIVE_URL"], wantURL)
	}
	assertNoS3Creds(t, spec.Env)
	if len(arch.getKeys) != 1 || arch.getKeys[0] != key {
		t.Errorf("PresignGet keys = %v, want [%s]", arch.getKeys, key)
	}
	// Archive marker cleared once the restore run is dispatched.
	got, err := st.GetService(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt != nil || got.ArchiveKey != "" {
		t.Errorf("archive marker not cleared after restore dispatch: archived_at=%v key=%q", got.ArchivedAt, got.ArchiveKey)
	}
}

func TestRestore_FailVisibleWhenObjectStorageUnconfigured(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, nil) // archiver nil = object storage off
	svc := seedIdleService(t, st, 30*24*time.Hour)
	if err := st.MarkServiceArchived(ctx, svc, workspaceArchiveKey(svc), time.Now()); err != nil {
		t.Fatal(err)
	}
	proj, _ := firstProjectOfService(t, st, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: proj, ServiceID: svc, Prompt: "wake up",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)

	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusFailed {
		t.Fatalf("archived+no-objstore run must fail visibly, got status=%s", got.Status)
	}
	if got.FailureReason != domain.FailureSetupFailed {
		t.Errorf("failure_reason = %q, want %q", got.FailureReason, domain.FailureSetupFailed)
	}
	if !strings.Contains(got.FailureMessage, "object storage") {
		t.Errorf("failure message should name object storage, got %q", got.FailureMessage)
	}
	if len(fake.Created) != 0 {
		t.Error("no runner Job should launch when the workspace is unrestorable")
	}
}

func TestSchedulingGate_ArchiveJobBlocksRun(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour) // not archived
	// An archive Job is already in flight for this service.
	fake.SetState(k8s.ArchiveJobName(svc), k8s.JobRunning)

	proj, _ := firstProjectOfService(t, st, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: proj, ServiceID: svc, Prompt: "x",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusQueued {
		t.Fatalf("run should stay queued while its service is archiving, got %s", got.Status)
	}
	if _, ok := fake.LiveSpec(k8s.JobName(run.ID)); ok {
		t.Error("no run Job should be created while an archive Job holds the PVC")
	}
}

func TestSchedulingGate_FailedArchiveDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetState(k8s.ArchiveJobName(svc), k8s.JobFailed) // failed archive lingers

	proj, _ := firstProjectOfService(t, st, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: proj, ServiceID: svc, Prompt: "x",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("a failed archive Job must not block scheduling, got %s", got.Status)
	}
}

// TestSchedulingGate_SucceededArchiveIsFinalizedNotDeadlocked is the regression
// for the race where a run is queued at the exact moment the archive Job reaches
// Succeeded: the service leaves the idle-candidate set (so reconcileArchive
// won't finalize it), and a naive gate that just "blocks on Succeeded" would
// hang the run forever. The gate must finalize the archive itself.
func TestSchedulingGate_SucceededArchiveIsFinalizedNotDeadlocked(t *testing.T) {
	ctx := context.Background()
	arch := &fakeArchiver{}
	rec, st, fake := testRecArchive(t, arch)
	svc := seedIdleService(t, st, 30*24*time.Hour) // NOT archived yet
	fake.SetPVCExists(svc, true)
	// Archive Job already Succeeded (upload done) but not yet finalized.
	fake.SetState(k8s.ArchiveJobName(svc), k8s.JobSucceeded)

	proj, _ := firstProjectOfService(t, st, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: proj, ServiceID: svc, Prompt: "wake",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Tick 1: gate finalizes the Succeeded archive (marks archived, deletes PVC +
	// Job) and holds the run one tick.
	rec.Tick(ctx)
	got, _ := st.GetService(ctx, svc)
	if got.ArchivedAt == nil {
		t.Fatal("gate must finalize a Succeeded archive Job (service should be archived)")
	}
	if !contains(fake.DeletedPVCs, svc) || !contains(fake.Deleted, k8s.ArchiveJobName(svc)) {
		t.Fatalf("finalize must delete PVC + archive Job; deletedPVCs=%v deleted=%v", fake.DeletedPVCs, fake.Deleted)
	}
	r1, _ := st.GetRun(ctx, run.ID)
	if r1.Status != domain.StatusQueued {
		t.Fatalf("run should hold one tick during finalize, got %s", r1.Status)
	}

	// Tick 2: archive Job gone, service archived -> run schedules as a RESTORE.
	rec.Tick(ctx)
	r2, _ := st.GetRun(ctx, run.ID)
	if r2.Status != domain.StatusScheduling {
		t.Fatalf("run should schedule (restore) after finalize, got %s (no deadlock expected)", r2.Status)
	}
	spec, ok := fake.LiveSpec(k8s.JobName(run.ID))
	if !ok || spec.Env["RESTORE_ARCHIVE_URL"] == "" {
		t.Fatal("restore run must carry RESTORE_ARCHIVE_URL")
	}
	if len(arch.getKeys) != 1 {
		t.Errorf("expected one PresignGet for the restore, got %v", arch.getKeys)
	}
}

func TestArchiveTransient_PresignErrorDoesNotArchive(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRecArchive(t, &fakeArchiver{putErr: errors.New("boom")})
	svc := seedIdleService(t, st, 30*24*time.Hour)
	fake.SetPVCExists(svc, true)

	rec.reconcileArchive(ctx)

	if len(fake.Created) != 0 {
		t.Error("no archive Job should be created when the URL cannot be signed")
	}
	got, _ := st.GetService(ctx, svc)
	if got.ArchivedAt != nil {
		t.Error("service must not be marked archived on a presign failure")
	}
}

// --- helpers ---

func assertNoS3Creds(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		up := strings.ToUpper(k)
		if strings.Contains(up, "S3_") || strings.Contains(up, "SECRET_KEY") || strings.Contains(up, "ACCESS_KEY") {
			t.Errorf("D16 violation: pod env carries a credential-shaped key %q=%q", k, v)
		}
		if strings.Contains(v, "minioadmin") {
			t.Errorf("D16 violation: pod env value %q looks like an S3 credential", k)
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func firstProjectOfService(t *testing.T, st *store.MemStore, svcID string) (projectID, _ string) {
	t.Helper()
	svc, err := st.GetService(context.Background(), svcID)
	if err != nil {
		t.Fatal(err)
	}
	return svc.ProjectID, ""
}
