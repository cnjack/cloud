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
}

// LabelRunID is the label the reconciler and operators use to find a run's Job.
const LabelRunID = "jcloud.run-id"

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
					Containers: []corev1.Container{{
						Name:  "runner",
						Image: c.cfg.RunnerImage,
						Env:   env,
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
