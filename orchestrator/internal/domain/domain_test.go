package domain

import (
	"testing"
	"time"
)

func TestCanTransition(t *testing.T) {
	cases := []struct {
		name string
		from RunStatus
		to   RunStatus
		want bool
	}{
		{"queued->scheduling", StatusQueued, StatusScheduling, true},
		{"queued->canceled", StatusQueued, StatusCanceled, true},
		{"queued->failed", StatusQueued, StatusFailed, true},
		{"queued->running (skip)", StatusQueued, StatusRunning, false},
		{"queued->succeeded (skip)", StatusQueued, StatusSucceeded, false},
		{"scheduling->running", StatusScheduling, StatusRunning, true},
		{"scheduling->succeeded (fast job)", StatusScheduling, StatusSucceeded, true},
		{"scheduling->failed", StatusScheduling, StatusFailed, true},
		{"scheduling->canceled", StatusScheduling, StatusCanceled, true},
		{"scheduling->queued (backward)", StatusScheduling, StatusQueued, false},
		{"running->succeeded", StatusRunning, StatusSucceeded, true},
		{"running->failed", StatusRunning, StatusFailed, true},
		{"running->canceled", StatusRunning, StatusCanceled, true},
		{"running->queued (backward)", StatusRunning, StatusQueued, false},
		{"running->scheduling (backward)", StatusRunning, StatusScheduling, false},
		{"succeeded->failed (terminal)", StatusSucceeded, StatusFailed, false},
		{"failed->running (terminal)", StatusFailed, StatusRunning, false},
		{"canceled->running (terminal)", StatusCanceled, StatusRunning, false},
		// Session (D22): running <-> awaiting_input and the finalize/fail/cancel exits.
		{"running->awaiting_input (session turn done)", StatusRunning, StatusAwaitingInput, true},
		{"awaiting_input->running (message delivered)", StatusAwaitingInput, StatusRunning, true},
		{"awaiting_input->succeeded (finalized)", StatusAwaitingInput, StatusSucceeded, true},
		{"awaiting_input->failed (pod died)", StatusAwaitingInput, StatusFailed, true},
		{"awaiting_input->canceled (user cancel)", StatusAwaitingInput, StatusCanceled, true},
		{"awaiting_input->scheduling (backward)", StatusAwaitingInput, StatusScheduling, false},
		{"awaiting_input->queued (backward)", StatusAwaitingInput, StatusQueued, false},
		{"queued->awaiting_input (skip)", StatusQueued, StatusAwaitingInput, false},
		{"scheduling->awaiting_input (skip)", StatusScheduling, StatusAwaitingInput, false},
		// idempotent no-op transitions are always allowed
		{"queued->queued (noop)", StatusQueued, StatusQueued, true},
		{"running->running (noop)", StatusRunning, StatusRunning, true},
		{"awaiting_input->awaiting_input (noop)", StatusAwaitingInput, StatusAwaitingInput, true},
		{"succeeded->succeeded (noop)", StatusSucceeded, StatusSucceeded, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransition(%s,%s)=%v want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestStatusTerminalAndValid(t *testing.T) {
	terminal := []RunStatus{StatusSucceeded, StatusFailed, StatusCanceled}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	nonTerminal := []RunStatus{StatusQueued, StatusScheduling, StatusRunning, StatusAwaitingInput, StatusBlocked}
	for _, s := range nonTerminal {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	all := []RunStatus{
		StatusQueued, StatusScheduling, StatusRunning, StatusAwaitingInput,
		StatusSucceeded, StatusFailed, StatusCanceled, StatusBlocked,
	}
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if RunStatus("bogus").Valid() {
		t.Error("bogus status should be invalid")
	}
}

func TestValidFailureReason(t *testing.T) {
	// push_failed (ST-1) is a first-class reason alongside the original four.
	valid := []FailureReason{FailureCloneFailed, FailureSetupFailed, FailureAgentError, FailureTimeout, FailurePushFailed}
	for _, r := range valid {
		if !ValidFailureReason(r) {
			t.Errorf("%s should be valid", r)
		}
	}
	if ValidFailureReason("nope") {
		t.Error("nope should be invalid")
	}
}

func TestValidGitMode(t *testing.T) {
	if !ValidGitMode(GitModeReadonly) || !ValidGitMode(GitModeDraftPR) {
		t.Error("readonly and draft_pr must be valid git modes")
	}
	if ValidGitMode("") || ValidGitMode("merge") {
		t.Error("empty/unknown git modes must be invalid")
	}
}

func TestBackoff(t *testing.T) {
	// Symphony formula: min(10000 * 2^(attempt-1), 300000) ms.
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 10 * time.Second},   // clamped to attempt 1
		{1, 10 * time.Second},   // 10000
		{2, 20 * time.Second},   // 20000
		{3, 40 * time.Second},   // 40000
		{4, 80 * time.Second},   // 80000
		{5, 160 * time.Second},  // 160000
		{6, 300 * time.Second},  // 320000 -> capped 300000
		{10, 300 * time.Second}, // capped
		{50, 300 * time.Second}, // no overflow
	}
	for _, tc := range cases {
		got := Backoff(tc.attempt, 10000, 300000)
		if got != tc.want {
			t.Errorf("Backoff(%d)=%v want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 32 {
			t.Fatalf("id length = %d, want 32", len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
