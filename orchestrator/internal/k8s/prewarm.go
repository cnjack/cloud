package k8s

// prewarm.go — runner-image prewarm: a DaemonSet of sleeper pods that keeps the
// configured runner image cached on every schedulable node, so a run's first
// container start never pays a cold multi-hundred-MB pull. The console Cluster
// page's "sync runner image" button triggers PrewarmRunnerImage; the /system
// snapshot surfaces RunnerImagePrewarmStatus.

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PrewarmName is the deterministic DaemonSet name, so sync is idempotent.
	PrewarmName = "jcloud-runner-prewarm"
	// prewarmComponentLabel/prewarmComponentValue mark the prewarm pods (and
	// select them when a sync restarts them).
	prewarmComponentLabel = "jcloud.io/component"
	prewarmComponentValue = "runner-prewarm"
	// prewarmLastSyncAnnotation stamps the last API-triggered sync (RFC3339) on
	// the DaemonSet; surfaced as last_sync in the /system snapshot.
	prewarmLastSyncAnnotation = "jcloud.io/last-prewarm-sync"
)

// PrewarmStatus is the launcher-facing summary of the runner-image prewarm
// DaemonSet, decoupled from appsv1 so the API layer never imports client-go.
type PrewarmStatus struct {
	Desired  int32
	Ready    int32
	Image    string
	LastSync string // RFC3339; "" when never synced through the API
}

// PrewarmRunnerImage (re)asserts the prewarm DaemonSet against the CURRENT
// configured runner image and restarts its pods: with imagePullPolicy:Always a
// fresh pod re-pulls even an unchanged :latest tag, which is exactly what a
// manual "sync" after re-pushing the image needs. Idempotent: the DaemonSet
// name is fixed, and an AlreadyExists-equivalent race just updates.
func (c *Client) PrewarmRunnerImage(ctx context.Context) error {
	dsClient := c.cs.AppsV1().DaemonSets(c.cfg.Namespace)
	now := time.Now().UTC().Format(time.RFC3339)

	existing, err := dsClient.Get(ctx, PrewarmName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := dsClient.Create(ctx, c.buildPrewarmDaemonSet(now), metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create prewarm daemonset: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get prewarm daemonset: %w", err)
	}

	// Image drift (RUNNER_IMAGE changed since the last sync): update the
	// template — the DaemonSet controller rolls fresh pods on its own. The pod
	// delete below is still required for the unchanged-template case, where no
	// rollout would happen by itself.
	if len(existing.Spec.Template.Spec.Containers) > 0 &&
		existing.Spec.Template.Spec.Containers[0].Image != c.cfg.RunnerImage {
		existing.Spec.Template.Spec.Containers[0].Image = c.cfg.RunnerImage
	}
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[prewarmLastSyncAnnotation] = now
	if _, err := dsClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update prewarm daemonset: %w", err)
	}
	return c.deletePrewarmPods(ctx)
}

// RunnerImagePrewarmStatus reads the prewarm DaemonSet. A missing DaemonSet is
// NOT an error — it just means the image was never synced through the API
// (Desired/Ready zero, LastSync empty).
func (c *Client) RunnerImagePrewarmStatus(ctx context.Context) (PrewarmStatus, error) {
	st := PrewarmStatus{Image: c.cfg.RunnerImage}
	ds, err := c.cs.AppsV1().DaemonSets(c.cfg.Namespace).Get(ctx, PrewarmName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return st, nil
	case err != nil:
		return st, fmt.Errorf("get prewarm daemonset: %w", err)
	}
	st.Desired = ds.Status.DesiredNumberScheduled
	st.Ready = ds.Status.NumberReady
	if len(ds.Spec.Template.Spec.Containers) > 0 {
		st.Image = ds.Spec.Template.Spec.Containers[0].Image
	}
	st.LastSync = ds.Annotations[prewarmLastSyncAnnotation]
	return st, nil
}

// deletePrewarmPods restarts every prewarm pod so the DaemonSet recreates them
// and imagePullPolicy:Always forces a fresh pull. Deleting a missing pod set is
// not an error.
func (c *Client) deletePrewarmPods(ctx context.Context) error {
	podsClient := c.cs.CoreV1().Pods(c.cfg.Namespace)
	pods, err := podsClient.List(ctx, metav1.ListOptions{
		LabelSelector: prewarmComponentLabel + "=" + prewarmComponentValue,
	})
	if err != nil {
		return fmt.Errorf("list prewarm pods: %w", err)
	}
	for i := range pods.Items {
		if err := podsClient.Delete(ctx, pods.Items[i].Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete prewarm pod %s: %w", pods.Items[i].Name, err)
		}
	}
	return nil
}

func (c *Client) buildPrewarmDaemonSet(lastSync string) *appsv1.DaemonSet {
	labels := map[string]string{prewarmComponentLabel: prewarmComponentValue}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PrewarmName,
			Namespace: c.cfg.Namespace,
			Labels: map[string]string{
				prewarmComponentLabel:          prewarmComponentValue,
				"app.kubernetes.io/managed-by": "jcloud",
			},
			Annotations: map[string]string{prewarmLastSyncAnnotation: lastSync},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					// A sleeper whose only job is to pin the image on the node.
					// No ServiceAccount: it never talks to the API. Requests are
					// kept at the floor so the DaemonSet is effectively free.
					Containers: []corev1.Container{{
						Name:            "prewarm",
						Image:           c.cfg.RunnerImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/bin/bash", "-c", "sleep infinity"},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1m"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

var _ ImagePrewarmer = (*Client)(nil)
