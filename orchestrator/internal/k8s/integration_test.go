package k8s

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestIntegrationJobLifecycle exercises the real client-go launcher against a
// live cluster. It is skipped unless JCLOUD_IT=1 so `go test ./...` needs no
// cluster. Run with:
//
//	JCLOUD_IT=1 KUBECONFIG=$HOME/.kube/config go test ./internal/k8s/ -run Integration -v
//
// Env knobs: K8S_NAMESPACE (default "default"), IT_RUNNER_IMAGE (default
// "busybox:latest" running `true`).
func TestIntegrationJobLifecycle(t *testing.T) {
	if os.Getenv("JCLOUD_IT") != "1" {
		t.Skip("integration test skipped; set JCLOUD_IT=1 to run against a real cluster")
	}
	ns := os.Getenv("K8S_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	image := os.Getenv("IT_RUNNER_IMAGE")
	if image == "" {
		image = "busybox:latest"
	}

	client, err := NewClient(Config{
		Kubeconfig:    os.Getenv("KUBECONFIG"),
		Namespace:     ns,
		RunnerImage:   image,
		TTLSeconds:    120,
		CPULimit:      "500m",
		MemoryLimit:   "256Mi",
		CPURequest:    "100m",
		MemoryRequest: "64Mi",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	runID := "it-" + time.Now().Format("150405")
	name := JobName(runID)
	spec := JobSpec{
		Name:           name,
		RunID:          runID,
		Env:            map[string]string{"RUN_ID": runID},
		TimeoutSeconds: 120,
	}
	t.Cleanup(func() {
		_ = client.DeleteJob(context.Background(), name)
	})

	if err := client.CreateJob(ctx, spec); err != nil {
		t.Fatalf("create job: %v", err)
	}
	// CreateJob is idempotent: a second create must not error.
	if err := client.CreateJob(ctx, spec); err != nil {
		t.Fatalf("idempotent create job: %v", err)
	}

	// Poll until the Job reaches a terminal state.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		state, err := client.GetJobState(ctx, name)
		if err != nil {
			t.Fatalf("get job state: %v", err)
		}
		if state == JobSucceeded || state == JobFailed || state == JobDeadlineExceeded {
			t.Logf("job reached terminal state: %s", state)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not terminate; last state=%s", state)
		}
		time.Sleep(2 * time.Second)
	}

	if err := client.DeleteJob(ctx, name); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	// Deleting a missing Job must be a no-op.
	if err := client.DeleteJob(ctx, name); err != nil {
		t.Fatalf("delete missing job should be no-op: %v", err)
	}
}
