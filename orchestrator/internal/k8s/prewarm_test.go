package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newPrewarmTestClient(image string) *Client {
	return &Client{
		cs:  fake.NewSimpleClientset(),
		cfg: Config{Namespace: "jcloud", RunnerImage: image, PluginRuntimeImage: "ghcr.io/acme/orchestrator:v1"},
	}
}

// First sync creates the DaemonSet: pinned to the configured image, pull-Always
// (so a pod restart re-pulls even an unchanged :latest tag), last-sync stamped.
func TestPrewarmFirstSyncCreatesDaemonSet(t *testing.T) {
	c := newPrewarmTestClient("ghcr.io/acme/runner:v1")
	ctx := context.Background()

	if err := c.PrewarmRunnerImage(ctx); err != nil {
		t.Fatalf("PrewarmRunnerImage: %v", err)
	}

	ds, err := c.cs.AppsV1().DaemonSets("jcloud").Get(ctx, PrewarmName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("daemonset not created: %v", err)
	}
	if len(ds.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("containers=%d want runner and Plugin runtime prewarm", len(ds.Spec.Template.Spec.Containers))
	}
	container := ds.Spec.Template.Spec.Containers[0]
	if container.Image != "ghcr.io/acme/runner:v1" {
		t.Fatalf("image=%q want ghcr.io/acme/runner:v1", container.Image)
	}
	if container.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("pullPolicy=%q want Always (pod restart must re-pull :latest)", container.ImagePullPolicy)
	}
	runtime := ds.Spec.Template.Spec.Containers[1]
	if runtime.Name != "plugin-runtime-prewarm" || runtime.Image != "ghcr.io/acme/orchestrator:v1" || runtime.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("runtime prewarm=%+v", runtime)
	}
	if ds.Spec.Template.Spec.AutomountServiceAccountToken == nil || *ds.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("prewarm Pod must not mount a ServiceAccount token")
	}
	if ds.Annotations[prewarmLastSyncAnnotation] == "" {
		t.Fatal("last-sync annotation not stamped")
	}

	st, err := c.RunnerImagePrewarmStatus(ctx)
	if err != nil {
		t.Fatalf("RunnerImagePrewarmStatus: %v", err)
	}
	if st.Image != "ghcr.io/acme/runner:v1" || st.LastSync == "" {
		t.Fatalf("status=%+v want image v1 + last_sync set", st)
	}
}

// A sync after RUNNER_IMAGE changed updates the DaemonSet template (the
// controller then rolls fresh pods on its own).
func TestPrewarmSyncFollowsConfiguredImage(t *testing.T) {
	c := newPrewarmTestClient("ghcr.io/acme/runner:v1")
	ctx := context.Background()
	if err := c.PrewarmRunnerImage(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	c.cfg.RunnerImage = "ghcr.io/acme/runner:v2"
	c.cfg.PluginRuntimeImage = "ghcr.io/acme/orchestrator:v2"
	if err := c.PrewarmRunnerImage(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	st, err := c.RunnerImagePrewarmStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Image != "ghcr.io/acme/runner:v2" {
		t.Fatalf("status.image=%q want v2 after RUNNER_IMAGE change", st.Image)
	}
	ds, err := c.cs.AppsV1().DaemonSets("jcloud").Get(ctx, PrewarmName, metav1.GetOptions{})
	if err != nil || len(ds.Spec.Template.Spec.Containers) != 2 || ds.Spec.Template.Spec.Containers[1].Image != "ghcr.io/acme/orchestrator:v2" {
		t.Fatalf("runtime prewarm did not follow configured image: ds=%+v err=%v", ds, err)
	}
}

// The unchanged-tag flow (re-pushed :latest): the template does not change, so
// the sync must RESTART the prewarm pods — pull-Always on the fresh pods is
// what actually drags the new digest onto the node.
func TestPrewarmSyncRestartsPodsOnSameImage(t *testing.T) {
	c := newPrewarmTestClient("ghcr.io/acme/runner:latest")
	ctx := context.Background()
	if err := c.PrewarmRunnerImage(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Two sleeper pods as the DaemonSet controller would have placed them.
	for _, name := range []string{"prewarm-a", "prewarm-b"} {
		_, err := c.cs.CoreV1().Pods("jcloud").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "jcloud",
				Labels:    map[string]string{prewarmComponentLabel: prewarmComponentValue},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("seed pod %s: %v", name, err)
		}
	}

	if err := c.PrewarmRunnerImage(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	pods, err := c.cs.CoreV1().Pods("jcloud").List(ctx, metav1.ListOptions{
		LabelSelector: prewarmComponentLabel + "=" + prewarmComponentValue,
	})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("pods after sync = %d, want 0 (all restarted for a fresh pull)", len(pods.Items))
	}
}

// Status before any sync is not an error: zero counts, the configured image,
// empty last_sync — the console renders "never synced" from that.
func TestPrewarmStatusNeverSyncedIsNotAnError(t *testing.T) {
	c := newPrewarmTestClient("ghcr.io/acme/runner:v1")
	st, err := c.RunnerImagePrewarmStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Desired != 0 || st.Ready != 0 || st.LastSync != "" || st.Image != "ghcr.io/acme/runner:v1" {
		t.Fatalf("status=%+v want zero counts + configured image + empty last_sync", st)
	}
}
