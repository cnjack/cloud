package k8s

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestClassify(t *testing.T) {
	cond := func(ty batchv1.JobConditionType, reason string) batchv1.JobCondition {
		return batchv1.JobCondition{Type: ty, Status: corev1.ConditionTrue, Reason: reason}
	}
	cases := []struct {
		name string
		job  *batchv1.Job
		want JobState
	}{
		{"complete", &batchv1.Job{Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{cond(batchv1.JobComplete, "")}}}, JobSucceeded},
		{"failed", &batchv1.Job{Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{cond(batchv1.JobFailed, "BackoffLimitExceeded")}}}, JobFailed},
		{"deadline", &batchv1.Job{Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{cond(batchv1.JobFailed, "DeadlineExceeded")}}}, JobDeadlineExceeded},
		{"active", &batchv1.Job{Status: batchv1.JobStatus{Active: 1}}, JobRunning},
		{"succeeded counter", &batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}, JobSucceeded},
		{"failed counter", &batchv1.Job{Status: batchv1.JobStatus{Failed: 1}}, JobFailed},
		{"pending", &batchv1.Job{Status: batchv1.JobStatus{}}, JobPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.job); got != tc.want {
				t.Fatalf("classify=%v want %v", got, tc.want)
			}
		})
	}
}

func TestJobName(t *testing.T) {
	if got := JobName("abc123"); got != "jcloud-run-abc123" {
		t.Fatalf("JobName=%q", got)
	}
}
