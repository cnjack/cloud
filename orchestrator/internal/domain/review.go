package domain

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	MaxReviewFindings          = 8
	MaxReviewCompletionReasons = 8

	// Keep the composed review comfortably below common provider comment limits.
	// Individual budgets preserve every finding's identity while bounding escaped
	// model text, whose entities can be much larger than the validated input.
	maxRenderedReviewBytes       = 60_000
	maxRenderedSummaryTextBytes  = 4_000
	maxRenderedFindingTitleBytes = 512
	maxRenderedFindingPathBytes  = 1_024
	maxRenderedFindingBodyBytes  = 4_000
	maxRenderedReviewCheckBytes  = 512
	reviewTextTruncationMarker   = "…"
	reviewTextTruncationNotice   = "> Some review text was truncated to fit provider comment limits. The complete validated result remains available in jcode Cloud."
	inlineTextTruncationNotice   = "> Some finding text was truncated to fit provider comment limits. The complete validated result remains available in jcode Cloud."
)

// ReviewResult is the provider-neutral, validated output of a review Run.
// Provider-specific Markdown is derived only after this structure crosses the
// validation boundary.
type ReviewResult struct {
	Summary    string            `json:"summary"`
	Findings   []ReviewFinding   `json:"findings"`
	Checks     []string          `json:"checks,omitempty"`
	Completion *ReviewCompletion `json:"completion,omitempty"`
}

// ReviewCompletion is the reviewer's bounded execution receipt. It is kept
// separate from ReviewPlan input coverage: the Plan proves what entered the
// review, while this receipt records which indexed files the reviewer claims
// to have inspected and why it stopped. NormalizeAgainst independently checks
// the receipt against the server-owned Plan before the result is persisted.
type ReviewCompletion struct {
	Status        ReviewCompletionStatus   `json:"status"`
	ReviewedFiles []string                 `json:"reviewed_files"`
	Reasons       []ReviewIncompleteReason `json:"reasons,omitempty"`
}

type ReviewCompletionStatus string

const (
	ReviewCompletionComplete ReviewCompletionStatus = "complete"
	ReviewCompletionPartial  ReviewCompletionStatus = "partial"
	ReviewCompletionFailed   ReviewCompletionStatus = "failed"
)

func (s ReviewCompletionStatus) Valid() bool {
	switch s {
	case ReviewCompletionComplete, ReviewCompletionPartial, ReviewCompletionFailed:
		return true
	}
	return false
}

type ReviewIncompleteReason string

const (
	ReviewReasonCompletionUnreported   ReviewIncompleteReason = "completion_unreported"
	ReviewReasonInputCoveragePartial   ReviewIncompleteReason = "input_coverage_partial"
	ReviewReasonFilesUnreviewed        ReviewIncompleteReason = "files_unreviewed"
	ReviewReasonBudgetExhausted        ReviewIncompleteReason = "budget_exhausted"
	ReviewReasonModelError             ReviewIncompleteReason = "model_error"
	ReviewReasonVerificationIncomplete ReviewIncompleteReason = "verification_incomplete"
	ReviewReasonReviewerIncomplete     ReviewIncompleteReason = "reviewer_incomplete"
)

func (r ReviewIncompleteReason) Valid() bool {
	switch r {
	case ReviewReasonCompletionUnreported, ReviewReasonInputCoveragePartial,
		ReviewReasonFilesUnreviewed, ReviewReasonBudgetExhausted,
		ReviewReasonModelError, ReviewReasonVerificationIncomplete,
		ReviewReasonReviewerIncomplete:
		return true
	}
	return false
}

type ReviewFinding struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	EndLine    int    `json:"end_line,omitempty"`
	Severity   string `json:"severity"`
	Confidence int    `json:"confidence"`
	Title      string `json:"title"`
	Body       string `json:"body" jsonschema_description:"Required. Write compact GitHub-flavored Markdown. Put Impact, evidence, and suggested fix in separate short paragraphs with a blank line between paragraphs. Put code identifiers, file paths, commands, and API routes in Markdown inline code. Do not add backslashes before Markdown punctuation."`
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
	if err := validateReviewCompletion(r.Completion); err != nil {
		return err
	}
	return nil
}

