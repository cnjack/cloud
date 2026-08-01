package domain

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ReviewPlanSchemaVersion = 1
	MaxReviewDiffBytes      = 2 << 20
	MaxReviewFiles          = 400
	MaxReviewHunks          = 2_000
	ReviewCoverageComplete  = "complete"
	ReviewCoveragePartial   = "partial"
)

type ReviewPlanInput struct {
	BaseSHA      string    `json:"base_sha"`
	HeadSHA      string    `json:"head_sha"`
	MergeBaseSHA string    `json:"merge_base_sha"`
	Diff         string    `json:"diff"`
	CreatedAt    time.Time `json:"-"`
}

type ReviewPlan struct {
	SchemaVersion int              `json:"schema_version"`
	PlanHash      string           `json:"plan_hash"`
	BaseSHA       string           `json:"base_sha"`
	HeadSHA       string           `json:"head_sha"`
	MergeBaseSHA  string           `json:"merge_base_sha"`
	RulesRevision string           `json:"rules_revision"`
	Coverage      string           `json:"coverage"`
	ChangedFiles  int              `json:"changed_files"`
	EligibleFiles int              `json:"eligible_files"`
	IndexedFiles  int              `json:"indexed_files"`
	ChangedHunks  int              `json:"changed_hunks"`
	IndexedHunks  int              `json:"indexed_hunks"`
	ChangedLines  int              `json:"changed_lines"`
	Files         []ReviewPlanFile `json:"files"`
	Anchors       []ReviewAnchor   `json:"-"`
	CreatedAt     time.Time        `json:"created_at"`
}

type ReviewPlanFile struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Hunks        int    `json:"hunks"`
	ChangedLines int    `json:"changed_lines"`
}

type ReviewAnchor struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

var hunkHeader = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

func BuildReviewPlan(input ReviewPlanInput) (*ReviewPlan, error) {
	input.BaseSHA = strings.TrimSpace(input.BaseSHA)
	input.HeadSHA = strings.TrimSpace(input.HeadSHA)
	input.MergeBaseSHA = strings.TrimSpace(input.MergeBaseSHA)
	if !ValidCommitSHA(input.BaseSHA) || !ValidCommitSHA(input.HeadSHA) || !ValidCommitSHA(input.MergeBaseSHA) {
		return nil, errors.New("review_revision_invalid: base, head, and merge-base must be commit SHAs")
	}
	if len(input.Diff) > MaxReviewDiffBytes {
		return nil, fmt.Errorf("review_input_too_large: diff exceeds %d bytes", MaxReviewDiffBytes)
	}
	plan := &ReviewPlan{SchemaVersion: ReviewPlanSchemaVersion, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, MergeBaseSHA: input.MergeBaseSHA, RulesRevision: "review-v2", Coverage: ReviewCoverageComplete, CreatedAt: input.CreatedAt.UTC()}
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = time.Now().UTC()
	}
	type fileState struct {
		summary ReviewPlanFile
		lines   []int
		newLine int
		inHunk  bool
	}
	var current *fileState
	flush := func() error {
		if current == nil {
			return nil
		}
		if current.summary.Path == "" || !safeReviewPath(current.summary.Path) {
			current.summary.Status, current.summary.Reason = "skipped", "unsupported"
		}
		if current.summary.Status == "" {
			current.summary.Status = "indexed"
		}
		plan.ChangedFiles++
		if plan.ChangedFiles > MaxReviewFiles {
			return fmt.Errorf("review_input_too_large: more than %d changed files", MaxReviewFiles)
		}
		if current.summary.Status == "indexed" {
			plan.EligibleFiles++
			plan.IndexedFiles++
			plan.IndexedHunks += current.summary.Hunks
			plan.ChangedLines += len(current.lines)
			for _, a := range compressReviewLines(current.summary.Path, current.lines) {
				plan.Anchors = append(plan.Anchors, a)
			}
		} else {
			plan.Coverage = ReviewCoveragePartial
		}
		plan.ChangedHunks += current.summary.Hunks
		current.summary.ChangedLines = len(current.lines)
		plan.Files = append(plan.Files, current.summary)
		current = nil
		return nil
	}
	scanner := bufio.NewScanner(strings.NewReader(input.Diff))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, MaxReviewDiffBytes+1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git ") {
			if err := flush(); err != nil {
				return nil, err
			}
			parts := strings.SplitN(line, " b/", 2)
			path := ""
			if len(parts) == 2 {
				path = strings.Trim(parts[1], `"`)
			}
			current = &fileState{summary: ReviewPlanFile{Path: path}}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			value := strings.TrimPrefix(line, "+++ ")
			if value != "/dev/null" {
				current.summary.Path = strings.TrimPrefix(strings.Trim(value, `"`), "b/")
			}
			continue
		}
		if strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch") {
			current.summary.Status, current.summary.Reason = "skipped", "binary"
			current.inHunk = false
			continue
		}
		if strings.HasPrefix(line, "Submodule ") {
			current.summary.Status, current.summary.Reason = "skipped", "unsupported"
			continue
		}
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			start, _ := strconv.Atoi(match[1])
			current.newLine, current.inHunk = start, true
			current.summary.Hunks++
			if plan.ChangedHunks+current.summary.Hunks > MaxReviewHunks {
				return nil, fmt.Errorf("review_input_too_large: more than %d hunks", MaxReviewHunks)
			}
			continue
		}
		if !current.inHunk || line == "\\ No newline at end of file" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			current.lines = append(current.lines, current.newLine)
			current.newLine++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			// A deletion has no right-side anchor and does not advance newLine.
		default:
			current.newLine++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse review diff: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	sort.Slice(plan.Anchors, func(i, j int) bool {
		if plan.Anchors[i].Path == plan.Anchors[j].Path {
			return plan.Anchors[i].StartLine < plan.Anchors[j].StartLine
		}
		return plan.Anchors[i].Path < plan.Anchors[j].Path
	})
	hash, err := plan.CanonicalHash()
	if err != nil {
		return nil, err
	}
	plan.PlanHash = hash
	return plan, nil
}

