package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config configures the client-go JobLauncher.
type Config struct {
	Kubeconfig         string // path; empty => in-cluster
	Namespace          string
	RunnerImage        string
	PluginRuntimeImage string
	ServiceAccount     string
	TTLSeconds         int32
	CPULimit           string
	MemoryLimit        string
	CPURequest         string
	MemoryRequest      string

	// Persistent workspace (Feature C / D05). WorkspacePVCSize is the requested
	// size of a per-service PVC (e.g. "10Gi"); WorkspaceStorageClass is optional
	// (empty => the cluster's default StorageClass).
	WorkspacePVCSize      string
	WorkspaceStorageClass string
}

// LabelRunID is the label the reconciler and operators use to find a run's Job.
const LabelRunID = "jcloud.run-id"

// Labels stamped on a per-service workspace PVC for tenant attribution and
// cleanup (Feature C / D05).
const (
	LabelServiceID = "jcloud.service-id"
	LabelProjectID = "jcloud.project-id"
)

// Persistent-workspace mount layout (Feature C / D05). A SINGLE RWO PVC backs
// both the git checkout and the jcode memory HOME, split by subPath so no second
// volume is needed:
//   - work/  -> /workspace     (the runner's git working copy)
//   - home/  -> $HOME/.jcode    (jcode config.json + memory; the runner uses
//     the fixed non-root HOME=/home/jcode)
const (
	workspaceVolumeName         = "workspace"
	workspaceMountPath          = "/workspace"
	workspaceSubPath            = "work"
	jcodeHomeMountPath          = "/home/jcode/.jcode"
	jcodeHomeSubPath            = "home"
	pluginCredentialsVolumeName = "plugin-credentials"
	pluginCredentialsMountPath  = "/run/jcloud/plugins"
	pluginRuntimeVolumeName     = "plugin-runtime"
	pluginRuntimeMountPath      = "/run/jcloud/runtime"
	pluginRuntimeSkillsPath     = "/home/jcode/.jcode/skills"
	managedSkillsDir            = pluginRuntimeMountPath + "/skills"
	reservedSkills              = "github,gitlab,gitea"
	jcodeMCPConfigMountPath     = "/home/jcode/.jcode/mcp.json"
	pluginLifecycleVolumeName   = "plugin-lifecycle"
	pluginLifecycleMountPath    = "/run/jcloud/lifecycle"
	pluginSyncStopFile          = pluginLifecycleMountPath + "/runner-finished"
	runtimeConfigVolumeName     = "run-runtime-config"
	runtimeConfigMountPath      = "/run/jcloud/config"
	attachmentsVolumeName       = "run-attachments"
	attachmentsMountPath        = "/run/jcloud/attachments"
)

// Client is the client-go-backed JobLauncher.
type Client struct {
	cs  kubernetes.Interface
	cfg Config
}

// NewClient builds a Client from kubeconfig (or in-cluster if path is empty).
func NewClient(cfg Config) (*Client, error) {
	var rc *rest.Config
	var err error
	if cfg.Kubeconfig == "" {
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	} else {
		rc, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("build kubeconfig %q: %w", cfg.Kubeconfig, err)
		}
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("new clientset: %w", err)
	}
	return &Client{cs: cs, cfg: cfg}, nil
}

// CreateJob is idempotent: an AlreadyExists error is swallowed.
func (c *Client) CreateJob(ctx context.Context, spec JobSpec) error {
	job := c.buildJob(spec)
	_, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create job %s: %w", spec.Name, err)
	}
	return nil
}

// GetJobState inspects the Job's status conditions and counters.
func (c *Client) GetJobState(ctx context.Context, name string) (JobState, error) {
	job, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return JobMissing, nil
	}
	if err != nil {
		return JobUnknown, fmt.Errorf("get job %s: %w", name, err)
	}
	return classify(job), nil
}