func validateReviewCompletion(completion *ReviewCompletion) error {
	if completion == nil {
		return nil
	}
	if !completion.Status.Valid() {
		return fmt.Errorf("review completion has invalid status %q", completion.Status)
	}
	if len(completion.ReviewedFiles) > MaxReviewFiles {
		return fmt.Errorf("review completion has more than %d reviewed files", MaxReviewFiles)
	}
	seenFiles := make(map[string]bool, len(completion.ReviewedFiles))
	for i, value := range completion.ReviewedFiles {
		if !safeReviewPath(value) {
			return fmt.Errorf("review completion file %d has an unsafe path", i+1)
		}
		if seenFiles[value] {
			return fmt.Errorf("review completion file %d duplicates an existing path", i+1)
		}
		seenFiles[value] = true
	}
	if len(completion.Reasons) > MaxReviewCompletionReasons {
		return fmt.Errorf("review completion has more than %d reasons", MaxReviewCompletionReasons)
	}
	seenReasons := make(map[ReviewIncompleteReason]bool, len(completion.Reasons))
	for i, reason := range completion.Reasons {
		if !reason.Valid() {
			return fmt.Errorf("review completion reason %d is invalid", i+1)
		}
		if seenReasons[reason] {
			return fmt.Errorf("review completion reason %d is duplicated", i+1)
		}
		seenReasons[reason] = true
	}
	if completion.Status == ReviewCompletionComplete && len(completion.Reasons) > 0 {
		return errors.New("complete review completion cannot include incomplete reasons")
	}
	if completion.Status != ReviewCompletionComplete && len(completion.Reasons) == 0 {
		return errors.New("incomplete review completion requires at least one reason")
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
// used for providers that cannot place findings inline. The output deliberately
// sticks to portable Markdown; GitHub reviews use RenderGitHubSummary instead.
func (r ReviewResult) RenderSummary(includeFindings bool) string {
	return r.renderSummary(includeFindings, false)
}

// RenderGitHubSummary enhances the portable summary with GitHub alert syntax.
// includeFindings is used only for the fail-visible fallback when GitHub rejects
// inline anchors; normal GitHub reviews keep findings inline.
func (r ReviewResult) RenderGitHubSummary(includeFindings bool) string {
	return r.renderSummary(includeFindings, true)
}

func (r ReviewResult) renderSummary(includeFindings, githubAlerts bool) string {
	count := len(r.Findings)
	complete := r.Completion != nil && r.Completion.Status == ReviewCompletionComplete
	findingHeading := reviewFindingHeading(count, complete)
	truncated := false
	var b strings.Builder
	if githubAlerts {
		alertType := "NOTE"
		if !complete {
			alertType = "WARNING"
		} else if includeFindings && count > 0 {
			alertType = "WARNING"
		} else if count > 0 {
			alertType = "IMPORTANT"
		}
		fmt.Fprintf(&b, "> [!%s]\n> ## %s\n>\n", alertType, findingHeading)
		switch {
		case !complete && count == 0:
			b.WriteString("> No findings were confirmed before review stopped. This is not a clean result.")
		case !complete:
			b.WriteString("> Confirmed findings are available, but review did not finish. This is not a clean result.")
		case includeFindings && count > 0:
			b.WriteString("> Inline comments could not be placed on the diff. Review the locations below.")
		case count == 0:
			b.WriteString("> No findings met the configured confidence threshold.")
		case count == 1:
			b.WriteString("> Review the inline comment on the changed line.")
		default:
			b.WriteString("> Review the inline comments on the changed lines.")
		}
	} else {
		fmt.Fprintf(&b, "## jcode review\n\n### %s", findingHeading)
		if includeFindings && count > 0 {
			b.WriteString("\n\n> Inline comments are unavailable for this provider. Review the locations below.")
		}
	}
	b.WriteString("\n\n### Summary\n\n")
	summary, summaryTruncated := renderReviewMarkdownSubsetBounded(strings.TrimSpace(r.Summary), maxRenderedSummaryTextBytes)
	truncated = truncated || summaryTruncated
	b.WriteString(summary)
	if !complete {
		b.WriteString("\n\n### Incomplete review\n\n")
		b.WriteString("This review did not reach a clean conclusion")
		if reasons := r.renderCompletionReasons(); reasons != "" {
			b.WriteString(": ")
			b.WriteString(reasons)
		}
		b.WriteString(".")
	}
	if includeFindings && count > 0 {
		b.WriteString("\n\n### Findings")
		findings := append([]ReviewFinding(nil), r.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			if findings[i].Path == findings[j].Path {
				return findings[i].Line < findings[j].Line
			}
			return findings[i].Path < findings[j].Path
		})
		for _, finding := range findings {
			title, titleTruncated := escapeReviewMarkdownBounded(reviewSingleLine(finding.Title), maxRenderedFindingTitleBytes)
			location, locationTruncated := escapeReviewHTMLBounded(
				fmt.Sprintf("%s:%d", finding.Path, finding.Line), maxRenderedFindingPathBytes,
			)
			body, bodyTruncated := renderReviewMarkdownSubsetBounded(strings.TrimSpace(finding.Body), maxRenderedFindingBodyBytes)
			truncated = truncated || titleTruncated || locationTruncated || bodyTruncated
			fmt.Fprintf(&b, "\n\n#### %s · %s\n\n<code>%s</code> · %d%% confidence\n\n%s",
				finding.Severity, title, location, finding.Confidence, body)
		}
	}
	if len(r.Checks) > 0 {
		fmt.Fprintf(&b, "\n\n<details>\n<summary>🔍 <strong>Checks performed</strong> · %d</summary>\n\n", len(r.Checks))
		for _, check := range r.Checks {
			value, valueTruncated := escapeReviewMarkdownBounded(reviewSingleLine(check), maxRenderedReviewCheckBytes)
			truncated = truncated || valueTruncated
			fmt.Fprintf(&b, "- %s\n", value)
		}
		b.WriteString("\n</details>")
	}
	if truncated {
		b.WriteString("\n\n" + reviewTextTruncationNotice)
	}
	b.WriteString("\n\n---\n\n<sub>jcode posts a non-blocking COMMENT review. Merge decisions remain with your team.</sub>")
	return b.String()
}

func reviewFindingHeading(count int, complete bool) string {
	if !complete {
		return "Review incomplete"
	}
	switch count {
	case 0:
		return "No high-confidence findings"
	case 1:
		return "1 validated finding"
	default:
		return fmt.Sprintf("%d validated findings", count)
	}
}

func (r ReviewResult) renderCompletionReasons() string {
	reasons := []ReviewIncompleteReason{ReviewReasonCompletionUnreported}
	if r.Completion != nil && len(r.Completion.Reasons) > 0 {
		reasons = r.Completion.Reasons
	}
	labels := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		switch reason {
		case ReviewReasonCompletionUnreported:
			labels = append(labels, "completion was not reported")
		case ReviewReasonInputCoveragePartial:
			labels = append(labels, "input coverage was partial")
		case ReviewReasonFilesUnreviewed:
			labels = append(labels, "one or more indexed files were not reviewed")
		case ReviewReasonBudgetExhausted:
			labels = append(labels, "budget exhausted")
		case ReviewReasonModelError:
			labels = append(labels, "the model failed")
		case ReviewReasonVerificationIncomplete:
			labels = append(labels, "candidate verification was incomplete")
		case ReviewReasonReviewerIncomplete:
			labels = append(labels, "the reviewer stopped before completion")
		}
	}
	return strings.Join(labels, "; ")
}

func reviewSingleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func escapeReviewMarkdownBounded(value string, limit int) (string, bool) {
	return encodeReviewTextBounded(value, limit, escapedReviewMarkdownRune)
}

func escapeReviewHTMLBounded(value string, limit int) (string, bool) {
	return encodeReviewTextBounded(value, limit, escapedReviewHTMLRune)
}

// renderReviewMarkdownSubsetBounded keeps review prose readable without
// treating model output as arbitrary Markdown. It permits only paragraph
// breaks, top-level unordered-list markers, and single-backtick inline code.
// Everything else still crosses the same escaping and mention-neutralization
// boundary used by plain review text.
func renderReviewMarkdownSubsetBounded(value string, limit int) (string, bool) {
	runes := []rune(normalizeReviewText(value))
	encoded := boundedReviewText{limit: limit}
	atLineStart := true
	for i := 0; i < len(runes); {
		if atLineStart && runes[i] == '-' && i+1 < len(runes) && runes[i+1] == ' ' {
			if !encoded.append("- ") {
				break
			}
			i += 2
			atLineStart = false
			continue
		}
		if runes[i] == '`' && (i == 0 || runes[i-1] != '`') {
			end := i + 1
			for end < len(runes) && runes[end] != '`' && runes[end] != '\n' {
				end++
			}
			if end > i+1 && end < len(runes) && runes[end] == '`' &&
				(end+1 == len(runes) || runes[end+1] != '`') {
				var code strings.Builder
				for _, r := range runes[i+1 : end] {
					code.WriteString(escapedReviewInlineCodeRune(r))
				}
				if !encoded.append("`" + code.String() + "`") {
					break
				}
				i = end + 1
				atLineStart = false
				continue
			}
		}
		if !encoded.append(escapedReviewMarkdownRune(runes[i])) {
			break
		}
		atLineStart = runes[i] == '\n'
		i++
	}
	return encoded.result()
}