// ValidCommitSHA accepts full provider commit IDs and abbreviated hexadecimal
// object names. Creation paths use it to reject malformed revision pairs before
// queueing; the Runner still verifies that the objects exist and match exactly.
func ValidCommitSHA(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func compressReviewLines(path string, lines []int) []ReviewAnchor {
	if len(lines) == 0 {
		return nil
	}
	sort.Ints(lines)
	start, end := lines[0], lines[0]
	var out []ReviewAnchor
	for _, line := range lines[1:] {
		if line <= end+1 {
			if line > end {
				end = line
			}
			continue
		}
		out = append(out, ReviewAnchor{Path: path, StartLine: start, EndLine: end})
		start, end = line, line
	}
	return append(out, ReviewAnchor{Path: path, StartLine: start, EndLine: end})
}

func (p ReviewPlan) AllowsAnchor(path string, line, endLine int) bool {
	if endLine == 0 {
		endLine = line
	}
	for _, anchor := range p.Anchors {
		if anchor.Path == path && line >= anchor.StartLine && endLine <= anchor.EndLine {
			return true
		}
	}
	return false
}

func (r ReviewResult) ValidateAgainst(plan *ReviewPlan) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if plan == nil {
		return errors.New("review plan is required")
	}
	for i, finding := range r.Findings {
		if !plan.AllowsAnchor(finding.Path, finding.Line, finding.EndLine) {
			return fmt.Errorf("finding %d is not anchored to a changed right-side line", i+1)
		}
	}
	return nil
}

type storedReviewPlan struct {
	SchemaVersion int              `json:"schema_version"`
	PlanHash      string           `json:"plan_hash"`
	BaseSHA       string           `json:"base_sha"`
	HeadSHA       string           `json:"head_sha"`
	MergeBaseSHA  string           `json:"merge_base_sha"`
	RulesRevision string           `json:"rules_revision"`
	Coverage      string           `json:"coverage"`
	ChangedFiles  int              `json:"changed_files"`
	EligibleFiles int              `json:"eligible_files"`
	IndexedFiles  int              `json:"indexed_files"`
	ChangedHunks  int              `json:"changed_hunks"`
	IndexedHunks  int              `json:"indexed_hunks"`
	ChangedLines  int              `json:"changed_lines"`
	Files         []ReviewPlanFile `json:"files"`
	Anchors       []ReviewAnchor   `json:"anchors"`
	CreatedAt     time.Time        `json:"created_at"`
}

func (p ReviewPlan) stored() storedReviewPlan {
	return storedReviewPlan{p.SchemaVersion, p.PlanHash, p.BaseSHA, p.HeadSHA, p.MergeBaseSHA, p.RulesRevision, p.Coverage, p.ChangedFiles, p.EligibleFiles, p.IndexedFiles, p.ChangedHunks, p.IndexedHunks, p.ChangedLines, p.Files, p.Anchors, p.CreatedAt}
}

func (p ReviewPlan) CanonicalHash() (string, error) {
	stored := p.stored()
	stored.PlanHash = ""
	stored.CreatedAt = time.Time{}
	data, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func MarshalStoredReviewPlan(p *ReviewPlan) ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	return json.Marshal(p.stored())
}

func UnmarshalStoredReviewPlan(data []byte) (*ReviewPlan, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var value storedReviewPlan
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	p := &ReviewPlan{value.SchemaVersion, value.PlanHash, value.BaseSHA, value.HeadSHA, value.MergeBaseSHA, value.RulesRevision, value.Coverage, value.ChangedFiles, value.EligibleFiles, value.IndexedFiles, value.ChangedHunks, value.IndexedHunks, value.ChangedLines, value.Files, value.Anchors, value.CreatedAt}
	want, err := p.CanonicalHash()
	if err != nil {
		return nil, err
	}
	if p.PlanHash != want {
		return nil, errors.New("review plan hash mismatch")
	}
	return p, nil
}
