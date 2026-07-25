package k8s

import (
	"context"
	"strings"
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
	if m, ok := byPath["/home/jcode/.jcode"]; !ok || m.SubPath != "home" {
		t.Fatalf("/home/jcode/.jcode mount missing or wrong subPath: %+v", m)
	}
}

func TestBuildJobWithPluginCredentialsUsesReadOnlyTmpfsAndSidecar(t *testing.T) {
	c := &Client{cfg: Config{
		Namespace: "jcloud", RunnerImage: "runner:test",
		CPULimit: "2", MemoryLimit: "4Gi", CPURequest: "500m", MemoryRequest: "1Gi",
	}}
	job := c.buildJob(JobSpec{
		Name: "jcloud-run-x", RunID: "x", PluginCredentials: true,
		Env: map[string]string{"RUN_ID": "x", "RUN_TOKEN": "run-token", "ORCH_BASE_URL": "https://cloud.test"},
	})
	pod := job.Spec.Template.Spec
	if len(pod.Volumes) != 2 ||
		pod.Volumes[0].Name != pluginCredentialsVolumeName ||
		pod.Volumes[0].EmptyDir == nil ||
		pod.Volumes[0].EmptyDir.Medium != corev1.StorageMediumMemory ||
		pod.Volumes[1].Name != pluginLifecycleVolumeName ||
		pod.Volumes[1].EmptyDir == nil ||
		pod.Volumes[1].EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Fatalf("plugin volumes = %+v, want credential and lifecycle memory EmptyDirs", pod.Volumes)
	}
	if len(pod.Containers) != 2 {
		t.Fatalf("containers=%d want runner plus Kubernetes 1.28-compatible sync companion", len(pod.Containers))
	}
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "plugin-credential-initializer" || len(pod.InitContainers[0].Command) < 3 || pod.InitContainers[0].Command[2] != "--once" {
		t.Fatalf("init containers=%+v, want one credential initializer", pod.InitContainers)
	}
	runner, sidecar := pod.Containers[0], pod.Containers[1]
	if sidecar.Name != "plugin-credential-sync" || len(sidecar.Command) < 2 || sidecar.Command[1] != "sync-plugin-credentials" {
		t.Fatalf("sidecar=%+v, want plugin credential sync", sidecar)
	}
	if sidecar.RestartPolicy != nil {
		t.Fatalf("sidecar restart policy=%v, want normal Kubernetes 1.28 companion", sidecar.RestartPolicy)
	}
	if len(sidecar.Env) != 3 {
		t.Fatalf("sidecar env=%+v, want only run endpoint auth vars", sidecar.Env)
	}
	var runnerReadOnly, sidecarWritable, jcodeMCPMounted, runnerLifecycleWritable, sidecarLifecycleWritable bool
	for _, m := range runner.VolumeMounts {
		if m.Name == pluginCredentialsVolumeName {
			runnerReadOnly = m.ReadOnly
			if m.MountPath == jcodeMCPConfigMountPath && m.SubPath == "jtype/mcp.json" {
				jcodeMCPMounted = true
			}
		}
		if m.Name == pluginLifecycleVolumeName {
			runnerLifecycleWritable = !m.ReadOnly
		}
	}
	for _, m := range sidecar.VolumeMounts {
		if m.Name == pluginCredentialsVolumeName {
			sidecarWritable = !m.ReadOnly
		}
		if m.Name == pluginLifecycleVolumeName {
			sidecarLifecycleWritable = !m.ReadOnly
		}
	}
	if !runnerReadOnly || !sidecarWritable || !jcodeMCPMounted || !runnerLifecycleWritable || !sidecarLifecycleWritable {
		t.Fatalf("mounts runner_read_only=%v sidecar_writable=%v jcode_mcp=%v runner_lifecycle=%v sidecar_lifecycle=%v", runnerReadOnly, sidecarWritable, jcodeMCPMounted, runnerLifecycleWritable, sidecarLifecycleWritable)
	}
	values := map[string]string{}
	for _, e := range runner.Env {
		values[e.Name] = e.Value
	}
	if values["JCODE_PLUGIN_CREDENTIALS_DIR"] != pluginCredentialsMountPath ||
		values["GIT_CONFIG_GLOBAL"] == "" ||
		values["XDG_CONFIG_HOME"] != pluginCredentialsMountPath ||
		values["PLUGIN_SYNC_STOP_FILE"] != pluginSyncStopFile {
		t.Fatalf("runner plugin env=%v", values)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("runner Pod must not mount a Kubernetes service-account token")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 10001 ||
		pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 10001 ||
		pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod security context is not hardened: %+v", pod.SecurityContext)
	}
	assertHardened := func(name string, security *corev1.SecurityContext) {
		t.Helper()
		if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot ||
			security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
			security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
			security.Capabilities == nil || len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
			t.Fatalf("%s security context is not hardened: %+v", name, security)
		}
	}
	for i := range pod.InitContainers {
		assertHardened(pod.InitContainers[i].Name, pod.InitContainers[i].SecurityContext)
	}
	for i := range pod.Containers {
		assertHardened(pod.Containers[i].Name, pod.Containers[i].SecurityContext)
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

// TestEnsureWorkspacePVCTerminatingIsTransient guards the F10 archive→restore
// race: after finalizeArchiveJob deletes a service's PVC, a fast follow-up run
// may EnsureWorkspacePVC while the old same-named PVC is still Terminating (its
// pvc-protection finalizer not yet cleared). Create returns AlreadyExists for
// that doomed object; binding a run to it would hang the pod Pending until the
// Job deadline fails it. EnsureWorkspacePVC must instead return a transient error
// so the reconciler leaves the run queued and retries once the PVC is truly gone.
func TestEnsureWorkspacePVCTerminatingIsTransient(t *testing.T) {
	now := metav1.Now()
	terminating := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              WorkspacePVCName("svc1"),
			Namespace:         "jcloud",
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes.io/pvc-protection"},
		},
	}
	cs := fake.NewSimpleClientset(terminating)
	c := &Client{cs: cs, cfg: Config{Namespace: "jcloud", WorkspacePVCSize: "10Gi"}}
	err := c.EnsureWorkspacePVC(context.Background(), "svc1", "proj1")
	if err == nil {
		t.Fatal("EnsureWorkspacePVC must return a transient error for a Terminating PVC (never bind a run to it)")
	}
	if !strings.Contains(err.Error(), "terminating") {
		t.Fatalf("error should name the terminating PVC, got %v", err)
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
