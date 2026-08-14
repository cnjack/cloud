package main

import (
	"os"
	"strings"
	"testing"
)

// TestReviewPromptRequiresTheValidatedSubmissionTool prevents the runner from
// drifting back to best-effort REVIEW.json generation. The tool owns the exact
// validator limits, changed-line Plan check, and completion receipt.
func TestReviewPromptRequiresTheValidatedSubmissionTool(t *testing.T) {
	entrypoint, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	contract := string(entrypoint)
	for _, want := range []string{
		"mcp__review__submit_review",
		"only accepted completion path",
		"do not write REVIEW.json directly",
		"not end the turn until the tool reports",
		"Never\nclaim complete merely because findings is empty",
		"reviewmcp verify",
		"successful submit_review tool call",
		`"command": "reviewmcp"`,
		"\"max_iterations\": $JCODE_MAX_ITERATIONS",
	} {
		if !strings.Contains(contract, want) {
			t.Errorf("review protocol does not publish validator constraint %q", want)
		}
	}
}

// TestReviewIterationBudgetRemainsOpenDuringPOC prevents the review runner from
// regressing to the 40-iteration ceiling that stopped a real review immediately
// after its verification build completed. Wall-clock timeout and the validated
// submit_review receipt remain the fail-visible bounds.
func TestReviewIterationBudgetRemainsOpenDuringPOC(t *testing.T) {
	entrypoint, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	contract := string(entrypoint)
	if !strings.Contains(contract, "JCODE_MAX_ITERATIONS=1000") {
		t.Fatal("runner no longer publishes the open 1000-iteration POC budget")
	}
	if strings.Contains(contract, `[ "$RUN_KIND" = "review" ] && JCODE_MAX_ITERATIONS=40`) {
		t.Fatal("review runner still restores the premature 40-iteration ceiling")
	}
}

// TestScenarioForRequestReview proves a request whose messages carry the
// "[review]" marker selects the review scenario regardless of the env default,
// and that a normal request keeps the default.
func TestScenarioForRequestReview(t *testing.T) {
	t.Setenv("MOCK_SCENARIO", "write_file")

	// Plain string content with the marker → review scenario.
	name, sc := scenarioForRequest([]message{
		{Role: "user", Content: "please [review] this PR diff ..."},
	})
	if name != "review" {
		t.Fatalf("scenario=%q want review", name)
	}
	if sc.ToolName != "mcp__review__submit_review" {
		t.Fatalf("review scenario must call submit_review; tool=%q", sc.ToolName)
	}

	// Array-of-parts content with the marker → review scenario.
	name2, _ := scenarioForRequest([]message{
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "context"},
			map[string]any{"type": "text", "text": "do a [review] now"},
		}},
	})
	if name2 != "review" {
		t.Fatalf("array content scenario=%q want review", name2)
	}

	// No marker → env default (write_file).
	name3, _ := scenarioForRequest([]message{
		{Role: "user", Content: "create a file HELLO.txt"},
	})
	if name3 != "write_file" {
		t.Fatalf("scenario=%q want write_file (no marker)", name3)
	}
}

// TestReviewScenarioTwoTurns proves the review scenario calls submit_review on
// turn 1 and finishes on turn 2 with structured findings + a completion receipt.
func TestReviewScenarioTwoTurns(t *testing.T) {
	msgsTurn1 := []message{{Role: "user", Content: "[review] the diff"}}
	_, sc := scenarioForRequest(msgsTurn1)
	if hasToolResult(msgsTurn1) {
		t.Fatal("turn 1 must not see a tool result")
	}
	if sc.ToolName != "mcp__review__submit_review" {
		t.Fatalf("turn 1 must call submit_review; got tool=%q args=%q", sc.ToolName, sc.ToolArgs)
	}
	if !strings.Contains(sc.ToolArgs, `"findings"`) || !strings.Contains(sc.ToolArgs, `"confidence"`) || !strings.Contains(sc.ToolArgs, `"completion"`) {
		t.Fatalf("review body must carry structured findings; args=%q", sc.ToolArgs)
	}

	msgsTurn2 := append(msgsTurn1, message{Role: "tool", ToolCallID: "call_mock_1", Content: "ok"})
	if !hasToolResult(msgsTurn2) {
		t.Fatal("turn 2 must observe the tool result and finish")
	}
}

func TestReviewScenarioRetriesOnceAfterRejectedSubmission(t *testing.T) {
	t.Setenv("MOCK_REVIEW_INVALID_FIRST", "1")
	msgs := []message{{Role: "user", Content: "[review] the diff"}}
	_, sc := scenarioForRequest(msgs)
	if strings.Contains(sc.ToolArgs, `"completion"`) {
		t.Fatalf("first review submission unexpectedly has completion: %s", sc.ToolArgs)
	}
	if !strings.Contains(sc.RetryToolArgs, `"completion"`) {
		t.Fatalf("corrected review submission lacks completion: %s", sc.RetryToolArgs)
	}
	if args, done := scenarioStep(sc, 0); done || args != sc.ToolArgs {
		t.Fatalf("step 0 = (%q,%v), want initial tool call", args, done)
	}
	if args, done := scenarioStep(sc, 1); done || args != sc.RetryToolArgs {
		t.Fatalf("step 1 = (%q,%v), want corrected tool call", args, done)
	}
	if args, done := scenarioStep(sc, 2); !done || args != "" {
		t.Fatalf("step 2 = (%q,%v), want final response", args, done)
	}
}

// Different prompts must yield different write_file contents (M7 live find:
// identical mock output on a branch that already had the file → empty diff →
// no_changes with no push, so the update-push flow would never advance).
func TestWriteFilePersonalisedByPrompt(t *testing.T) {
	msgs := func(prompt string) []message {
		return []message{{Role: "user", Content: prompt}}
	}
	_, a := scenarioForRequest(msgs("Add a CONTRIBUTING.md"))
	_, b := scenarioForRequest(msgs("Fix the flaky test"))
	if a.ToolArgs == b.ToolArgs {
		t.Fatalf("expected distinct ToolArgs for distinct prompts, both = %s", a.ToolArgs)
	}
	if !strings.Contains(a.ToolArgs, "JCODE_TASK_") {
		t.Fatalf("fingerprinted path missing from args: %s", a.ToolArgs)
	}
	// review marker still wins
	name, _ := scenarioForRequest(msgs("please [review] this"))
	if name != "review" {
		t.Fatalf("review marker lost: got %s", name)
	}
}
