package domain

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Status comments are intentionally much smaller than a native review. This
	// leaves ample room below every supported provider's comment-body limit.
	maxRenderedReviewStatusBytes       = 16_000
	maxReviewStatusTitleBytes          = 1_024
	maxReviewStatusFailureMessageBytes = 4_000
	maxReviewStatusRunIDBytes          = 512
	maxReviewStatusRevisionBytes       = 64
	maxReviewStatusURLBytes            = 2_048
	maxRenderedReviewStatusURLBytes    = 2_048
	maxReviewStatusMarkerBytes         = 512
	reviewStatusTextTruncationNotice   = "> Some status text was truncated to fit provider comment limits. Full Run details remain available in jcode Cloud."
	reviewStatusMarkerPrefix           = "<!-- jcode-review-status:v1:"
	reviewStatusMarkerSuffix           = " -->"
)

// ReviewStatusState is the provider-visible lifecycle of the single status
// comment maintained for a Service and pull request. It is deliberately
// separate from RunStatus and DeliveryStatus: one status comment can follow a
// sequence of Runs as newer revisions supersede older work.
type ReviewStatusState string

const (
	ReviewStatusQueued     ReviewStatusState = "queued"
	ReviewStatusRunning    ReviewStatusState = "running"
	ReviewStatusPublishing ReviewStatusState = "publishing"
	ReviewStatusCompleted  ReviewStatusState = "completed"
	ReviewStatusPartial    ReviewStatusState = "partial"
	ReviewStatusFailed     ReviewStatusState = "failed"
	ReviewStatusCanceled   ReviewStatusState = "canceled"
	ReviewStatusSuperseded ReviewStatusState = "superseded"
)

func (s ReviewStatusState) Valid() bool {
	switch s {
	case ReviewStatusQueued, ReviewStatusRunning, ReviewStatusPublishing,
		ReviewStatusCompleted, ReviewStatusPartial, ReviewStatusFailed, ReviewStatusCanceled,
		ReviewStatusSuperseded:
		return true
	}
	return false
}

// Terminal reports whether no further transition is required for the current
// review attempt. A later head revision may still reuse the Service+PR comment.
func (s ReviewStatusState) Terminal() bool {
	switch s {
	case ReviewStatusCompleted, ReviewStatusPartial, ReviewStatusFailed, ReviewStatusCanceled, ReviewStatusSuperseded:
		return true
	}
	return false
}

// ReviewStatusCompletionConverged keeps the provider status projection tied to
// the structured completion receipt. Unlike the ordinary Run observation, this
// check remains live across rolling upgrades so an older worker cannot make a
// legacy or partial review quiesce as completed.
func ReviewStatusCompletionConverged(state ReviewStatusState, result *ReviewResult) bool {
	complete := result != nil && result.Completion != nil && result.Completion.Status == ReviewCompletionComplete
	switch state {
	case ReviewStatusCompleted:
		return complete
	case ReviewStatusPartial:
		return !complete
	default:
		return true
	}
}

// ReviewStatusCommentKey enforces the product identity of one mutable status
// comment per Service and provider pull request.
type ReviewStatusCommentKey struct {
	ServiceID      string      `json:"service_id"`
	Provider       GitProvider `json:"provider"`
	ProviderRepoID string      `json:"provider_repo_id"`
	PRNumber       int         `json:"pr_number"`
}

// ReviewStatusCommentMarker derives the opaque marker used to recover the
// provider comment after an ambiguous create. Key material is hashed so public
// provider comments do not disclose internal Service or repository IDs.
func ReviewStatusCommentMarker(key ReviewStatusCommentKey) string {
	payload := fmt.Sprintf("%d:%s|%d:%s|%d:%s|%d",
		len(key.ServiceID), key.ServiceID,
		len(key.Provider), key.Provider,
		len(key.ProviderRepoID), key.ProviderRepoID,
		key.PRNumber,
	)
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%s%x%s", reviewStatusMarkerPrefix, digest, reviewStatusMarkerSuffix)
}

