package main

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalSignalDetectsSplitCanonicalRateLimitFailure(t *testing.T) {
	var signal terminalSignal
	signal.Observe("\nRate limited by zhipuai-coding-plan (glm-5.2), and retr")
	signal.Observe("ies didn't clear it. Nothing was lost — wait a moment and send the message again.")
	if !signal.ModelRateLimited() {
		t.Fatal("split canonical rate-limit summary was not detected")
	}
}

func TestTerminalSignalRejectsLookalikes(t *testing.T) {
	tests := []string{
		"Rate limited briefly, but retry succeeded.",
		"The logs say: Rate limited by provider, and retries didn't clear it.",
		"unrelated agent failure",
	}
	for _, value := range tests {
		var signal terminalSignal
		signal.Observe(value)
		if signal.ModelRateLimited() {
			t.Fatalf("lookalike classified as terminal rate limit: %q", value)
		}
	}
}

func TestTerminalSignalResetPreventsCrossTurnClassification(t *testing.T) {
	var signal terminalSignal
	signal.Observe("Rate limited by provider (model), and retries didn't clear it.")
	if !signal.ModelRateLimited() {
		t.Fatal("precondition: canonical signal was not detected")
	}
	signal.Reset()
	signal.Observe("unrelated failure in the next turn")
	if signal.ModelRateLimited() {
		t.Fatal("a prior turn's rate-limit signal survived Reset")
	}
}

func TestRunClassifiesCanonicalSummaryOnlyWhenPromptFails(t *testing.T) {
	t.Setenv("ACPDRIVE_TEST_FAKE_AGENT", "1")
	t.Setenv("FAKE_AGENT_LIVE_TEXT", "Rate limited by provider (model), and retries didn't clear it. Nothing was lost.")
	t.Setenv("FAKE_AGENT_PROMPT_ERR", "provider request failed")
	t.Setenv("ORCH_BASE_URL", "")
	err := run(context.Background(), mustSelfExe(t), nil, t.TempDir(), "do it", "", false)
	if !errors.Is(err, errModelRateLimited) {
		t.Fatalf("run error = %v, want errModelRateLimited", err)
	}
}

func TestRunDoesNotClassifySuccessfulTurnThatMentionsRateLimit(t *testing.T) {
	t.Setenv("ACPDRIVE_TEST_FAKE_AGENT", "1")
	t.Setenv("FAKE_AGENT_LIVE_TEXT", "Rate limited by provider (model), and retries didn't clear it. Nothing was lost.")
	t.Setenv("FAKE_AGENT_PROMPT_ERR", "")
	t.Setenv("ORCH_BASE_URL", "")
	if err := run(context.Background(), mustSelfExe(t), nil, t.TempDir(), "do it", "", false); err != nil {
		t.Fatalf("successful turn was reclassified: %v", err)
	}
}
