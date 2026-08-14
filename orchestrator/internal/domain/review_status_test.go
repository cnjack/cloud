package domain

import (
	"strings"
	"testing"
	"time"
)

func TestReviewStatusStateLifecycle(t *testing.T) {
	states := []ReviewStatusState{
		ReviewStatusQueued,
		ReviewStatusRunning,
		ReviewStatusPublishing,
		ReviewStatusCompleted,
		ReviewStatusPartial,
		ReviewStatusFailed,
		ReviewStatusCanceled,
		ReviewStatusSuperseded,
	}
	for _, state := range states {
		if !state.Valid() {
			t.Fatalf("state %q is not valid", state)
		}
		wantTerminal := state == ReviewStatusCompleted || state == ReviewStatusPartial || state == ReviewStatusFailed || state == ReviewStatusCanceled || state == ReviewStatusSuperseded
		if got := state.Terminal(); got != wantTerminal {
			t.Fatalf("state %q Terminal() = %v, want %v", state, got, wantTerminal)
		}
	}
	if ReviewStatusState("waiting").Valid() || ReviewStatusState("waiting").Terminal() {
		t.Fatal("unknown state accepted")
	}
}

func TestReviewStatusCompletionConverged(t *testing.T) {
	complete := &ReviewResult{Completion: &ReviewCompletion{Status: ReviewCompletionComplete}}
	partial := &ReviewResult{Completion: &ReviewCompletion{
		Status: ReviewCompletionPartial, Reasons: []ReviewIncompleteReason{ReviewReasonReviewerIncomplete},
	}}
	tests := []struct {
		name   string
		state  ReviewStatusState
		result *ReviewResult
		want   bool
	}{
		{name: "completed with receipt", state: ReviewStatusCompleted, result: complete, want: true},
		{name: "completed without receipt", state: ReviewStatusCompleted, want: false},
		{name: "partial without receipt", state: ReviewStatusPartial, want: true},
		{name: "partial with partial receipt", state: ReviewStatusPartial, result: partial, want: true},
		{name: "partial with complete receipt", state: ReviewStatusPartial, result: complete, want: false},
		{name: "unrelated terminal state", state: ReviewStatusFailed, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReviewStatusCompletionConverged(tt.state, tt.result); got != tt.want {
				t.Fatalf("ReviewStatusCompletionConverged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReviewStatusCommentPersistenceShape(t *testing.T) {
	now := time.Now().UTC()
	comment := ReviewStatusComment{
		Key: ReviewStatusCommentKey{
			ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-7", PRNumber: 42,
		},
		RepositoryPath:   "acme/widget",
		CurrentRunID:     "run-1",
		HeadSHA:          "abcdef",
		AcceptedSequence: 7,
		CommentID:        "123",
		CommentURL:       "https://github.example/comment/123",
		DesiredState:     ReviewStatusRunning,
		AppliedState:     ReviewStatusQueued,
		DesiredBodyHash:  "desired",
		AppliedBodyHash:  "applied",
		ClaimToken:       "claim",
		ClaimedAt:        &now,
		Attempts:         2,
		LastError:        "retryable",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if comment.Key.Provider != ProviderGitHub || comment.DesiredState.Terminal() || comment.AppliedState.Terminal() {
		t.Fatalf("persistence shape lost typed values: %#v", comment)
	}
}

func TestReviewStatusCommentMarkerIsStableAndOpaque(t *testing.T) {
	key := ReviewStatusCommentKey{ServiceID: "svc/@team", Provider: ProviderGitHub, ProviderRepoID: "repo:7", PRNumber: 42}
	marker := ReviewStatusCommentMarker(key)
	if marker != ReviewStatusCommentMarker(key) {
		t.Fatal("same key produced different markers")
	}
	if !strings.HasPrefix(marker, "<!-- jcode-review-status:v1:") || !strings.HasSuffix(marker, " -->") {
		t.Fatalf("marker does not use the versioned status namespace: %q", marker)
	}
	for _, leaked := range []string{key.ServiceID, key.ProviderRepoID, "@team"} {
		if strings.Contains(marker, leaked) {
			t.Fatalf("marker leaked key material %q: %q", leaked, marker)
		}
	}
	changed := key
	changed.PRNumber++
	if marker == ReviewStatusCommentMarker(changed) {
		t.Fatal("different keys produced the same marker")
	}
}

func TestReviewStatusCommentBodyHashIsStableAndContentAddressed(t *testing.T) {
	if got, want := ReviewStatusCommentBodyHash("queued"), ReviewStatusCommentBodyHash("queued"); got != want {
		t.Fatalf("same body hashes differ: %q != %q", got, want)
	}
	if ReviewStatusCommentBodyHash("queued") == ReviewStatusCommentBodyHash("running") {
		t.Fatal("different bodies produced the same hash")
	}
}

func TestRenderReviewStatusCommentStates(t *testing.T) {
	marker := ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "service-7", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 42})
	base := ReviewStatusCommentInput{
		Provider: ProviderGitHub,
		Run: Run{
			ID:        "run-123",
			PRNumber:  42,
			PRTitle:   "Keep transfers atomic",
			PRHeadSHA: "0123456789abcdef",
			PRURL:     "https://github.example/acme/widget/pull/42",
		},
		Plan:   ReviewStatusPlanCounts{ChangedFiles: 5, EligibleFiles: 4, IndexedFiles: 3, ChangedLines: 87},
		RunURL: "https://cloud.example/runs/run-123",
		Marker: marker,
	}

	tests := []struct {
		state    ReviewStatusState
		alert    string
		heading  string
		message  string
		separate string
	}{
		{ReviewStatusQueued, "NOTE", "Review queued", "waiting for an available runner", "final review will be posted separately"},
		{ReviewStatusRunning, "NOTE", "Review in progress", "reviewing the captured pull request revision", "final review will be posted separately"},
		{ReviewStatusPublishing, "IMPORTANT", "Publishing review", "analysis has ended", "native review is being published separately"},
		{ReviewStatusCompleted, "TIP", "Review completed", "native review was published", "separately from this status comment"},
		{ReviewStatusPartial, "WARNING", "Review incomplete", "did not reach a clean conclusion", "partial native review was published"},
		{ReviewStatusFailed, "CAUTION", "Review failed", "review did not complete", "No native review was published for this attempt"},
		{ReviewStatusCanceled, "WARNING", "Review canceled", "review was canceled", "No native review was published for this attempt"},
		{ReviewStatusSuperseded, "NOTE", "Review superseded", "newer pull request revision", "No native review was published for this attempt"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			input := base
			input.State = tt.state
			if tt.state == ReviewStatusFailed {
				input.Run.FailureMessage = "The runner timed out."
			}
			body, err := RenderReviewStatusComment(input)
			if err != nil {
				t.Fatalf("RenderReviewStatusComment() error = %v", err)
			}
			for _, want := range []string{
				base.Marker,
				"> [!" + tt.alert + "]",
				"> ## " + tt.heading,
				tt.message,
				tt.separate,
				"Pull request: [#42](https://github.example/acme/widget/pull/42) · Keep transfers atomic",
				"Revision: <code>0123456789ab</code>",
				"Plan: **3 of 5 files indexed** · 4 eligible · 87 changed lines",
				"[View run](https://cloud.example/runs/run-123)",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("%s comment missing %q:\n%s", tt.state, want, body)
				}
			}
			if strings.Count(body, base.Marker) != 1 {
				t.Fatalf("marker count = %d, want 1:\n%s", strings.Count(body, base.Marker), body)
			}
		})
	}
}

func TestRenderReviewStatusCommentUsesPortableMarkdown(t *testing.T) {
	for _, provider := range []GitProvider{ProviderGitea, ProviderGitLab} {
		t.Run(string(provider), func(t *testing.T) {
			marker := ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: provider, ProviderRepoID: "repo-1", PRNumber: 9})
			body, err := RenderReviewStatusComment(ReviewStatusCommentInput{
				Provider: provider,
				State:    ReviewStatusRunning,
				Run:      Run{ID: "run-1", PRNumber: 9, PRHeadSHA: "abcdef0123456789"},
				Marker:   marker,
			})
			if err != nil {
				t.Fatalf("RenderReviewStatusComment() error = %v", err)
			}
			for _, want := range []string{marker, "## jcode review · Review in progress", "> jcode is reviewing"} {
				if !strings.Contains(body, want) {
					t.Fatalf("portable comment missing %q:\n%s", want, body)
				}
			}
			if strings.Contains(body, "[!NOTE]") || strings.Contains(body, "[!CAUTION]") {
				t.Fatalf("portable comment contains GitHub-only alert syntax:\n%s", body)
			}
		})
	}
}

