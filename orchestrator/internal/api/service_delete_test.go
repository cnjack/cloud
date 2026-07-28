package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
)

// TestDeleteServiceSkipsAlreadyCleanedJobs: services with a long run history keep
// k8s_job_name for audit after job_cleaned_at is stamped. Deletion must not
// re-issue a DeleteJob for every historical name (that path is O(runs) and
// times out under gateway deadlines); only uncleansed Jobs plus the archive Job
// and workspace PVC are cleaned.
func TestDeleteServiceSkipsAlreadyCleanedJobs(t *testing.T) {
	ts, st, fake := newTestServerWithLauncher(t)
	p := createProject(t, ts)

	const n = 40
	cleanedNames := make([]string, 0, n)
	for i := 0; i < n; i++ {
		resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken, map[string]any{"prompt": "done"})
		var run domain.Run
		decode(t, resp, &run)
		jobName := "jcloud-run-" + run.ID
		if _, err := st.ScheduleRun(context.Background(), run.ID, jobName, "token", "Scheduling"); err != nil {
			t.Fatalf("schedule: %v", err)
		}
		if _, err := st.MarkRunning(context.Background(), run.ID, "Running", time.Now()); err != nil {
			t.Fatalf("running: %v", err)
		}
		if _, err := st.MarkSucceeded(context.Background(), run.ID, "Succeeded", time.Now()); err != nil {
			t.Fatalf("succeeded: %v", err)
		}
		if err := st.MarkJobCleaned(context.Background(), run.ID); err != nil {
			t.Fatalf("job cleaned: %v", err)
		}
		cleanedNames = append(cleanedNames, jobName)
	}

	// One still-active run whose Job must be canceled and deleted.
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken, map[string]any{"prompt": "active"})
	var active domain.Run
	decode(t, resp, &active)
	activeJob := "jcloud-run-" + active.ID
	if _, err := st.ScheduleRun(context.Background(), active.ID, activeJob, "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(context.Background(), active.ID, "Running", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := fake.CreateJob(context.Background(), k8s.JobSpec{Name: activeJob}); err != nil {
		t.Fatal(err)
	}
	fake.SetPVCExists(p.ServiceID, true)

	resp = do(t, "DELETE", ts.URL+"/api/v1/services/"+p.ServiceID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	for _, name := range cleanedNames {
		if containsDeleted(fake.Deleted, name) {
			t.Fatalf("unexpected DeleteJob for already-cleaned job %s (deleted=%v)", name, fake.Deleted)
		}
	}
	if !containsDeleted(fake.Deleted, activeJob) {
		t.Fatalf("active job not deleted: %v", fake.Deleted)
	}
	if !containsDeleted(fake.Deleted, k8s.ArchiveJobName(p.ServiceID)) {
		t.Fatalf("archive job not deleted: %v", fake.Deleted)
	}
	if !containsDeleted(fake.DeletedPVCs, p.ServiceID) {
		t.Fatalf("workspace PVC not deleted: %v", fake.DeletedPVCs)
	}
}

func containsDeleted(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
