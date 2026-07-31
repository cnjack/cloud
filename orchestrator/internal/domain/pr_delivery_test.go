package domain

import "testing"

func TestPRReadyPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		policy PRReadyPolicy
		valid  bool
	}{
		{PRReadyPolicyAlwaysDraft, true},
		{PRReadyPolicyLifecycleAware, true},
		{"", false},
		{"automatic", false},
	} {
		if got := ValidPRReadyPolicy(tc.policy); got != tc.valid {
			t.Errorf("ValidPRReadyPolicy(%q)=%v want %v", tc.policy, got, tc.valid)
		}
	}
}

func TestRunPushBranchTreatsEmptyKindAsDefaultAgent(t *testing.T) {
	run := &Run{ID: "abcdef123456", PRHeadBranch: "feature/existing-pr"}
	if got := RunPushBranch(run); got != "feature/existing-pr" {
		t.Fatalf("RunPushBranch(empty kind)=%q want existing PR head", got)
	}
	run.Kind = RunKindReview
	if got := RunPushBranch(run); got != "jcode/run-abcdef12" {
		t.Fatalf("RunPushBranch(review)=%q want deterministic non-update branch", got)
	}
}
