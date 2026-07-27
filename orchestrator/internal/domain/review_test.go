package domain

import (
	"strings"
	"testing"
)

func TestReviewResultValidation(t *testing.T) {
	valid := ReviewResult{
		Summary: "The balance guard is reversed.",
		Findings: []ReviewFinding{{
			Path: "ledger.py", Line: 7, Severity: "P1", Confidence: 99,
			Title: "Rejects valid transfers", Body: "The comparison is reversed.",
			Suggestion: "if amount > balance:",
		}},
		Checks: []string{"Inspected callers", "Ran unit tests"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ReviewResult){
		"low confidence": func(r *ReviewResult) { r.Findings[0].Confidence = 79 },
		"bad severity":   func(r *ReviewResult) { r.Findings[0].Severity = "major" },
		"unsafe path":    func(r *ReviewResult) { r.Findings[0].Path = "../secret" },
		"bad line":       func(r *ReviewResult) { r.Findings[0].Line = 0 },
		"empty title":    func(r *ReviewResult) { r.Findings[0].Title = "" },
	} {
		t.Run(name, func(t *testing.T) {
			got := valid
			got.Findings = append([]ReviewFinding(nil), valid.Findings...)
			mutate(&got)
			if err := got.Validate(); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	duplicate := valid
	duplicate.Findings = append(duplicate.Findings, valid.Findings[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate anchor accepted")
	}
	tooMany := valid
	for len(tooMany.Findings) < 9 {
		finding := valid.Findings[0]
		finding.Line += len(tooMany.Findings)
		tooMany.Findings = append(tooMany.Findings, finding)
	}
	if err := tooMany.Validate(); err == nil {
		t.Fatal("more than eight findings accepted")
	}
}

func TestReviewResultRenderIsExplicitForCleanAndFallback(t *testing.T) {
	clean := ReviewResult{Summary: "No high-confidence defects found.", Checks: []string{"Inspected changed code"}}
	body := clean.RenderSummary(false)
	if !strings.Contains(body, "No high-confidence") || !strings.Contains(body, "0 findings") {
		t.Fatalf("clean render=%q", body)
	}
	result := ReviewResult{
		Summary: "One defect found.",
		Findings: []ReviewFinding{{
			Path: "ledger.py", Line: 7, Severity: "P1", Confidence: 99,
			Title: "Reversed guard", Body: "Valid transfers are rejected.",
		}},
	}
	fallback := result.RenderSummary(true)
	for _, want := range []string{"1 finding", "`ledger.py:7`", "inline placement was unavailable"} {
		if !strings.Contains(fallback, want) {
			t.Fatalf("fallback render missing %q: %s", want, fallback)
		}
	}
}
