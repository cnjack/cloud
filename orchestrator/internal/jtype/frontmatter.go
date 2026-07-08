// Package jtype is a thin client for the jtype document REST API, used by the
// kanban poller (Feature E) to list cards, dispatch runs on a trigger column,
// and write run results back as card comments / status moves.
//
// The card model (jtype v2): a board is a `.board` JSON document; a card is a
// `.md` document whose YAML-ish frontmatter carries board / status / title /
// priority / assignee / due. "Move a card to column X" = rewrite the frontmatter
// `status` to X and save. This file holds the frontmatter helpers; client.go
// holds the HTTP surface.
package jtype

import (
	"strings"
)

// Card carries the frontmatter fields the poller/writeback care about, plus the
// markdown body that follows the frontmatter. Empty Board means "not a card".
type Card struct {
	Board  string
	Status string
	Title  string
	Body   string
}

// frontmatterDelimiter is the YAML-ish fence that opens and closes the block.
const frontmatterDelimiter = "---"

// ParseCard extracts the card frontmatter (board/status/title) and the body
// from a `.md` document. A document without a leading `---` fence yields an
// empty Card (Body = content) — i.e. "not a card". Frontmatter values may be
// bare or single/double-quoted; surrounding quotes are stripped.
func ParseCard(content string) Card {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontmatterDelimiter {
		// No frontmatter — not a card. Keep the content as body for callers that
		// still want it (we never call ParseCard on non-cards in practice).
		return Card{Body: content}
	}
	var fm []string
	bodyStart := len(lines)
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterDelimiter {
			fm = lines[1:i]
			bodyStart = i + 1
			break
		}
	}
	if fm == nil {
		// Opening fence but no closing fence — malformed; treat as not-a-card.
		return Card{Body: content}
	}
	c := Card{Body: strings.Join(lines[bodyStart:], "\n")}
	for _, raw := range fm {
		key, val, ok := splitKV(raw)
		if !ok {
			continue
		}
		val = unquote(val)
		switch key {
		case "board":
			c.Board = val
		case "status":
			c.Status = val
		case "title":
			c.Title = val
		}
	}
	return c
}

// SetStatus rewrites the frontmatter `status` value to newStatus, preserving
// every other byte of the document. It is used by MoveCard to change a card's
// column. Cases:
//   - A `status:` line exists in the frontmatter: its value is replaced.
//   - Frontmatter but no `status:` line: a `status: <new>` line is inserted
//     right after the opening fence.
//   - No frontmatter at all: a minimal fenced block with the new status is
//     prepended (defensive — real cards always have frontmatter).
func SetStatus(content, newStatus string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontmatterDelimiter {
		// No frontmatter: prepend a minimal block.
		block := "---\nstatus: " + newStatus + "\n---\n"
		return block + content
	}
	// Find the closing fence and any existing status line within the block.
	closeIdx := -1
	statusIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterDelimiter {
			closeIdx = i
			break
		}
		if statusIdx == -1 {
			if key, _, ok := splitKV(lines[i]); ok && key == "status" {
				statusIdx = i
			}
		}
	}
	if closeIdx == -1 {
		// Malformed (no closing fence): fall back to prepend.
		block := "---\nstatus: " + newStatus + "\n---\n"
		return block + content
	}
	if statusIdx >= 0 {
		lines[statusIdx] = setKVValue(lines[statusIdx], newStatus)
		return strings.Join(lines, "\n")
	}
	// No status line: insert one immediately after the opening fence.
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0])
	out = append(out, "status: "+newStatus)
	out = append(out, lines[1:]...)
	return strings.Join(out, "\n")
}

// splitKV splits a "key: value" frontmatter line. Returns ok=false for blank
// lines, comments, or lines without a colon. The key is trimmed/lowercased;
// the value keeps its raw form (unquoting happens in the caller).
func splitKV(line string) (key, val string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	idx := strings.Index(t, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(t[:idx]))
	val = strings.TrimSpace(t[idx+1:])
	// An inline comment after the value (`status: ai  # do the thing`) is stripped.
	if h := strings.Index(val, "#"); h >= 0 {
		val = strings.TrimSpace(val[:h])
	}
	return key, val, true
}

// setKVValue replaces the value of a "key: old" line with new, preserving the
// original key and any leading indent. The value is emitted bare.
func setKVValue(line, newV string) string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return line
	}
	indent := line[:idx]
	return indent + ": " + newV
}

// unquote strips a single pair of surrounding single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
