package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/cuberuntime"
)

func TestPGCubeRuntimeOwnershipCheckpointAndCascade(t *testing.T) {
	st, id := pgTestStore(t)
	ctx := context.Background()
	run, err := st.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	r := cuberuntime.NewPGRegistry(st.Pool(), "test-"+id)
	j := &cuberuntime.Job{Name: "jcloud-run-" + id, RunID: id, ServiceID: run.ServiceID, SandboxID: "sandbox-test", CheckpointKey: "checkpoint-one", CreatedAt: time.Now().UTC()}
	if err := r.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveWorkspace(ctx, run.ServiceID, j.CheckpointKey); err != nil {
		t.Fatal(err)
	}
	got, err := r.Job(ctx, j.Name)
	if err != nil || got.SandboxID != j.SandboxID || got.ServiceID != run.ServiceID {
		t.Fatalf("readback: %+v %v", got, err)
	}
	other := cuberuntime.NewPGRegistry(st.Pool(), "another-owner")
	if got, err := other.Job(ctx, j.Name); err != nil || got != nil {
		t.Fatal("cross-deployment resource visible")
	}
	unlock, err := r.Lock(ctx, j.Name)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	now := time.Now().UTC()
	j.CleanedAt = &now
	j.CheckpointSaved = true
	if err := r.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if jobs, err := r.PendingJobs(ctx, run.ServiceID); err != nil || len(jobs) != 0 {
		t.Fatal("cleaned job remains pending")
	}
	if jobs, err := r.AllJobs(ctx, run.ServiceID); err != nil || len(jobs) != 1 {
		t.Fatal("checkpoint erased from service-erasure inventory")
	}
	if err := st.DeleteProject(ctx, run.ProjectID); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Job(ctx, j.Name); err != nil || got != nil {
		t.Fatal("runtime ownership did not cascade")
	}
	if _, exists, err := r.Workspace(ctx, run.ServiceID); err != nil || exists {
		t.Fatal("workspace ownership did not cascade")
	}
}
