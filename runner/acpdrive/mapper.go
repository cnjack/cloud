package main

// mapper.go — translate ACP session/update notifications into orchestrator run
// events. jcode drives these notifications from internal/handler/acp.go:
//
//   - AgentMessageChunk  → agent.text {text}
//   - SessionUpdateToolCall (initial) → agent.tool_call {name, args, call_id, ...}
//   - SessionToolCallUpdate with a TERMINAL status (completed/failed) →
//     agent.tool_result {call_id, name, output, is_error}
//
// Intermediate ToolCallUpdate notifications (status pending/in_progress, no
// output) are ignored: emitting a result for them would produce spurious empty
// results in the console. We only emit a result once the tool reaches a terminal
// status, carrying the RawOutput / textual content jcode attaches at that point.

import (
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// mapSessionUpdate converts one ACP SessionUpdate into zero or more run events
// and emits them. Returns the number of events emitted (for tests/logging).
func mapSessionUpdate(e *Emitter, u acp.SessionUpdate) int {
	switch {
	case u.AgentMessageChunk != nil:
		if txt := blockText(u.AgentMessageChunk.Content); txt != "" {
			e.EmitText(txt)
			return 1
		}

	case u.ToolCall != nil:
		tc := u.ToolCall
		payload := map[string]any{
			"name":    toolName(tc.Kind, tc.Title),
			"call_id": string(tc.ToolCallId),
			"title":   tc.Title,
			"kind":    string(tc.Kind),
			"status":  string(tc.Status),
		}
		if tc.RawInput != nil {
			payload["args"] = tc.RawInput
		}
		if locs := locations(tc.Locations); len(locs) > 0 {
			payload["locations"] = locs
		}
		e.Emit(eventAgentToolCall, payload)
		return 1

	case u.ToolCallUpdate != nil:
		up := u.ToolCallUpdate
		// Only terminal updates carry a result worth surfacing.
		if up.Status == nil || !terminalToolStatus(*up.Status) {
			return 0
		}
		payload := map[string]any{
			"call_id":  string(up.ToolCallId),
			"is_error": *up.Status == acp.ToolCallStatusFailed,
			"status":   string(*up.Status),
		}
		if up.Title != nil {
			payload["name"] = *up.Title
		} else if up.Kind != nil {
			payload["name"] = string(*up.Kind)
		}
		if out := toolOutput(up.RawOutput, up.Content); out != "" {
			payload["output"] = out
		}
		e.Emit(eventAgentToolResult, payload)
		return 1
	}
	return 0
}

// toolName picks a stable machine-ish tool name for the console. jcode's
// notification does not carry the raw eino tool name, so we use the ACP Kind
// (read/edit/execute/…) which maps 1:1 to the contract's example ("edit"),
// falling back to the human Title when Kind is empty/other.
func toolName(kind acp.ToolKind, title string) string {
	if kind != "" && kind != acp.ToolKindOther {
		return string(kind)
	}
	// Title is like "Write path" / "Read path"; take the first word lowercased.
	if title != "" {
		if i := strings.IndexByte(title, ' '); i > 0 {
			return strings.ToLower(title[:i])
		}
		return strings.ToLower(title)
	}
	return string(kind)
}

func terminalToolStatus(s acp.ToolCallStatus) bool {
	return s == acp.ToolCallStatusCompleted || s == acp.ToolCallStatusFailed
}

// blockText extracts plain text from a ContentBlock (only text blocks carry it).
func blockText(b acp.ContentBlock) string {
	if b.Text != nil {
		return strings.TrimRight(b.Text.Text, "\n")
	}
	return ""
}

// toolOutput derives a human-readable result string. Prefer the structured
// RawOutput (jcode sets it to the tool's text output); otherwise stitch together
// any textual/diff content blocks.
func toolOutput(raw any, content []acp.ToolCallContent) string {
	if s, ok := raw.(string); ok && s != "" {
		return s
	}
	var parts []string
	for _, c := range content {
		switch {
		case c.Content != nil:
			if t := blockText(c.Content.Content); t != "" {
				parts = append(parts, t)
			}
		case c.Diff != nil:
			parts = append(parts, "--- "+c.Diff.Path+" (diff)")
		}
	}
	return strings.Join(parts, "\n")
}

// locations flattens ToolCallLocation entries to plain path strings.
func locations(locs []acp.ToolCallLocation) []string {
	if len(locs) == 0 {
		return nil
	}
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		if l.Path != "" {
			out = append(out, l.Path)
		}
	}
	return out
}