func normalizeReviewText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func encodeReviewTextBounded(value string, limit int, encode func(rune) string) (string, bool) {
	encoded := boundedReviewText{limit: limit}
	for _, r := range normalizeReviewText(value) {
		if !encoded.append(encode(r)) {
			break
		}
	}
	return encoded.result()
}

type boundedReviewText struct {
	segments  []string
	size      int
	limit     int
	truncated bool
}

func (b *boundedReviewText) append(segment string) bool {
	if b.size+len(segment) > b.limit {
		b.truncated = true
		return false
	}
	b.segments = append(b.segments, segment)
	b.size += len(segment)
	return true
}

func (b *boundedReviewText) result() (string, bool) {
	if b.truncated {
		for len(b.segments) > 0 && b.size+len(reviewTextTruncationMarker) > b.limit {
			b.size -= len(b.segments[len(b.segments)-1])
			b.segments = b.segments[:len(b.segments)-1]
		}
	}
	var result strings.Builder
	result.Grow(b.size + len(reviewTextTruncationMarker))
	for _, segment := range b.segments {
		result.WriteString(segment)
	}
	if b.truncated {
		result.WriteString(reviewTextTruncationMarker)
	}
	return result.String(), b.truncated
}

func escapedReviewMarkdownRune(r rune) string {
	const punctuation = "!\"#$%'()*+,-./:;=?[\\]^_`{|}~"
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '@':
		// Add a zero-width separator after the encoded marker so GitHub does
		// not turn model text into a user or team notification.
		return "&#64;&#8203;"
	default:
		if strings.ContainsRune(punctuation, r) {
			return "\\" + string(r)
		}
		return string(r)
	}
}

func escapedReviewHTMLRune(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '"':
		return "&#34;"
	case '\'':
		return "&#39;"
	default:
		return string(r)
	}
}

func escapedReviewInlineCodeRune(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '@':
		return "&#64;&#8203;"
	default:
		return string(r)
	}
}

func reviewSuggestionFence(value string) string {
	longest, current := 0, 0
	for _, r := range value {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	length := longest + 1
	if length < 3 {
		length = 3
	}
	return strings.Repeat("`", length)
}

func (f ReviewFinding) RenderInline() string {
	title, titleTruncated := escapeReviewMarkdownBounded(reviewSingleLine(f.Title), maxRenderedFindingTitleBytes)
	body, bodyTruncated := renderReviewMarkdownSubsetBounded(strings.TrimSpace(f.Body), maxRenderedFindingBodyBytes)
	var b strings.Builder
	fmt.Fprintf(&b, "**%s · %s**\n\n%s\n\n_%d%% confidence_", f.Severity, title, body, f.Confidence)
	if titleTruncated || bodyTruncated {
		b.WriteString("\n\n" + inlineTextTruncationNotice)
	}
	if strings.TrimSpace(f.Suggestion) != "" {
		suggestion := strings.TrimSuffix(f.Suggestion, "\n")
		fence := reviewSuggestionFence(suggestion)
		fmt.Fprintf(&b, "\n\n%ssuggestion\n%s\n%s", fence, suggestion, fence)
	}
	return b.String()
}