func TestRenderReviewStatusCommentEscapesUntrustedFields(t *testing.T) {
	marker := ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 7})
	body, err := RenderReviewStatusComment(ReviewStatusCommentInput{
		Provider: ProviderGitHub,
		State:    ReviewStatusFailed,
		Run: Run{
			ID:             "run-1 @ops/team",
			PRNumber:       7,
			PRTitle:        "Breakout " + marker + "\n\n> [!CAUTION]\n> injected @org/team & &#64;other/team",
			PRHeadSHA:      "abcdef0123456789",
			FailureMessage: "</details>\n\n@security/team",
		},
		Marker: marker,
	})
	if err != nil {
		t.Fatalf("RenderReviewStatusComment() error = %v", err)
	}
	if strings.Count(body, "[!") != 1 {
		t.Fatalf("untrusted text injected a GitHub alert:\n%s", body)
	}
	if strings.Count(body, marker) != 1 {
		t.Fatalf("untrusted text duplicated the identity marker:\n%s", body)
	}
	for _, active := range []string{"@ops/team", "@org/team", "@other/team", "@security/team", "</details>"} {
		if strings.Contains(body, active) {
			t.Fatalf("comment retained unsafe text %q:\n%s", active, body)
		}
	}
	if !strings.Contains(body, "&#64;&#8203;security") || !strings.Contains(body, "&amp;\\#64\\;other") {
		t.Fatalf("mentions or entities were not neutralized:\n%s", body)
	}
	if !strings.Contains(body, "Run: <code>run-1 &#64;&#8203;ops/team</code>") {
		t.Fatalf("untrusted Run ID was not safely rendered:\n%s", body)
	}
}

