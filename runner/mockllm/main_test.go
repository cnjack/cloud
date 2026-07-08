package main

import (
	"strings"
	"testing"
)

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
	if !strings.Contains(sc.ToolArgs, "REVIEW.md") {
		t.Fatalf("review scenario must write REVIEW.md; args=%q", sc.ToolArgs)
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

// TestReviewScenarioTwoTurns proves the review scenario writes REVIEW.md on turn
// 1 (tool call) and finishes on turn 2 (after a tool result), and that the fixed
// review markdown carries a conclusion.
func TestReviewScenarioTwoTurns(t *testing.T) {
	msgsTurn1 := []message{{Role: "user", Content: "[review] the diff"}}
	_, sc := scenarioForRequest(msgsTurn1)
	if hasToolResult(msgsTurn1) {
		t.Fatal("turn 1 must not see a tool result")
	}
	if sc.ToolName != "write" || !strings.Contains(sc.ToolArgs, "REVIEW.md") {
		t.Fatalf("turn 1 must write REVIEW.md; got tool=%q args=%q", sc.ToolName, sc.ToolArgs)
	}
	// The review body must lead with a conclusion (approve|needs-work).
	if !strings.Contains(sc.ToolArgs, "needs-work") && !strings.Contains(sc.ToolArgs, "approve") {
		t.Fatalf("review body must state a conclusion; args=%q", sc.ToolArgs)
	}

	msgsTurn2 := append(msgsTurn1, message{Role: "tool", ToolCallID: "call_mock_1", Content: "ok"})
	if !hasToolResult(msgsTurn2) {
		t.Fatal("turn 2 must observe the tool result and finish")
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