// classify maps a batchv1.Job's status to a JobState. Exposed for tests.
func classify(job *batchv1.Job) JobState {
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return JobSucceeded
		case batchv1.JobFailed:
			// activeDeadlineSeconds exceeded surfaces as reason "DeadlineExceeded".
			if cond.Reason == "DeadlineExceeded" {
				return JobDeadlineExceeded
			}
			return JobFailed
		}
	}
	if job.Status.Active > 0 {
		return JobRunning
	}
	if job.Status.Succeeded > 0 {
		return JobSucceeded
	}
	if job.Status.Failed > 0 {
		return JobFailed
	}
	return JobPending
}

// EnsureWorkspacePVC idempotently creates the per-service persistent workspace
// PVC (Feature C / D05). It is ReadWriteOnce (one pod at a time — the reconciler
// serializes per-service runs to honour this), sized by WorkspacePVCSize, and
// bound to WorkspaceStorageClass when set (else the cluster default). An
// AlreadyExists is swallowed so a re-create across ticks / restarts is a no-op.
func (c *Client) EnsureWorkspacePVC(ctx context.Context, serviceID, projectID string) error {
	size := c.cfg.WorkspacePVCSize
	if size == "" {
		size = "10Gi"
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspacePVCName(serviceID),
			Namespace: c.cfg.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "jcloud",
				LabelServiceID:                 serviceID,
				LabelProjectID:                 projectID,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}
	// Empty StorageClassName ("") would REQUEST a PVC with no class; leave the
	// field nil so the cluster's default StorageClass applies. Set it only when
	// explicitly configured.
	if sc := c.cfg.WorkspaceStorageClass; sc != "" {
		pvc.Spec.StorageClassName = &sc
	}
	_, err := c.cs.CoreV1().PersistentVolumeClaims(c.cfg.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create workspace pvc %s: %w", pvc.Name, err)
	}
	// AlreadyExists is normally the idempotent re-ensure of a live PVC. But a PVC
	// that was JUST deleted (F10 archive finalize, or tenant erasure) can linger in
	// Terminating state while its pvc-protection finalizer clears — Create then
	// still returns AlreadyExists for that doomed object, and mounting a run onto a
	// Terminating PVC hangs the pod Pending until activeDeadlineSeconds fails it.
	// So inspect the existing object: a non-nil DeletionTimestamp means Terminating
	// → return a TRANSIENT error. The reconciler treats an EnsureWorkspacePVC
	// failure as transient (leaves the run queued, retries next tick), by which
	// time the old PVC is gone and the re-create binds a fresh one. A live PVC (no
	// DeletionTimestamp) is the normal idempotent no-op.
	existing, gerr := c.cs.CoreV1().PersistentVolumeClaims(c.cfg.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	if gerr != nil {
		// Raced away between Create and Get (e.g. the finalizer just cleared): let
		// the caller retry rather than proceed on an object we could not verify.
		return fmt.Errorf("inspect existing workspace pvc %s: %w", pvc.Name, gerr)
	}
	if existing.DeletionTimestamp != nil {
		return fmt.Errorf("workspace pvc %s is still terminating; deferring run until it is fully deleted", pvc.Name)
	}
	return nil
}

// WorkspacePVCExists reports whether the service's workspace PVC exists (F10).
// A NotFound is a clean false; any other error is propagated so the archive
// pass treats it as transient and retries rather than skipping the service.
func (c *Client) WorkspacePVCExists(ctx context.Context, serviceID string) (bool, error) {
	name := WorkspacePVCName(serviceID)
	_, err := c.cs.CoreV1().PersistentVolumeClaims(c.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get workspace pvc %s: %w", name, err)
	}
	return true, nil
}

// DeleteWorkspacePVC best-effort deletes a service's workspace PVC (D05 tenant
// erasure). A missing PVC is not an error.
func (c *Client) DeleteWorkspacePVC(ctx context.Context, serviceID string) error {
	name := WorkspacePVCName(serviceID)
	err := c.cs.CoreV1().PersistentVolumeClaims(c.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete workspace pvc %s: %w", name, err)
	}
	return nil
}

// DeleteJob deletes with foreground propagation so pods are cleaned up.
func (c *Client) DeleteJob(ctx context.Context, name string) error {
	policy := metav1.DeletePropagationBackground
	err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete job %s: %w", name, err)
	}
	return nil
}