// ReviewStatusCommentBodyHash is the deterministic comparison key stored in
// the outbox. Persisting only the digest keeps the provider-rendered body out of
// the database while still making repeated reconcile ticks no-ops.
func ReviewStatusCommentBodyHash(body string) string {
	digest := sha256.Sum256([]byte(body))
	return fmt.Sprintf("%x", digest)
}

// ReviewStatusComment is the durable desired/applied state used to reconcile a
// provider comment without coupling review execution to provider availability.
// LastError contains only a caller-sanitized error; credentials and raw
// provider responses never belong in this record.
type ReviewStatusComment struct {
	Key               ReviewStatusCommentKey     `json:"key"`
	RepositoryPath    string                     `json:"repository_path"`
	CurrentRunID      string                     `json:"current_run_id"`
	HeadSHA           string                     `json:"head_sha"`
	AcceptedSequence  int64                      `json:"-"`
	ReceiptClaimToken string                     `json:"-"`
	InstallationID    string                     `json:"-"`
	CommentID         string                     `json:"comment_id,omitempty"`
	CommentURL        string                     `json:"comment_url,omitempty"`
	DesiredState      ReviewStatusState          `json:"desired_state"`
	AppliedState      ReviewStatusState          `json:"applied_state,omitempty"`
	DesiredBodyHash   string                     `json:"desired_body_hash"`
	AppliedBodyHash   string                     `json:"applied_body_hash,omitempty"`
	ClaimToken        string                     `json:"-"`
	ClaimedAt         *time.Time                 `json:"-"`
	Attempts          int                        `json:"attempts"`
	LastError         string                     `json:"last_error,omitempty"`
	NextAttemptAt     *time.Time                 `json:"-"`
	ObservedRun       ReviewStatusRunObservation `json:"-"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

// ReviewStatusRunObservation is the exact Run projection last rendered into
// DesiredState/DesiredBodyHash. Persisting this fingerprint lets the outbox
// select only real changes instead of hot-scanning every active review forever.
type ReviewStatusRunObservation struct {
	Status         RunStatus
	Phase          string
	FailureReason  FailureReason
	DeliveryStatus DeliveryStatus
	ReviewPosted   bool
	ReviewPlanHash string
}

func ReviewStatusObservationForRun(run Run) ReviewStatusRunObservation {
	planHash := ""
	if run.ReviewPlan != nil {
		planHash = run.ReviewPlan.PlanHash
	}
	return ReviewStatusRunObservation{
		Status: run.Status, Phase: run.Phase, FailureReason: run.FailureReason,
		DeliveryStatus: run.DeliveryStatus, ReviewPosted: run.ReviewPostedAt != nil,
		ReviewPlanHash: planHash,
	}
}

// ReviewStatusPlanCounts is the safe aggregate plan context shown in a status
// comment. Changed-line anchors and model-authored content are never rendered.
type ReviewStatusPlanCounts struct {
	ChangedFiles  int
	EligibleFiles int
	IndexedFiles  int
	ChangedLines  int
}

// ReviewStatusCommentInput contains the provider-neutral status facts. Marker
// is an opaque, caller-owned HTML comment used to recover and update the same
// provider comment; the renderer validates it and includes it exactly once.
type ReviewStatusCommentInput struct {
	Provider GitProvider
	State    ReviewStatusState
	Run      Run
	Plan     ReviewStatusPlanCounts
	RunURL   string
	Marker   string
}

type reviewStatusCopy struct {
	heading string
	message string
	alert   string
}

// RenderReviewStatusComment renders the mutable lifecycle comment. GitHub gets
// native alert syntax; Gitea and GitLab get portable Markdown. The final native
// review remains a separate provider object on every provider.
func RenderReviewStatusComment(input ReviewStatusCommentInput) (string, error) {
	if !ValidProvider(input.Provider) {
		return "", fmt.Errorf("unsupported review status provider %q", input.Provider)
	}
	if !input.State.Valid() {
		return "", fmt.Errorf("invalid review status state %q", input.State)
	}
	if err := validateReviewStatusRun(input.Run); err != nil {
		return "", err
	}
	if err := validateReviewStatusMarker(input.Marker); err != nil {
		return "", err
	}
	if err := validateReviewStatusPlanCounts(input.Plan); err != nil {
		return "", err
	}
	truncated := false
	runURL, valueTruncated, err := renderReviewStatusURL(input.RunURL)
	if err != nil {
		return "", err
	}
	truncated = truncated || valueTruncated
	prURL, valueTruncated, err := renderReviewStatusURL(input.Run.PRURL)
	if err != nil {
		return "", err
	}
	truncated = truncated || valueTruncated

	copy := reviewStatusCopyFor(input.State)
	title, valueTruncated := escapeReviewMarkdownBounded(reviewSingleLine(input.Run.PRTitle), maxReviewStatusTitleBytes)
	truncated = truncated || valueTruncated
	failure, valueTruncated := escapeReviewMarkdownBounded(reviewSingleLine(input.Run.FailureMessage), maxReviewStatusFailureMessageBytes)
	truncated = truncated || valueTruncated
	runID, valueTruncated := escapeReviewStatusHTMLBounded(reviewSingleLine(input.Run.ID), maxReviewStatusRunIDBytes)
	truncated = truncated || valueTruncated
	revision, valueTruncated := renderReviewStatusRevision(input.Run.PRHeadSHA)
	truncated = truncated || valueTruncated

	var b strings.Builder
	b.WriteString(input.Marker)
	b.WriteString("\n\n")
	if input.Provider == ProviderGitHub {
		fmt.Fprintf(&b, "> [!%s]\n> ## %s\n>\n> %s", copy.alert, copy.heading, copy.message)
	} else {
		fmt.Fprintf(&b, "## jcode review · %s\n\n> %s", copy.heading, copy.message)
	}

	if input.Run.PRNumber > 0 {
		b.WriteString("\n\nPull request: ")
		if prURL == "" {
			fmt.Fprintf(&b, "**#%d**", input.Run.PRNumber)
		} else {
			fmt.Fprintf(&b, "[#%d](%s)", input.Run.PRNumber, prURL)
		}
		if title != "" {
			b.WriteString(" · ")
			b.WriteString(title)
		}
	} else if title != "" {
		b.WriteString("\n\nPull request: ")
		b.WriteString(title)
	}
	if revision != "" {
		fmt.Fprintf(&b, "\n\nRevision: <code>%s</code>", revision)
	}
	if hasReviewStatusPlanCounts(input.Plan) {
		fmt.Fprintf(&b, "\n\nPlan: **%d of %d files indexed** · %d eligible · %d changed lines",
			input.Plan.IndexedFiles, input.Plan.ChangedFiles, input.Plan.EligibleFiles, input.Plan.ChangedLines)
	}
	if failure != "" && input.State == ReviewStatusFailed {
		b.WriteString("\n\nReason: ")
		b.WriteString(failure)
	}
	if runURL != "" {
		fmt.Fprintf(&b, "\n\n[View run](%s)", runURL)
	} else if runID != "" {
		fmt.Fprintf(&b, "\n\nRun: <code>%s</code>", runID)
	}
	if truncated {
		b.WriteString("\n\n")
		b.WriteString(reviewStatusTextTruncationNotice)
	}
	b.WriteString("\n\n---\n\n<sub>This status comment is updated in place. The native review is a separate, non-blocking COMMENT review.</sub>")

	body := b.String()
	if len(body) > maxRenderedReviewStatusBytes {
		return "", fmt.Errorf("rendered review status exceeds %d bytes", maxRenderedReviewStatusBytes)
	}
	return body, nil
}

func reviewStatusCopyFor(state ReviewStatusState) reviewStatusCopy {
	switch state {
	case ReviewStatusQueued:
		return reviewStatusCopy{"Review queued", "jcode accepted this review and is waiting for an available runner; the final review will be posted separately.", "NOTE"}
	case ReviewStatusRunning:
		return reviewStatusCopy{"Review in progress", "jcode is reviewing the captured pull request revision; the final review will be posted separately.", "NOTE"}
	case ReviewStatusPublishing:
		return reviewStatusCopy{"Publishing review", "jcode's analysis has ended; the native review is being published separately.", "IMPORTANT"}
	case ReviewStatusCompleted:
		return reviewStatusCopy{"Review completed", "jcode's native review was published separately from this status comment.", "TIP"}
	case ReviewStatusPartial:
		return reviewStatusCopy{"Review incomplete", "jcode did not reach a clean conclusion; a partial native review was published separately.", "WARNING"}
	case ReviewStatusFailed:
		return reviewStatusCopy{"Review failed", "jcode review did not complete. No native review was published for this attempt.", "CAUTION"}
	case ReviewStatusCanceled:
		return reviewStatusCopy{"Review canceled", "jcode review was canceled. No native review was published for this attempt.", "WARNING"}
	case ReviewStatusSuperseded:
		return reviewStatusCopy{"Review superseded", "A newer pull request revision replaced this attempt. No native review was published for this attempt.", "NOTE"}
	default:
		panic("validated review status state is not rendered")
	}
}

func validateReviewStatusMarker(marker string) error {
	if marker != strings.TrimSpace(marker) || len(marker) > maxReviewStatusMarkerBytes ||
		!strings.HasPrefix(marker, reviewStatusMarkerPrefix) || !strings.HasSuffix(marker, reviewStatusMarkerSuffix) {
		return errors.New("review status marker must use the versioned jcode namespace")
	}
	digest := marker[len(reviewStatusMarkerPrefix) : len(marker)-len(reviewStatusMarkerSuffix)]
	if len(digest) != sha256.Size*2 {
		return errors.New("review status marker has an invalid digest")
	}
	for _, r := range digest {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return errors.New("review status marker has an invalid digest")
		}
	}
	return nil
}

func validateReviewStatusPlanCounts(plan ReviewStatusPlanCounts) error {
	if plan.ChangedFiles < 0 || plan.EligibleFiles < 0 || plan.IndexedFiles < 0 || plan.ChangedLines < 0 ||
		plan.EligibleFiles > plan.ChangedFiles || plan.IndexedFiles > plan.EligibleFiles {
		return errors.New("review status plan counts are inconsistent")
	}
	return nil
}

func validateReviewStatusRun(run Run) error {
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("review status Run ID is required")
	}
	if run.PRNumber < 1 {
		return errors.New("review status pull request number must be positive")
	}
	headSHA := strings.TrimSpace(run.PRHeadSHA)
	if headSHA != run.PRHeadSHA || !ValidCommitSHA(headSHA) {
		return errors.New("review status head revision must be a bounded hexadecimal object ID")
	}
	return nil
}

func hasReviewStatusPlanCounts(plan ReviewStatusPlanCounts) bool {
	return plan.ChangedFiles != 0 || plan.EligibleFiles != 0 || plan.IndexedFiles != 0 || plan.ChangedLines != 0
}

func renderReviewStatusRevision(value string) (string, bool) {
	value = reviewSingleLine(value)
	if len(value) > 12 {
		value = value[:12]
	}
	return escapeReviewStatusHTMLBounded(value, maxReviewStatusRevisionBytes)
}

// renderReviewStatusURL returns omitted=true when a syntactically optional link
// cannot fit its fixed output budget. Dropping the whole destination is safer
// than truncating it into a different URL, and the caller can still render the
// PR number or Run ID as a deterministic non-link fallback.
func renderReviewStatusURL(value string) (rendered string, omitted bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}
	if len(value) > maxReviewStatusURLBytes {
		return "", true, nil
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		// Links are optional display metadata from Provider/configuration input.
		// Omit an unsafe value visibly instead of letting it block the accepted
		// review Run or turning it into an active non-HTTP destination.
		return "", true, nil
	}
	rendered = u.String()
	rendered = strings.ReplaceAll(rendered, "\\", "%5C")
	rendered = strings.ReplaceAll(rendered, "(", "%28")
	rendered = strings.ReplaceAll(rendered, ")", "%29")
	rendered, outputTruncated := escapeReviewHTMLBounded(rendered, maxRenderedReviewStatusURLBytes)
	if outputTruncated {
		return "", true, nil
	}
	return rendered, false, nil
}

func escapeReviewStatusHTMLBounded(value string, limit int) (string, bool) {
	return encodeReviewTextBounded(value, limit, func(r rune) string {
		if r == '@' {
			return "&#64;&#8203;"
		}
		return escapedReviewHTMLRune(r)
	})
}
