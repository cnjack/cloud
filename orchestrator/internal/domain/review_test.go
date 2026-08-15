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

func TestReviewResultRenderSummaryUsesGitHubFormatting(t *testing.T) {
	t.Run("clean review", func(t *testing.T) {
		result := ReviewResult{
			Summary:    "No high-confidence defects found.",
			Checks:     []string{"Inspected changed code"},
			Completion: &ReviewCompletion{Status: ReviewCompletionComplete},
		}
		want := `> [!NOTE]
> ## No high-confidence findings
>
> No findings met the configured confidence threshold.

### Summary

No high\-confidence defects found\.

<details>
<summary>🔍 <strong>Checks performed</strong> · 1</summary>

- Inspected changed code

</details>

---

<sub>jcode posts a non-blocking COMMENT review. Merge decisions remain with your team.</sub>`
		if got := result.RenderGitHubSummary(false); got != want {
			t.Fatalf("RenderGitHubSummary() mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
		}
	})

	t.Run("review with inline findings", func(t *testing.T) {
		result := ReviewResult{
			Summary:    "One defect found.",
			Completion: &ReviewCompletion{Status: ReviewCompletionComplete},
			Findings: []ReviewFinding{{
				Path: "ledger.py", Line: 7, Severity: "P1", Confidence: 99,
				Title: "Reversed guard", Body: "Valid transfers are rejected.",
			}},
		}
		body := result.RenderGitHubSummary(false)
		for _, want := range []string{"> [!IMPORTANT]", "> ## 1 validated finding", "> Review the inline comment on the changed line.", "### Summary\n\nOne defect found\\."} {
			if !strings.Contains(body, want) {
				t.Fatalf("formatted review missing %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "### Findings") {
			t.Fatalf("inline review should not duplicate findings in its summary:\n%s", body)
		}
	})

	t.Run("fallback findings", func(t *testing.T) {
		result := ReviewResult{
			Summary:    "Two defects found.",
			Completion: &ReviewCompletion{Status: ReviewCompletionComplete},
			Findings: []ReviewFinding{
				{Path: "z.go", Line: 12, Severity: "P2", Confidence: 91, Title: "Late issue", Body: "The late path fails."},
				{Path: "a.go", Line: 7, Severity: "P1", Confidence: 99, Title: "Reversed guard", Body: "Valid transfers are rejected."},
			},
		}
		body := result.RenderGitHubSummary(true)
		for _, want := range []string{
			"> [!WARNING]",
			"> ## 2 validated findings",
			"> Inline comments could not be placed on the diff. Review the locations below.",
			"### Summary\n\nTwo defects found\\.",
			"### Findings",
			"#### P1 · Reversed guard\n\n<code>a.go:7</code> · 99% confidence\n\nValid transfers are rejected\\.",
			"#### P2 · Late issue\n\n<code>z.go:12</code> · 91% confidence\n\nThe late path fails\\.",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("fallback review missing %q:\n%s", want, body)
			}
		}
		if strings.Index(body, "a.go:7") > strings.Index(body, "z.go:12") {
			t.Fatalf("fallback findings are not sorted by path:\n%s", body)
		}
	})

	t.Run("multiline summary stays in its own section", func(t *testing.T) {
		result := ReviewResult{Summary: "First paragraph.\n\nSecond paragraph."}
		body := result.RenderGitHubSummary(false)
		if !strings.Contains(body, "### Summary\n\nFirst paragraph\\.\n\nSecond paragraph\\.") {
			t.Fatalf("summary section is malformed:\n%s", body)
		}
		if strings.Contains(body, "> First paragraph.") {
			t.Fatalf("model summary should not turn the status alert into a text wall:\n%s", body)
		}
	})

	t.Run("readable markdown subset survives provider rendering", func(t *testing.T) {
		result := ReviewResult{
			Summary:    "The projection revives `config.CustomModels`.\n\n- Removing `/api/models` no longer sticks\n- `model_state` remains stale",
			Completion: &ReviewCompletion{Status: ReviewCompletionComplete},
			Findings: []ReviewFinding{{
				Path: "providers.go", Line: 1198, Severity: "P3", Confidence: 80,
				Title: "Removed model returns", Body: "Impact: the removed `ModelRef` returns to the picker.\n\nEvidence: `handleUpdateProvider` never prunes `EnabledModels`.\n\nSuggested fix: prune stale refs before projecting `/api/models`.",
			}},
		}
		for name, body := range map[string]string{
			"github summary":   result.RenderGitHubSummary(false),
			"portable summary": result.RenderSummary(true),
			"inline finding":   result.Findings[0].RenderInline(),
		} {
			if name != "inline finding" {
				for _, want := range []string{"`config.CustomModels`", "`/api/models`"} {
					if !strings.Contains(body, want) {
						t.Fatalf("%s escaped readable Markdown %q:\n%s", name, want, body)
					}
				}
			}
			if name != "inline finding" && !strings.Contains(body, "\n\n- Removing `/api/models`") {
				t.Fatalf("%s did not preserve a Markdown list:\n%s", name, body)
			}
			if name == "inline finding" {
				for _, want := range []string{
					"`ModelRef`", "\n\nEvidence\\: `handleUpdateProvider`", "\n\nSuggested fix\\:", "`/api/models`",
				} {
					if !strings.Contains(body, want) {
						t.Fatalf("inline finding did not preserve %q:\n%s", want, body)
					}
				}
			}
		}
	})

	t.Run("portable providers do not receive GitHub alert syntax", func(t *testing.T) {
		result := ReviewResult{Summary: "No high-confidence defects found.", Completion: &ReviewCompletion{Status: ReviewCompletionComplete}}
		body := result.RenderSummary(false)
		for _, want := range []string{"## jcode review", "### No high-confidence findings", "### Summary\n\nNo high\\-confidence defects found\\."} {
			if !strings.Contains(body, want) {
				t.Fatalf("portable review missing %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "[!NOTE]") || strings.Contains(body, "[!WARNING]") {
			t.Fatalf("portable review contains GitHub-only alert syntax:\n%s", body)
		}
	})

	t.Run("partial zero-finding result never renders as clean", func(t *testing.T) {
		result := ReviewResult{
			Summary: "No issue was confirmed before the review stopped.",
			Completion: &ReviewCompletion{
				Status: ReviewCompletionPartial, Reasons: []ReviewIncompleteReason{ReviewReasonBudgetExhausted},
			},
		}
		body := result.RenderGitHubSummary(false)
		for _, want := range []string{"Review incomplete", "not a clean result", "budget exhausted"} {
			if !strings.Contains(body, want) {
				t.Fatalf("partial review missing %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "No high-confidence findings") {
			t.Fatalf("partial review rendered a clean heading:\n%s", body)
		}
	})

	t.Run("structured text cannot escape the renderer", func(t *testing.T) {
		finding := ReviewFinding{
			Path: "docs/@scope/guide  two.md", Line: 7, Severity: "P1", Confidence: 99,
			Title:      "Bug\n\n> [!CAUTION]\n> Review failed",
			Body:       "</details>\n\n@org/team",
			Suggestion: "before\n```\nafter",
		}
		result := ReviewResult{
			Summary:  "Looks fine.\n\n> [!CAUTION]\n> Review failed @org/team & &#64;other/team\n\n`<script>@org/team</script>` [unsafe](javascript:alert(1))",
			Findings: []ReviewFinding{finding},
			Checks:   []string{"</details>\n\n@org/team"},
		}
		body := result.RenderGitHubSummary(true)
		if strings.Count(body, "[!") != 1 {
			t.Fatalf("structured text injected another GitHub alert:\n%s", body)
		}
		if strings.Contains(body, "@org/team") || strings.Contains(body, "@other/team") {
			t.Fatalf("structured text retained an active mention:\n%s", body)
		}
		if strings.Count(body, "</details>") != 1 {
			t.Fatalf("structured text escaped the checks disclosure:\n%s", body)
		}
		if !strings.Contains(body, "&#64;&#8203;org") || !strings.Contains(body, "&amp;\\#64\\;other") {
			t.Fatalf("mentions or entities were not neutralized:\n%s", body)
		}
		if strings.Contains(body, "<script>") || strings.Contains(body, "[unsafe](javascript:") {
			t.Fatalf("restricted Markdown enabled model-controlled HTML or links:\n%s", body)
		}
		if !strings.Contains(body, "<code>docs/@scope/guide  two.md:7</code>") {
			t.Fatalf("fallback location no longer preserves the exact path:\n%s", body)
		}

		inline := finding.RenderInline()
		if strings.Contains(inline, "[!CAUTION]") || strings.Contains(inline, "@org/team") {
			t.Fatalf("inline finding retained injected Markdown:\n%s", inline)
		}
		if !strings.Contains(inline, "````suggestion\nbefore\n```\nafter\n````") {
			t.Fatalf("suggestion fence can be closed by its content:\n%s", inline)
		}
	})
}

func TestReviewMarkdownSubsetTruncatesWithoutOpeningFormatting(t *testing.T) {
	value := strings.Repeat("a", maxRenderedSummaryTextBytes-5) + " `" + strings.Repeat("b", 100) + "`"
	rendered, truncated := renderReviewMarkdownSubsetBounded(value, maxRenderedSummaryTextBytes)
	if !truncated || len(rendered) > maxRenderedSummaryTextBytes {
		t.Fatalf("rendered bytes=%d truncated=%v", len(rendered), truncated)
	}
	if strings.Count(rendered, "`")%2 != 0 {
		t.Fatalf("truncation left an open inline-code span: %q", rendered)
	}

	unmatched, truncated := renderReviewMarkdownSubsetBounded("Unclosed `code", 100)
	if truncated || unmatched != "Unclosed \\`code" {
		t.Fatalf("unmatched inline-code marker was activated: %q truncated=%v", unmatched, truncated)
	}
}

func TestReviewResultRenderedOutputStaysWithinProviderLimit(t *testing.T) {
	result := ReviewResult{
		Summary: strings.Repeat("@", 2_000),
		Checks:  make([]string, 12),
	}
	for i := range result.Checks {
		result.Checks[i] = strings.Repeat("@", 240)
	}
	for i := 0; i < MaxReviewFindings; i++ {
		result.Findings = append(result.Findings, ReviewFinding{
			Path:       strings.Repeat("@", 4_000) + ".go",
			Line:       i + 1,
			Severity:   "P1",
			Confidence: 99,
			Title:      strings.Repeat("@", 160),
			Body:       strings.Repeat("@", 4_000),
		})
	}
	result.Findings[0].Suggestion = strings.Repeat("`", 4_000)
	if err := result.Validate(); err != nil {
		t.Fatalf("worst-case result should satisfy the structured limits: %v", err)
	}

	for name, body := range map[string]string{
		"github fallback": result.RenderGitHubSummary(true),
		"portable":        result.RenderSummary(true),
		"inline":          result.Findings[0].RenderInline(),
	} {
		if len(body) > maxRenderedReviewBytes {
			t.Fatalf("%s rendered %d bytes, limit %d", name, len(body), maxRenderedReviewBytes)
		}
		if !strings.Contains(body, "truncated") {
			t.Fatalf("%s did not disclose truncation", name)
		}
	}
}