func (c *Client) buildJob(spec JobSpec) *batchv1.Job {
	runnerImage := strings.TrimSpace(spec.Image)
	if runnerImage == "" {
		runnerImage = c.cfg.RunnerImage
	}
	pluginProviderSet := make(map[string]bool, len(spec.PluginProviders))
	for _, provider := range spec.PluginProviders {
		pluginProviderSet[provider] = true
	}
	env := make([]corev1.EnvVar, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	// jcode treats these names as Cloud-managed when the environment variable is
	// present. Keep the reservation on every run, including a run with no Plugin
	// snapshot, so repository or persistent user Skills cannot impersonate a
	// Provider that Cloud did not authorize for this run.
	env = append(env, corev1.EnvVar{Name: "JCODE_RESERVED_SKILLS", Value: reservedSkills})

	backoffLimit := int32(0) // one attempt per Job; retries are new runs
	ttl := c.cfg.TTLSeconds
	runAsUser := int64(10001)
	runAsGroup := int64(10001)
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := false // toolchains and package managers need /tmp and image paths.
	seccomp := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	containerSecurityContext := &corev1.SecurityContext{
		RunAsUser:                &runAsUser,
		RunAsGroup:               &runAsGroup,
		RunAsNonRoot:             &runAsNonRoot,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           seccomp,
	}
	var deadline *int64
	if spec.TimeoutSeconds > 0 {
		d := spec.TimeoutSeconds
		deadline = &d
	}

	// Persistent workspace (Feature C / D05): mount the service PVC at /workspace
	// (subPath work/) and $HOME/.jcode (subPath home/) so the checkout + jcode
	// memory survive across runs. Empty WorkspacePVC keeps the ephemeral podspec
	// (no volumes) — the pre-Feature-C behaviour used by local/DISABLE and the
	// existing tests.
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	if spec.WorkspacePVC != "" {
		volumes = []corev1.Volume{{
			Name: workspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: spec.WorkspacePVC,
				},
			},
		}}
		mounts = []corev1.VolumeMount{
			{Name: workspaceVolumeName, MountPath: workspaceMountPath, SubPath: workspaceSubPath},
			{Name: workspaceVolumeName, MountPath: jcodeHomeMountPath, SubPath: jcodeHomeSubPath},
		}
	}

	// Plugin runtime configuration is a deliberately short-lived tmpfs. The
	// runner mounts it read-only; only the sidecar has write access. This keeps
	// refresh tokens, App private keys, and the cluster master key outside the
	// pod while still letting CLI tools read access tokens/config atomically.
	if spec.PluginCredentials {
		medium := corev1.StorageMediumMemory
		volumes = append(volumes, corev1.Volume{
			Name:         pluginCredentialsVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}},
		})
		volumes = append(volumes, corev1.Volume{
			Name:         pluginLifecycleVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}},
		})
		volumes = append(volumes, corev1.Volume{
			Name:         pluginRuntimeVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name: pluginCredentialsVolumeName, MountPath: pluginCredentialsMountPath, ReadOnly: true,
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name: pluginRuntimeVolumeName, MountPath: pluginRuntimeMountPath, ReadOnly: true,
		})
		// Always mask the three managed Provider Skill paths with run tmpfs.
		// Selected Providers receive SKILL.md from the injector; unselected paths
		// are empty, preventing a stale copy in persistent jcode HOME from falsely
		// advertising a CLI that this run did not receive.
		for _, provider := range []string{"github", "gitlab", "gitea"} {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      pluginRuntimeVolumeName,
				MountPath: pluginRuntimeSkillsPath + "/" + provider,
				SubPath:   "skills/" + provider, ReadOnly: true,
			})
		}
		// jcode discovers global MCP servers from ~/.jcode/mcp.json. Mount the
		// initializer-created JType file over that one path so the credential
		// remains on tmpfs instead of the persistent jcode HOME volume.
		mounts = append(mounts, corev1.VolumeMount{
			Name: pluginCredentialsVolumeName, MountPath: jcodeMCPConfigMountPath,
			SubPath: "jtype/mcp.json", ReadOnly: true,
		})
	}
	if spec.ModelConfigBase64 != "" {
		medium := corev1.StorageMediumMemory
		volumes = append(volumes, corev1.Volume{Name: runtimeConfigVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}}})
		mounts = append(mounts, corev1.VolumeMount{Name: runtimeConfigVolumeName, MountPath: runtimeConfigMountPath, ReadOnly: true})
		env = append(env, corev1.EnvVar{Name: "JCODE_CONFIG", Value: runtimeConfigMountPath + "/config.json"})
	}
	var attachmentBytes int64
	if len(spec.Attachments) > 0 {
		medium := corev1.StorageMediumMemory
		attachmentBytes = 64 << 10 // manifest + filesystem metadata
		for _, attachment := range spec.Attachments {
			attachmentBytes += attachment.SizeBytes
		}
		volumes = append(volumes, corev1.Volume{Name: attachmentsVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium, SizeLimit: resource.NewQuantity(attachmentBytes, resource.BinarySI)}}})
		mounts = append(mounts, corev1.VolumeMount{Name: attachmentsVolumeName, MountPath: attachmentsMountPath, ReadOnly: true})
		env = append(env, corev1.EnvVar{Name: "JCODE_ATTACHMENTS_DIR", Value: attachmentsMountPath})
	}

	if spec.PluginCredentials {
		env = append(env,
			corev1.EnvVar{Name: "PATH", Value: pluginRuntimeMountPath + "/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			corev1.EnvVar{Name: "JCODE_PLUGIN_CREDENTIALS_DIR", Value: pluginCredentialsMountPath},
			corev1.EnvVar{Name: "JCODE_MANAGED_SKILLS_DIR", Value: managedSkillsDir},
			corev1.EnvVar{Name: "PLUGIN_SYNC_STOP_FILE", Value: pluginSyncStopFile},
		)
		if pluginProviderSet["github"] {
			env = append(env, corev1.EnvVar{Name: "GH_CONFIG_DIR", Value: pluginCredentialsMountPath + "/gh"})
		}
		if pluginProviderSet["gitlab"] {
			env = append(env, corev1.EnvVar{Name: "GLAB_CONFIG_DIR", Value: pluginCredentialsMountPath + "/glab"})
		}
		if pluginProviderSet["gitea"] {
			env = append(env, corev1.EnvVar{Name: "XDG_CONFIG_HOME", Value: pluginCredentialsMountPath})
		}
		if pluginProviderSet["github"] || pluginProviderSet["gitlab"] || pluginProviderSet["gitea"] {
			env = append(env, corev1.EnvVar{Name: "GIT_CONFIG_GLOBAL", Value: pluginCredentialsMountPath + "/git/config"})
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name: pluginLifecycleVolumeName, MountPath: pluginLifecycleMountPath,
		})
	}

	memoryLimit := resource.MustParse(c.cfg.MemoryLimit)
	memoryRequest := resource.MustParse(c.cfg.MemoryRequest)
	if attachmentBytes > 0 {
		addition := *resource.NewQuantity(attachmentBytes, resource.BinarySI)
		memoryLimit.Add(addition)
		memoryRequest.Add(addition)
	}
	runner := corev1.Container{
		Name:            "runner",
		Image:           runnerImage,
		SecurityContext: containerSecurityContext.DeepCopy(),
		Env:             env,
		VolumeMounts:    mounts,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(c.cfg.CPULimit),
				corev1.ResourceMemory: memoryLimit,
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(c.cfg.CPURequest),
				corev1.ResourceMemory: memoryRequest,
			},
		},
	}
	containers := []corev1.Container{runner}
	var initContainers []corev1.Container
	if spec.ModelConfigBase64 != "" {
		initContainers = append(initContainers, corev1.Container{Name: "run-model-effort-config", Image: runnerImage,
			Command:      []string{"/bin/sh", "-ec", `printf %s "$RUN_MODEL_CONFIG_B64" | base64 -d > /run/jcloud/config/config.json`},
			Env:          []corev1.EnvVar{{Name: "RUN_MODEL_CONFIG_B64", Value: spec.ModelConfigBase64}},
			VolumeMounts: []corev1.VolumeMount{{Name: runtimeConfigVolumeName, MountPath: runtimeConfigMountPath}}, SecurityContext: containerSecurityContext.DeepCopy()})
	}
	for index, attachment := range spec.Attachments {
		// Index is part of the name, so two long ids with a shared prefix cannot
		// collide after Kubernetes' 63-character name limit is applied.
		initContainers = append(initContainers, corev1.Container{Name: fmt.Sprintf("run-attachment-%02d", index+1), Image: runnerImage,
			Command:      []string{"/bin/sh", "-ec", `tmp="${ATTACHMENT_DEST}.partial"; trap 'rm -f "$tmp"' EXIT; curl --fail --silent --show-error --location "$ATTACHMENT_URL" --output "$tmp"; actual="$(wc -c < "$tmp" | tr -d ' ')"; [ "$actual" = "$ATTACHMENT_SIZE_BYTES" ] || { echo "attachment size mismatch" >&2; exit 1; }; mv "$tmp" "$ATTACHMENT_DEST"`},
			Env:          []corev1.EnvVar{{Name: "ATTACHMENT_URL", Value: attachment.URL}, {Name: "ATTACHMENT_DEST", Value: attachmentsMountPath + "/" + attachment.StageID}, {Name: "ATTACHMENT_SIZE_BYTES", Value: strconv.FormatInt(attachment.SizeBytes, 10)}},
			VolumeMounts: []corev1.VolumeMount{{Name: attachmentsVolumeName, MountPath: attachmentsMountPath}}, SecurityContext: containerSecurityContext.DeepCopy()})
	}
	if len(spec.Attachments) > 0 {
		type manifestAttachment struct {
			StageID     string `json:"stage_id"`
			DisplayName string `json:"display_name"`
			ContentType string `json:"content_type,omitempty"`
			SizeBytes   int64  `json:"size_bytes"`
			Path        string `json:"path"`
		}
		manifest := make([]manifestAttachment, 0, len(spec.Attachments))
		for _, a := range spec.Attachments {
			manifest = append(manifest, manifestAttachment{StageID: a.StageID, DisplayName: a.DisplayName, ContentType: a.ContentType, SizeBytes: a.SizeBytes, Path: attachmentsMountPath + "/" + a.StageID})
		}
		// All fields came from the control plane. Marshal/base64 keeps arbitrary
		// display names out of shell source and makes the manifest read-only.
		b, err := json.Marshal(manifest)
		if err != nil {
			panic("attachment manifest marshal: " + err.Error())
		}
		initContainers = append(initContainers, corev1.Container{Name: "run-attachments-manifest", Image: runnerImage,
			Command:      []string{"/bin/sh", "-ec", `printf %s "$ATTACHMENTS_MANIFEST_B64" | base64 -d > /run/jcloud/attachments/manifest.json`},
			Env:          []corev1.EnvVar{{Name: "ATTACHMENTS_MANIFEST_B64", Value: base64.StdEncoding.EncodeToString(b)}},
			VolumeMounts: []corev1.VolumeMount{{Name: attachmentsVolumeName, MountPath: attachmentsMountPath}}, SecurityContext: containerSecurityContext.DeepCopy()})
	}
	if spec.PluginCredentials {
		sidecarEnv := []corev1.EnvVar{
			{Name: "ORCH_BASE_URL", Value: spec.Env["ORCH_BASE_URL"]},
			{Name: "RUN_ID", Value: spec.Env["RUN_ID"]},
			{Name: "RUN_TOKEN", Value: spec.Env["RUN_TOKEN"]},
		}
		credentialMount := []corev1.VolumeMount{{Name: pluginCredentialsVolumeName, MountPath: pluginCredentialsMountPath}}
		runtimeMount := []corev1.VolumeMount{{Name: pluginRuntimeVolumeName, MountPath: pluginRuntimeMountPath}}
		lifecycleMount := corev1.VolumeMount{Name: pluginLifecycleVolumeName, MountPath: pluginLifecycleMountPath}
		// The release-pinned Orchestrator image owns the complete Provider bundle,
		// but this init container copies only the Providers present in the immutable
		// run snapshot. Unknown providers or missing assets fail the Job closed.
		initContainers = append(initContainers, corev1.Container{
			Name: "plugin-runtime-injector", Image: c.cfg.PluginRuntimeImage,
			Command:         []string{"/plugin-runtime", "inject", "--providers", strings.Join(spec.PluginProviders, ","), "--dir", pluginRuntimeMountPath},
			VolumeMounts:    runtimeMount,
			SecurityContext: containerSecurityContext.DeepCopy(),
		})
		// An init pass prevents the task from racing the long-lived sidecar on
		// startup. The sidecar then refreshes this same tmpfs config while the
		// task runs.
		initContainers = append(initContainers, corev1.Container{
			Name: "plugin-credential-initializer", Image: c.cfg.PluginRuntimeImage,
			Command: []string{"/plugin-runtime", "sync-credentials", "--providers", strings.Join(spec.PluginProviders, ","), "--once", "--dir", pluginCredentialsMountPath},
			Env:     sidecarEnv, VolumeMounts: credentialMount,
			SecurityContext: containerSecurityContext.DeepCopy(),
		})
		// The production cluster is Kubernetes 1.28, before native sidecars. A
		// normal companion container therefore watches a lifecycle file written
		// by entrypoint's EXIT trap and terminates promptly with the runner.
		containers = append(containers, corev1.Container{
			Name:            "plugin-credential-sync",
			Image:           c.cfg.PluginRuntimeImage,
			Command:         []string{"/plugin-runtime", "sync-credentials", "--providers", strings.Join(spec.PluginProviders, ","), "--dir", pluginCredentialsMountPath, "--stop-file", pluginSyncStopFile},
			Env:             sidecarEnv,
			VolumeMounts:    append(credentialMount, lifecycleMount),
			SecurityContext: containerSecurityContext.DeepCopy(),
		})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: c.cfg.Namespace,
			Labels: map[string]string{
				LabelRunID:                     spec.RunID,
				"app.kubernetes.io/managed-by": "jcloud",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{LabelRunID: spec.RunID},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           c.cfg.ServiceAccount,
					AutomountServiceAccountToken: func() *bool { v := false; return &v }(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:           &runAsUser,
						RunAsGroup:          &runAsGroup,
						RunAsNonRoot:        &runAsNonRoot,
						FSGroup:             &runAsGroup,
						FSGroupChangePolicy: func() *corev1.PodFSGroupChangePolicy { v := corev1.FSGroupChangeOnRootMismatch; return &v }(),
						SeccompProfile:      seccomp.DeepCopy(),
					},
					Volumes:        volumes,
					InitContainers: initContainers,
					Containers:     containers,
				},
			},
		},
	}
}

var _ JobLauncher = (*Client)(nil)
