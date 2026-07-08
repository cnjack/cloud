package k8s

import (
	"context"
	"fmt"

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
	Kubeconfig     string // path; empty => in-cluster
	Namespace      string
	RunnerImage    string
	ServiceAccount string
	TTLSeconds     int32
	CPULimit       string
	MemoryLimit    string
	CPURequest     string
	MemoryRequest  string

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
//   - home/  -> $HOME/.jcode    (jcode config.json + memory; HOME=/root per the
//     runner image, see runner/Dockerfile `ENV HOME=/root`)
const (
	workspaceVolumeName = "workspace"
	workspaceMountPath  = "/workspace"
	workspaceSubPath    = "work"
	jcodeHomeMountPath  = "/root/.jcode"
	jcodeHomeSubPath    = "home"
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
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create workspace pvc %s: %w", pvc.Name, err)
	}
	return nil
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
	env := make([]corev1.EnvVar, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}

	backoffLimit := int32(0) // one attempt per Job; retries are new runs
	ttl := c.cfg.TTLSeconds
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
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: c.cfg.ServiceAccount,
					Volumes:            volumes,
					Containers: []corev1.Container{{
						Name:         "runner",
						Image:        c.cfg.RunnerImage,
						Env:          env,
						VolumeMounts: mounts,
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(c.cfg.CPULimit),
								corev1.ResourceMemory: resource.MustParse(c.cfg.MemoryLimit),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(c.cfg.CPURequest),
								corev1.ResourceMemory: resource.MustParse(c.cfg.MemoryRequest),
							},
						},
					}},
				},
			},
		},
	}
}

var _ JobLauncher = (*Client)(nil)
