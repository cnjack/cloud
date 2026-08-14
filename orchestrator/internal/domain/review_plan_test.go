package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const reviewPlanFixture = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -8,3 +8,4 @@ func a() {
 context
-old
+new
+another
 context
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..3333333
Binary files /dev/null and b/logo.png differ
diff --git a/renamed.txt b/moved.txt
similarity index 90%
rename from renamed.txt
rename to moved.txt
--- a/renamed.txt
+++ b/moved.txt
@@ -1 +1 @@
-before
+after
`

func TestBuildReviewPlanIndexesOnlyRightSideChangedLines(t *testing.T) {
	plan, err := BuildReviewPlan(ReviewPlanInput{
		BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeBaseSHA: strings.Repeat("c", 40),
		Diff: reviewPlanFixture, CreatedAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Coverage != ReviewCoveragePartial || plan.ChangedFiles != 3 || plan.EligibleFiles != 2 || plan.IndexedFiles != 2 {
		t.Fatalf("coverage summary = %#v", plan)
	}
	if plan.ChangedHunks != 2 || plan.IndexedHunks != 2 || plan.ChangedLines != 3 {
		t.Fatalf("hunk summary = %#v", plan)
	}
	if !plan.AllowsAnchor("a.go", 9, 10) {
		t.Fatal("changed right-side range rejected")
	}
	if plan.AllowsAnchor("a.go", 8, 0) {
		t.Fatal("context line accepted")
	}
	if plan.AllowsAnchor("logo.png", 1, 0) {
		t.Fatal("binary line accepted")
	}
	if !plan.AllowsAnchor("moved.txt", 1, 0) {
		t.Fatal("renamed changed line rejected")
	}
	if plan.PlanHash == "" {
		t.Fatal("plan hash missing")
	}
	if len(plan.Anchors) == 0 {
		t.Fatal("private anchors missing")
	}
}

func TestBuildReviewPlanRejectsUnboundedInput(t *testing.T) {
	_, err := BuildReviewPlan(ReviewPlanInput{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeBaseSHA: strings.Repeat("c", 40), Diff: strings.Repeat("x", MaxReviewDiffBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "review_input_too_large") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewResultValidateAgainstPlan(t *testing.T) {
	plan, err := BuildReviewPlan(ReviewPlanInput{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeBaseSHA: strings.Repeat("c", 40), Diff: reviewPlanFixture})
	if err != nil {
		t.Fatal(err)
	}
	valid := ReviewResult{Summary: "one issue", Findings: []ReviewFinding{{Path: "a.go", Line: 9, EndLine: 10, Severity: "P1", Confidence: 95, Title: "bug", Body: "breaks behavior"}}}
	if err := valid.ValidateAgainst(plan); err != nil {
		t.Fatalf("valid result: %v", err)
	}
	invalid := valid
	invalid.Findings = append([]ReviewFinding(nil), valid.Findings...)
	invalid.Findings[0].Line = 8
	invalid.Findings[0].EndLine = 0
	if err := invalid.ValidateAgainst(plan); err == nil || !strings.Contains(err.Error(), "changed right-side line") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewResultNormalizeCompletionAgainstPlan(t *testing.T) {
	completePlan, err := BuildReviewPlan(ReviewPlanInput{
		BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeBaseSHA: strings.Repeat("c", 40),
		Diff: `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-old
+new
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1 +1 @@
-old
+new
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	partialPlan, err := BuildReviewPlan(ReviewPlanInput{
		BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeBaseSHA: strings.Repeat("c", 40),
		Diff: reviewPlanFixture,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		plan        *ReviewPlan
		completion  *ReviewCompletion
		wantStatus  ReviewCompletionStatus
		wantReasons []ReviewIncompleteReason
	}{
		{
			name:        "missing receipt is partial",
			plan:        completePlan,
			wantStatus:  ReviewCompletionPartial,
			wantReasons: []ReviewIncompleteReason{ReviewReasonCompletionUnreported},
		},
		{
			name: "missing indexed file is partial",
			plan: completePlan,
			completion: &ReviewCompletion{
				Status: ReviewCompletionComplete, ReviewedFiles: []string{"a.go"},
			},
			wantStatus:  ReviewCompletionPartial,
			wantReasons: []ReviewIncompleteReason{ReviewReasonFilesUnreviewed},
		},
		{
			name: "partial input cannot become clean",
			plan: partialPlan,
			completion: &ReviewCompletion{
				Status: ReviewCompletionComplete, ReviewedFiles: []string{"a.go", "moved.txt"},
			},
			wantStatus:  ReviewCompletionPartial,
			wantReasons: []ReviewIncompleteReason{ReviewReasonInputCoveragePartial},
		},
		{
			name: "exact receipt is complete",
			plan: completePlan,
			completion: &ReviewCompletion{
				Status: ReviewCompletionComplete, ReviewedFiles: []string{"b.go", "a.go"},
			},
			wantStatus: ReviewCompletionComplete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ReviewResult{Summary: "Review finished.", Completion: tt.completion}
			if err := result.NormalizeAgainst(tt.plan); err != nil {
				t.Fatalf("NormalizeAgainst() error = %v", err)
			}
			if result.Completion == nil || result.Completion.Status != tt.wantStatus {
				t.Fatalf("completion = %#v, want status %q", result.Completion, tt.wantStatus)
			}
			if !reflect.DeepEqual(result.Completion.Reasons, tt.wantReasons) {
				t.Fatalf("reasons = %v, want %v", result.Completion.Reasons, tt.wantReasons)
			}
		})
	}
}
