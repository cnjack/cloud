package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	reviewOutputEnv  = "JCLOUD_REVIEW_OUTPUT_FILE"
	reviewReceiptEnv = "JCLOUD_REVIEW_RECEIPT_FILE"
	reviewDiffEnv    = "JCLOUD_REVIEW_DIFF_FILE"
	baseSHAEnv       = "PR_BASE_SHA"
	headSHAEnv       = "PR_HEAD_SHA"
	mergeBaseSHAEnv  = "PR_MERGE_BASE_SHA"
)

type submitReviewInput struct {
	Summary    string                   `json:"summary" jsonschema_description:"Required. Write compact GitHub-flavored Markdown in 2-4 short paragraphs or a short bullet list, using at most 4 short sentences focused on the conclusion and material risk. Put code identifiers, file paths, commands, and API routes in Markdown inline code. Do not add backslashes before Markdown punctuation."`
	Findings   []domain.ReviewFinding   `json:"findings" jsonschema_description:"Required. Verified defects only, anchored to changed right-side lines; use an empty array when none qualify."`
	Checks     []string                 `json:"checks" jsonschema_description:"Required. Specific files, callers, tests, and commands actually inspected."`
	Completion *domain.ReviewCompletion `json:"completion" jsonschema_description:"Required execution receipt. Claim complete only after every indexed text file was actually inspected."`
}

type submissionReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	PlanHash      string `json:"plan_hash"`
	ReviewSHA256  string `json:"review_sha256"`
}

type reviewSubmitter struct {
	plan        *domain.ReviewPlan
	outputFile  string
	receiptFile string
}

func main() {
	submitter, err := newSubmitterFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[reviewmcp] initialize: %v\n", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 {
		if os.Args[1] != "verify" {
			fmt.Fprintf(os.Stderr, "usage: reviewmcp [verify]\n")
			os.Exit(2)
		}
		if err := submitter.Verify(); err != nil {
			fmt.Fprintf(os.Stderr, "[reviewmcp] verify: %v\n", err)
			os.Exit(1)
		}
		return
	}

	tool := mcp.NewTool(
		"submit_review",
		mcp.WithDescription("Finalize this pull-request review. This is the only accepted completion path. It validates findings against the frozen changed-line plan, verifies the completion receipt, and writes REVIEW.json. If validation fails, correct the arguments and call this tool again. Do not end the review until it succeeds."),
		mcp.WithInputSchema[submitReviewInput](),
	)
	s := server.NewMCPServer("jcode-review", "1.0.0", server.WithToolCapabilities(false))
	s.AddTool(tool, func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, err := decodeSubmitReviewInput(request.GetRawArguments())
		if err != nil {
			return mcp.NewToolResultError("Review submission rejected: " + err.Error()), nil
		}
		result, err := submitter.Submit(input)
		if err != nil {
			return mcp.NewToolResultError("Review submission rejected: " + err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"Review submission accepted: status=%s, findings=%d, reviewed_files=%d. REVIEW.json is finalized; end the review now.",
			result.Completion.Status, len(result.Findings), len(result.Completion.ReviewedFiles),
		)), nil
	})
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "[reviewmcp] serve: %v\n", err)
		os.Exit(1)
	}
}

func newSubmitterFromEnv() (*reviewSubmitter, error) {
	requiredNames := []string{
		reviewOutputEnv,
		reviewReceiptEnv,
		reviewDiffEnv,
		baseSHAEnv,
		headSHAEnv,
		mergeBaseSHAEnv,
	}
	required := make(map[string]string, len(requiredNames))
	for _, name := range requiredNames {
		value := os.Getenv(name)
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
		required[name] = value
	}
	diff, err := os.ReadFile(required[reviewDiffEnv])
	if err != nil {
		return nil, fmt.Errorf("read frozen review diff: %w", err)
	}
	plan, err := domain.BuildReviewPlan(domain.ReviewPlanInput{
		BaseSHA:      required[baseSHAEnv],
		HeadSHA:      required[headSHAEnv],
		MergeBaseSHA: required[mergeBaseSHAEnv],
		Diff:         string(diff),
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("build frozen review plan: %w", err)
	}
	return &reviewSubmitter{
		plan: plan, outputFile: required[reviewOutputEnv], receiptFile: required[reviewReceiptEnv],
	}, nil
}

func decodeSubmitReviewInput(raw any) (submitReviewInput, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return submitReviewInput{}, fmt.Errorf("encode tool arguments: %w", err)
	}
	var input submitReviewInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return submitReviewInput{}, fmt.Errorf("arguments are not valid review JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return submitReviewInput{}, errors.New("arguments must contain exactly one review object")
	}
	if input.Findings == nil {
		return submitReviewInput{}, errors.New("findings is required; use an empty array when there are none")
	}
	if input.Checks == nil {
		return submitReviewInput{}, errors.New("checks is required; use an empty array only when no checks were performed")
	}
	if input.Completion == nil {
		return submitReviewInput{}, errors.New("completion is required")
	}
	if input.Completion.ReviewedFiles == nil {
		return submitReviewInput{}, errors.New("completion.reviewed_files is required; use an empty array when no file was inspected")
	}
	return input, nil
}