func TestRenderReviewStatusCommentRendersSafeLinks(t *testing.T) {
	marker := ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 7})
	body, err := RenderReviewStatusComment(ReviewStatusCommentInput{
		Provider: ProviderGitHub,
		State:    ReviewStatusRunning,
		Run: Run{
			ID: "run-1", PRNumber: 7, PRHeadSHA: "abcdef0123456789",
			PRURL: "https://github.example/acme/widget/pull/7?from=a&to=b",
		},
		RunURL: "https://cloud.example/runs/run-1?label=(review)&from=pr",
		Marker: marker,
	})
	if err != nil {
		t.Fatalf("RenderReviewStatusComment() error = %v", err)
	}
	for _, want := range []string{
		"Pull request: [#7](https://github.example/acme/widget/pull/7?from=a&amp;to=b)",
		"[View run](https://cloud.example/runs/run-1?label=%28review%29&amp;from=pr)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment missing safe link %q:\n%s", want, body)
		}
	}
}

func TestRenderReviewStatusCommentSafelyDropsOversizedLinks(t *testing.T) {
	marker := ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 7})
	ampHeavyQuery := strings.Repeat("&", 1_950)

	for _, test := range []struct {
		name   string
		prURL  string
		runURL string
	}{
		{
			name:   "html escaping expansion",
			prURL:  "https://github.example/acme/widget/pull/7?" + ampHeavyQuery,
			runURL: "https://cloud.example/runs/run-1?" + ampHeavyQuery,
		},
		{
			name:   "raw valid URL length",
			prURL:  "https://github.example/acme/widget/pull/7/" + strings.Repeat("a", 8_000),
			runURL: "https://cloud.example/runs/run-1/" + strings.Repeat("b", 8_000),
		},
		{
			name:   "unsafe optional URLs",
			prURL:  "javascript:alert(1)",
			runURL: "/runs/run-1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := RenderReviewStatusComment(ReviewStatusCommentInput{
				Provider: ProviderGitHub,
				State:    ReviewStatusRunning,
				Run: Run{
					ID: "run-1", PRNumber: 7, PRHeadSHA: "abcdef0123456789",
					PRURL: test.prURL,
				},
				RunURL: test.runURL,
				Marker: marker,
			})
			if err != nil {
				t.Fatalf("RenderReviewStatusComment() error = %v", err)
			}
			if len(body) > maxRenderedReviewStatusBytes {
				t.Fatalf("rendered %d bytes, limit %d", len(body), maxRenderedReviewStatusBytes)
			}
			for _, want := range []string{
				"Pull request: **#7**",
				"Run: <code>run-1</code>",
				"Some status text was truncated",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("safely degraded comment missing %q:\n%s", want, body)
				}
			}
			if strings.Contains(body, "[View run](") || strings.Contains(body, ampHeavyQuery) || strings.Contains(body, strings.Repeat("a", 100)) {
				t.Fatalf("oversized link leaked into rendered comment:\n%s", body)
			}
		})
	}
}

