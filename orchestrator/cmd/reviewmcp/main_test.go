package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

func testReviewSubmitter(t *testing.T) (*reviewSubmitter, string, string) {
	t.Helper()
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	diff := `diff --git a/alpha.go b/alpha.go
index 1111111..2222222 100644
--- a/alpha.go
+++ b/alpha.go
@@ -1,2 +1,3 @@
 package sample
+var alpha = true
 var keep = true
diff --git a/beta.go b/beta.go
index 3333333..4444444 100644
--- a/beta.go
+++ b/beta.go
@@ -8,2 +8,3 @@
 func beta() {
+    consume(nil)
 }
`
	plan, err := domain.BuildReviewPlan(domain.ReviewPlanInput{
		BaseSHA: base, HeadSHA: head, MergeBaseSHA: base, Diff: diff,
	})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "REVIEW.json")
	receipt := filepath.Join(dir, "review-receipt.json")
	return &reviewSubmitter{plan: plan, outputFile: output, receiptFile: receipt}, output, receipt
}

func completeReviewInput() submitReviewInput {
	return submitReviewInput{
		Summary:  "One verified defect.",
		Findings: []domain.ReviewFinding{{Path: "beta.go", Line: 9, Severity: "P2", Confidence: 95, Title: "Nil reaches consumer", Body: "The changed call passes nil into a consumer that requires a value."}},
		Checks:   []string{"Read both changed files and the consume caller."},
		Completion: &domain.ReviewCompletion{
			Status: domain.ReviewCompletionComplete, ReviewedFiles: []string{"alpha.go", "beta.go"},
		},
	}
}

func TestSubmitWritesValidatedReviewAndMatchingReceipt(t *testing.T) {
	s, output, receipt := testReviewSubmitter(t)
	result, err := s.Submit(completeReviewInput())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Completion.Status != domain.ReviewCompletionComplete {
		t.Fatalf("completion status = %q", result.Completion.Status)
	}
	if err := s.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(receipt); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode: info=%v err=%v", info, err)
	}
}

func TestSubmitRejectsMissingCompletionWithoutWritingFiles(t *testing.T) {
	s, output, receipt := testReviewSubmitter(t)
	input := completeReviewInput()
	input.Completion = nil
	if _, err := s.Submit(input); err == nil || !strings.Contains(err.Error(), "completion is required") {
		t.Fatalf("submit error = %v", err)
	}
	for _, path := range []string{output, receipt} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected file %s after rejected submit: %v", path, err)
		}
	}
}

func TestSubmitRejectsFalseCompleteAndNamesMissingFile(t *testing.T) {
	s, _, _ := testReviewSubmitter(t)
	input := completeReviewInput()
	input.Completion.ReviewedFiles = []string{"beta.go"}
	if _, err := s.Submit(input); err == nil || !strings.Contains(err.Error(), "alpha.go") {
		t.Fatalf("submit error = %v, want missing alpha.go", err)
	}
}

func TestSubmitRejectsFindingOutsideChangedLine(t *testing.T) {
	s, _, _ := testReviewSubmitter(t)
	input := completeReviewInput()
	input.Findings[0].Line = 8
	if _, err := s.Submit(input); err == nil || !strings.Contains(err.Error(), "changed right-side line") {
		t.Fatalf("submit error = %v", err)
	}
}

func TestSubmitAcceptsExplicitPartialReceipt(t *testing.T) {
	s, _, _ := testReviewSubmitter(t)
	input := completeReviewInput()
	input.Completion = &domain.ReviewCompletion{
		Status: domain.ReviewCompletionPartial, ReviewedFiles: []string{"beta.go"},
		Reasons: []domain.ReviewIncompleteReason{domain.ReviewReasonFilesUnreviewed},
	}
	result, err := s.Submit(input)
	if err != nil {
		t.Fatalf("submit partial: %v", err)
	}
	if result.Completion.Status != domain.ReviewCompletionPartial {
		t.Fatalf("completion status = %q", result.Completion.Status)
	}
	if err := s.Verify(); err != nil {
		t.Fatalf("verify partial: %v", err)
	}
}

func TestVerifyRejectsReviewChangedAfterSubmission(t *testing.T) {
	s, output, _ := testReviewSubmitter(t)
	if _, err := s.Submit(completeReviewInput()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	var changed map[string]any
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	changed["summary"] = "tampered after tool success"
	data, _ = json.Marshal(changed)
	if err := os.WriteFile(output, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(); err == nil || !strings.Contains(err.Error(), "changed after submit_review") {
		t.Fatalf("verify error = %v", err)
	}
}

func TestDecodeSubmitReviewInputRequiresArraysAndRejectsUnknownFields(t *testing.T) {
	base := map[string]any{
		"summary": "clean", "findings": []any{}, "checks": []any{},
		"completion": map[string]any{"status": "complete", "reviewed_files": []any{}},
	}
	if _, err := decodeSubmitReviewInput(base); err != nil {
		t.Fatalf("decode valid: %v", err)
	}
	delete(base, "completion")
	if _, err := decodeSubmitReviewInput(base); err == nil || !strings.Contains(err.Error(), "completion") {
		t.Fatalf("missing completion error = %v", err)
	}
	base["completion"] = map[string]any{"status": "complete", "reviewed_files": []any{}}
	base["extra"] = true
	if _, err := decodeSubmitReviewInput(base); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}
