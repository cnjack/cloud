package reconciler

import (
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name        string
		status      domain.RunStatus
		jobState    k8s.JobState
		hasCapacity bool
		wantAction  Action
		wantReason  domain.FailureReason
	}{
		// queued
		{"queued+capacity -> create", domain.StatusQueued, k8s.JobUnknown, true, ActionCreateJob, ""},
		{"queued+no capacity -> none", domain.StatusQueued, k8s.JobUnknown, false, ActionNone, ""},

		// scheduling
		{"scheduling+pending -> none", domain.StatusScheduling, k8s.JobPending, true, ActionNone, ""},
		{"scheduling+running -> running", domain.StatusScheduling, k8s.JobRunning, true, ActionMarkRunning, ""},
		{"scheduling+succeeded -> succeeded", domain.StatusScheduling, k8s.JobSucceeded, true, ActionMarkSucceeded, ""},
		{"scheduling+failed -> failed(agent)", domain.StatusScheduling, k8s.JobFailed, true, ActionMarkFailed, domain.FailureAgentError},
		{"scheduling+deadline -> failed(timeout)", domain.StatusScheduling, k8s.JobDeadlineExceeded, true, ActionMarkFailed, domain.FailureTimeout},
		{"scheduling+missing -> failed(agent)", domain.StatusScheduling, k8s.JobMissing, true, ActionMarkFailed, domain.FailureAgentError},

		// running
		{"running+running -> none", domain.StatusRunning, k8s.JobRunning, true, ActionNone, ""},
		{"running+succeeded -> succeeded", domain.StatusRunning, k8s.JobSucceeded, true, ActionMarkSucceeded, ""},
		{"running+failed -> failed(agent)", domain.StatusRunning, k8s.JobFailed, true, ActionMarkFailed, domain.FailureAgentError},
		{"running+deadline -> failed(timeout)", domain.StatusRunning, k8s.JobDeadlineExceeded, true, ActionMarkFailed, domain.FailureTimeout},
		{"running+missing -> failed(agent)", domain.StatusRunning, k8s.JobMissing, true, ActionMarkFailed, domain.FailureAgentError},

		// terminal / blocked: never acted on
		{"succeeded -> none", domain.StatusSucceeded, k8s.JobSucceeded, true, ActionNone, ""},
		{"failed -> none", domain.StatusFailed, k8s.JobFailed, true, ActionNone, ""},
		{"canceled -> none", domain.StatusCanceled, k8s.JobMissing, true, ActionNone, ""},
		{"blocked -> none", domain.StatusBlocked, k8s.JobRunning, true, ActionNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{Status: tc.status}
			d := decide(run, tc.jobState, tc.hasCapacity)
			if d.Action != tc.wantAction {
				t.Fatalf("action = %v, want %v", d.Action, tc.wantAction)
			}
			if tc.wantAction == ActionMarkFailed {
				if d.FailureReason != tc.wantReason {
					t.Fatalf("failure reason = %v, want %v", d.FailureReason, tc.wantReason)
				}
				if d.FailureMsg == "" {
					t.Fatal("failure message must be non-empty")
				}
			}
		})
	}
}
