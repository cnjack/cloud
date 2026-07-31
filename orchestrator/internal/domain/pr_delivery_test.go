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