func TestRenderReviewStatusCommentRejectsInvalidControlData(t *testing.T) {
	valid := ReviewStatusCommentInput{
		Provider: ProviderGitHub,
		State:    ReviewStatusQueued,
		Run:      Run{ID: "run-1", PRNumber: 1, PRHeadSHA: "abcdef0123456789"},
		Marker:   ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 1}),
	}
	tests := map[string]func(*ReviewStatusCommentInput){
		"provider":  func(input *ReviewStatusCommentInput) { input.Provider = "forge" },
		"state":     func(input *ReviewStatusCommentInput) { input.State = "waiting" },
		"marker":    func(input *ReviewStatusCommentInput) { input.Marker = "<!-- ok -->\n> injected" },
		"run id":    func(input *ReviewStatusCommentInput) { input.Run.ID = "" },
		"pr number": func(input *ReviewStatusCommentInput) { input.Run.PRNumber = 0 },
		"head sha":  func(input *ReviewStatusCommentInput) { input.Run.PRHeadSHA = "not-a-sha" },
		"counts": func(input *ReviewStatusCommentInput) {
			input.Plan = ReviewStatusPlanCounts{ChangedFiles: 1, EligibleFiles: 2, IndexedFiles: 1}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := RenderReviewStatusComment(input); err == nil {
				t.Fatal("invalid control data accepted")
			}
		})
	}
}

func TestRenderReviewStatusCommentStaysWithinProviderLimit(t *testing.T) {
	body, err := RenderReviewStatusComment(ReviewStatusCommentInput{
		Provider: ProviderGitHub,
		State:    ReviewStatusFailed,
		Run: Run{
			ID:             strings.Repeat("@", 10_000),
			PRNumber:       7,
			PRTitle:        strings.Repeat("@", 10_000),
			PRHeadSHA:      strings.Repeat("a", maxReviewStatusRevisionBytes),
			FailureMessage: strings.Repeat("@", 100_000),
		},
		RunURL: "https://cloud.example/runs/run-1",
		Marker: ReviewStatusCommentMarker(ReviewStatusCommentKey{ServiceID: "svc", Provider: ProviderGitHub, ProviderRepoID: "repo-1", PRNumber: 7}),
	})
	if err != nil {
		t.Fatalf("RenderReviewStatusComment() error = %v", err)
	}
	if len(body) > maxRenderedReviewStatusBytes {
		t.Fatalf("rendered %d bytes, limit %d", len(body), maxRenderedReviewStatusBytes)
	}
	if !strings.Contains(body, "truncated") {
		t.Fatalf("comment did not disclose truncation:\n%s", body)
	}
	if strings.Contains(body, "@") {
		t.Fatalf("comment retained an active mention:\n%s", body)
	}
}
