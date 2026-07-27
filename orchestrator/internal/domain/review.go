package domain

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const MaxReviewFindings = 8

// ReviewResult is the provider-neutral, validated output of a review Run.
// Provider-specific Markdown is derived only after this structure crosses the
// validation boundary.
type ReviewResult struct {
	Summary  string          `json:"summary"`
	Findings []ReviewFinding `json:"findings"`
	Checks   []string        `json:"checks,omitempty"`
}

type ReviewFinding struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	EndLine    int    `json:"end_line,omitempty"`
	Severity   string `json:"severity"`
	Confidence int    `json:"confidence"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion,omitempty"`
}

func (r ReviewResult) Validate() error {
	r.Summary = strings.TrimSpace(r.Summary)
	if r.Summary == "" || len(r.Summary) > 2_000 {
		return errors.New("review summary must be between 1 and 2000 bytes")
	}
	if len(r.Findings) > MaxReviewFindings {
		return fmt.Errorf("review has more than %d findings", MaxReviewFindings)
	}
	if len(r.Checks) > 12 {
		return errors.New("review has more than 12 checks")
	}
	seen := make(map[string]bool, len(r.Findings))
	for i := range r.Findings {
		f := &r.Findings[i]
		f.Path = strings.TrimSpace(f.Path)
		f.Title = strings.TrimSpace(f.Title)
		f.Body = strings.TrimSpace(f.Body)
		if !safeReviewPath(f.Path) {
			return fmt.Errorf("finding %d has an unsafe path", i+1)
		}
		if f.Line < 1 || f.EndLine < 0 || (f.EndLine > 0 && f.EndLine < f.Line) {
			return fmt.Errorf("finding %d has an invalid line range", i+1)
		}
		switch f.Severity {
		case "P0", "P1", "P2", "P3":
		default:
			return fmt.Errorf("finding %d has an invalid severity", i+1)
		}
		if f.Confidence < 80 || f.Confidence > 100 {
			return fmt.Errorf("finding %d confidence must be between 80 and 100", i+1)
		}
		if f.Title == "" || len(f.Title) > 160 || f.Body == "" || len(f.Body) > 4_000 || len(f.Suggestion) > 4_000 {
			return fmt.Errorf("finding %d has invalid field lengths", i+1)
		}
		key := fmt.Sprintf("%s:%d:%d", f.Path, f.Line, f.EndLine)
		if seen[key] {
			return fmt.Errorf("finding %d duplicates an existing anchor", i+1)
		}
		seen[key] = true
	}
	for i, check := range r.Checks {
		if strings.TrimSpace(check) == "" || len(check) > 240 {
			return fmt.Errorf("check %d has invalid length", i+1)
		}
	}
	return nil
}

func safeReviewPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// RenderSummary produces the single top-level review body. includeFindings is
// used only for the fail-visible fallback when a provider rejects inline
// anchors; normal GitHub reviews keep findings inline.
func (r ReviewResult) RenderSummary(includeFindings bool) string {
	count := len(r.Findings)
	findingWord := "findings"
	if count == 1 {
		findingWord = "finding"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## jcode review\n\n%s\n\n**%d %s**", strings.TrimSpace(r.Summary), count, findingWord)
	if includeFindings && count > 0 {
		b.WriteString("\n\n> GitHub inline placement was unavailable; locations are listed below.\n")
		findings := append([]ReviewFinding(nil), r.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			if findings[i].Path == findings[j].Path {
				return findings[i].Line < findings[j].Line
			}
			return findings[i].Path < findings[j].Path
		})
		for _, finding := range findings {
			fmt.Fprintf(&b, "\n- **%s · %s** at `%s:%d` (%d%% confidence): %s",
				finding.Severity, finding.Title, finding.Path, finding.Line, finding.Confidence, finding.Body)
		}
	}
	if len(r.Checks) > 0 {
		b.WriteString("\n\n<details><summary>Checks performed</summary>\n\n")
		for _, check := range r.Checks {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(check))
		}
		b.WriteString("\n</details>")
	}
	b.WriteString("\n\n<sub>jcode posts a non-blocking COMMENT review. Merge decisions remain with your team.</sub>")
	return b.String()
}

func (f ReviewFinding) RenderInline() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s · %s**\n\n%s\n\n_%d%% confidence_", f.Severity, f.Title, f.Body, f.Confidence)
	if strings.TrimSpace(f.Suggestion) != "" {
		fmt.Fprintf(&b, "\n\n```suggestion\n%s\n```", strings.TrimSuffix(f.Suggestion, "\n"))
	}
	return b.String()
}
