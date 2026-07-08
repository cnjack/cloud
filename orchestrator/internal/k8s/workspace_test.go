package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkspacePVCName(t *testing.T) {
	if got := WorkspacePVCName("svc123"); got != "ws-svc123" {
		t.Fatalf("WorkspacePVCName=%q want ws-svc123", got)
	}
}

// TestBuildJobNoPVC pins the pre-Feature-C ephemeral behaviour: an empty
// WorkspacePVC yields NO volumes and NO volume mounts.
func TestBuildJobNoPVC(t *testing.T) {
	c := &Client{cfg: Config{
		Namespace: "jcloud", RunnerImage: "runner:test",
		CPULimit: "2", MemoryLimit: "4Gi", CPURequest: "500m", MemoryRequest: "1Gi",
	}}
	job := c.buildJob(JobSpec{Name: "jcloud-run-x", RunID: "x", Env: map[string]string{"RUN_ID": "x"}})
	pod := job.Spec.Template.Spec
	if len(pod.Volumes) != 0 {
		t.Fatalf("ephemeral job has %d volumes, want 0", len(pod.Volumes))
	}
	if len(pod.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("ephemeral job has %d mounts, want 0", len(pod.Containers[0].VolumeMounts))
	}
}

// TestBuildJobWithPVC verifies the persistent layout: a single PVC-backed volume
// mounted at /workspace (subPath work) and $HOME/.jcode (subPath home).
func TestBuildJobWithPVC(t *testing.T) {
	c := &Client{cfg: Config{
		Namespace: "jcloud", RunnerImage: "runner:test",
		CPULimit: "2", MemoryLimit: "4Gi", CPURequest: "500m", MemoryRequest: "1Gi",
	}}
	job := c.buildJob(JobSpec{
		Name: "jcloud-run-x", RunID: "x", Env: map[string]string{"RUN_ID": "x"},
		WorkspacePVC: "ws-svc1",
	})
	pod := job.Spec.Template.Spec

	if len(pod.Volumes) != 1 {
		t.Fatalf("persistent job has %d volumes, want 1", len(pod.Volumes))
	}
	vol := pod.Volumes[0]
	if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != "ws-svc1" {
		t.Fatalf("volume is not backed by PVC ws-svc1: %+v", vol)
	}

	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 2 {
		t.Fatalf("persistent job has %d mounts, want 2 (work + home)", len(mounts))
	}
	byPath := map[string]corev1.VolumeMount{}
	for _, m := range mounts {
		if m.Name != vol.Name {
			t.Fatalf("mount %q references volume %q, want %q", m.MountPath, m.Name, vol.Name)
		}
		byPath[m.MountPath] = m
	}
	if m, ok := byPath["/workspace"]; !ok || m.SubPath != "work" {
		t.Fatalf("/workspace mount missing or wrong subPath: %+v", m)
	}
	if m, ok := byPath["/root/.jcode"]; !ok || m.SubPath != "home" {
		t.Fatalf("/root/.jcode mount missing or wrong subPath: %+v", m)
	}
}

// TestEnsureWorkspacePVCCreatesRWO checks the created PVC is RWO, sized from
// config, carries the service/project labels, and (given a storage class) sets
// it. Uses the client-go fake clientset.
func TestEnsureWorkspacePVCCreatesRWO(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &Client{cs: cs, cfg: Config{
		Namespace: "jcloud", WorkspacePVCSize: "20Gi", WorkspaceStorageClass: "fast-ssd",
	}}
	ctx := context.Background()
	if err := c.EnsureWorkspacePVC(ctx, "svc1", "proj1"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("jcloud").Get(ctx, "ws-svc1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("access modes = %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}
	want := resource.MustParse("20Gi")
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(want) != 0 {
		t.Fatalf("storage request = %s, want 20Gi", got.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-ssd" {
		t.Fatalf("storage class = %v, want fast-ssd", pvc.Spec.StorageClassName)
	}
	if pvc.Labels[LabelServiceID] != "svc1" || pvc.Labels[LabelProjectID] != "proj1" {
		t.Fatalf("labels = %v, want service/project stamped", pvc.Labels)
	}
}

// TestEnsureWorkspacePVCEmptyStorageClassLeavesDefault: no configured class => the
// PVC's StorageClassName is left nil so the cluster default applies (NOT "").
func TestEnsureWorkspacePVCEmptyStorageClassLeavesDefault(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &Client{cs: cs, cfg: Config{Namespace: "jcloud", WorkspacePVCSize: "10Gi"}}
	ctx := context.Background()
	if err := c.EnsureWorkspacePVC(ctx, "svc2", "proj2"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	pvc, _ := cs.CoreV1().PersistentVolumeClaims("jcloud").Get(ctx, "ws-svc2", metav1.GetOptions{})
	if pvc.Spec.StorageClassName != nil {
		t.Fatalf("storage class = %q, want nil (cluster default)", *pvc.Spec.StorageClassName)
	}
}

// TestEnsureWorkspacePVCIdempotent: a second Ensure is a no-op (AlreadyExists
// swallowed), and exactly one PVC exists.
func TestEnsureWorkspacePVCIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &Client{cs: cs, cfg: Config{Namespace: "jcloud", WorkspacePVCSize: "10Gi"}}
	ctx := context.Background()
	if err := c.EnsureWorkspacePVC(ctx, "svc1", "proj1"); err != nil {
		t.Fatalf("ensure #1: %v", err)
	}
	if err := c.EnsureWorkspacePVC(ctx, "svc1", "proj1"); err != nil {
		t.Fatalf("ensure #2 (idempotent) returned error: %v", err)
	}
	list, _ := cs.CoreV1().PersistentVolumeClaims("jcloud").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("have %d PVCs after two ensures, want 1", len(list.Items))
	}
}

// TestDeleteWorkspacePVC removes an existing PVC and is a no-op on a missing one.
func TestDeleteWorkspacePVC(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &Client{cs: cs, cfg: Config{Namespace: "jcloud", WorkspacePVCSize: "10Gi"}}
	ctx := context.Background()
	if err := c.EnsureWorkspacePVC(ctx, "svc1", "proj1"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := c.DeleteWorkspacePVC(ctx, "svc1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("jcloud").Get(ctx, "ws-svc1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pvc still present after delete (err=%v)", err)
	}
	// Deleting the now-missing PVC must be a no-op.
	if err := c.DeleteWorkspacePVC(ctx, "svc1"); err != nil {
		t.Fatalf("delete missing pvc should be no-op: %v", err)
	}
}