func (s *reviewSubmitter) Submit(input submitReviewInput) (*domain.ReviewResult, error) {
	if s == nil || s.plan == nil {
		return nil, errors.New("review plan is unavailable")
	}
	if input.Completion == nil {
		return nil, errors.New("completion is required")
	}
	result := domain.ReviewResult{
		Summary: input.Summary, Findings: input.Findings, Checks: input.Checks, Completion: input.Completion,
	}
	if err := result.ValidateAgainst(s.plan); err != nil {
		return nil, err
	}
	if err := validateCompletionClaim(result.Completion, s.plan); err != nil {
		return nil, err
	}
	if err := result.NormalizeAgainst(s.plan); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode validated review: %w", err)
	}
	data = append(data, '\n')
	hash := sha256.Sum256(data)
	receipt := submissionReceipt{
		SchemaVersion: 1,
		PlanHash:      s.plan.PlanHash,
		ReviewSHA256:  "sha256:" + hex.EncodeToString(hash[:]),
	}
	receiptData, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode review receipt: %w", err)
	}
	receiptData = append(receiptData, '\n')

	_ = os.Remove(s.receiptFile)
	if err := writeAtomic(s.outputFile, data); err != nil {
		return nil, fmt.Errorf("write REVIEW.json: %w", err)
	}
	if err := writeAtomic(s.receiptFile, receiptData); err != nil {
		return nil, fmt.Errorf("write review receipt: %w", err)
	}
	return &result, nil
}

func validateCompletionClaim(completion *domain.ReviewCompletion, plan *domain.ReviewPlan) error {
	if completion == nil {
		return errors.New("completion is required")
	}
	indexed := make(map[string]bool, plan.IndexedFiles)
	for _, file := range plan.Files {
		if file.Status == "indexed" {
			indexed[file.Path] = true
		}
	}
	for _, file := range completion.ReviewedFiles {
		if !indexed[file] {
			return fmt.Errorf("completion references non-indexed file %q", file)
		}
	}
	if completion.Status != domain.ReviewCompletionComplete {
		return nil
	}
	if plan.Coverage != domain.ReviewCoverageComplete {
		return errors.New("completion cannot be complete because the immutable Review Plan has partial input coverage; use partial with reason input_coverage_partial")
	}
	reviewed := make(map[string]bool, len(completion.ReviewedFiles))
	for _, file := range completion.ReviewedFiles {
		reviewed[file] = true
	}
	missing := make([]string, 0)
	for file := range indexed {
		if !reviewed[file] {
			missing = append(missing, file)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("complete review is missing %d indexed file(s): %s; inspect them or submit partial with reason files_unreviewed", len(missing), strings.Join(missing, ", "))
	}
	return nil
}

func (s *reviewSubmitter) Verify() error {
	if s == nil || s.plan == nil {
		return errors.New("review plan is unavailable")
	}
	reviewData, err := os.ReadFile(s.outputFile)
	if err != nil {
		return fmt.Errorf("read REVIEW.json: %w", err)
	}
	receiptData, err := os.ReadFile(s.receiptFile)
	if err != nil {
		return fmt.Errorf("successful submit_review receipt is missing: %w", err)
	}
	var receipt submissionReceipt
	decoder := json.NewDecoder(bytes.NewReader(receiptData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("submit_review receipt is invalid")
	}
	if receipt.SchemaVersion != 1 || receipt.PlanHash != s.plan.PlanHash {
		return errors.New("submit_review receipt does not match the frozen Review Plan")
	}
	hash := sha256.Sum256(reviewData)
	want := "sha256:" + hex.EncodeToString(hash[:])
	if receipt.ReviewSHA256 != want {
		return errors.New("REVIEW.json changed after submit_review succeeded")
	}
	var result domain.ReviewResult
	decoder = json.NewDecoder(bytes.NewReader(reviewData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("REVIEW.json is not exactly one valid review object")
	}
	if result.Completion == nil {
		return errors.New("REVIEW.json completion is missing")
	}
	if err := result.ValidateAgainst(s.plan); err != nil {
		return err
	}
	return validateCompletionClaim(result.Completion, s.plan)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jcode-review-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
