package scmevent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestCapabilitiesExposeOnlyProviderSupportedActions(t *testing.T) {
	if !Capabilities(ProviderGitHub).Supports(FamilyReview, ActionDismissed) {
		t.Fatal("GitHub must support dismissed reviews")
	}
	if Capabilities(ProviderGitLab).Supports(FamilyReview, ActionDismissed) {
		t.Fatal("GitLab must not advertise a fake dismissed-review action")
	}
	if got := Capabilities(ProviderGitea).MinimumVersion; got != "1.25" {
		t.Fatalf("Gitea minimum version = %q", got)
	}
}

func TestCapabilitiesForVersionDisablesUnsupportedSelfHostedProvider(t *testing.T) {
	if got := CapabilitiesForVersion(ProviderGitLab, "17.10.9"); len(got.Capabilities) != 0 {
		t.Fatalf("old GitLab advertised %d capability families", len(got.Capabilities))
	}
	if got := CapabilitiesForVersion(ProviderGitea, "v1.25.0+gitea"); len(got.Capabilities) == 0 {
		t.Fatal("supported Gitea release lost all capabilities")
	}
	if got := CapabilitiesForVersion(ProviderGitea, "not-a-version"); len(got.Capabilities) != 0 {
		t.Fatal("unparseable self-hosted version must fail closed")
	}
}

func TestNormalizeGitHubCommentKeepsWholeBody(t *testing.T) {
	body := []byte(`{
	  "action":"created",
	  "comment":{"id":44,"body":"context before @jcode please inspect this whole report","html_url":"https://github.com/a/r/issues/7#issuecomment-44","user":{"id":9,"login":"outside-contributor"}},
	  "issue":{"number":7,"pull_request":{"url":"https://api.github.test/repos/a/r/pulls/7"}},
	  "repository":{"id":12,"full_name":"a/r","default_branch":"main","html_url":"https://github.com/a/r"},
	  "sender":{"id":9,"login":"outside-contributor"}
	}`)
	event, err := Normalize(ProviderGitHub, "issue_comment", "delivery-1", body, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.Family != FamilyComment || event.Action != ActionCreated {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.Body != "context before @jcode please inspect this whole report" {
		t.Fatalf("comment body was rewritten: %q", event.Body)
	}
	if event.Actor.ID != "9" || event.Actor.Login != "outside-contributor" {
		t.Fatalf("actor = %+v", event.Actor)
	}
}

func TestNormalizeGitHubCommentUsesObservedAppSlug(t *testing.T) {
	body := []byte(`{"action":"created","repository":{"id":1,"full_name":"acme/repo"},"sender":{"id":2,"login":"alice"},"issue":{"number":7,"pull_request":{"url":"https://api.github.test/pulls/7"}},"comment":{"id":9,"body":"@acme-review-bot review","html_url":"https://github.test/comment/9","user":{"id":2,"login":"alice"}}}`)
	event, err := NormalizeForApp(ProviderGitHub, "issue_comment", "custom-app", body, time.Unix(100, 0), "acme-review-bot")
	if err != nil {
		t.Fatal(err)
	}
	if event.Body != "@acme-review-bot review" {
		t.Fatalf("body=%q", event.Body)
	}
}

func TestNormalizeCommentEditAndMissingMentionAreIgnored(t *testing.T) {
	for name, body := range map[string]string{
		"edited":  `{"action":"edited","comment":{"id":1,"body":"@jcode run"},"issue":{"number":2},"repository":{"id":3,"full_name":"a/r"}}`,
		"missing": `{"action":"created","comment":{"id":1,"body":"ordinary comment"},"issue":{"number":2},"repository":{"id":3,"full_name":"a/r"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Normalize(ProviderGitHub, "issue_comment", "d", []byte(body), time.Now())
			if !errors.Is(err, ErrIgnored) {
				t.Fatalf("error = %v, want ErrIgnored", err)
			}
		})
	}
}

func TestContainsJCodeMentionRequiresTokenBoundaries(t *testing.T) {
	tests := map[string]bool{
		"@jcode please review":       true,
		"hello (@JCODE), ship this":  true,
		"@jcode-cloud-app review":    true,
		"foo@jcodeevil.example":      false,
		"@jcode_bot do something":    false,
		"prefix-@jcode is not a tag": false,
		"no mention":                 false,
	}
	for body, want := range tests {
		if got := ContainsJCodeMention(body); got != want {
			t.Errorf("ContainsJCodeMention(%q)=%v want %v", body, got, want)
		}
	}
}

func TestParseReviewCommandUsesRealAppHandleAndCompatibilityAlias(t *testing.T) {
	tests := []struct {
		body      string
		wantOK    bool
		wantFull  bool
		wantFocus string
	}{
		{"@jcode-cloud-app review", true, false, ""},
		{"please @JCODE-CLOUD-APP full review", true, true, ""},
		{"@jcode-cloud-app review security and concurrency", true, false, "security and concurrency"},
		{"@jcode review the transaction boundary", true, false, "the transaction boundary"},
		{"@jcode-cloud-app investigate this", false, false, ""},
		{"email@jcode-cloud-app review", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			got, ok := ParseReviewCommand(tt.body, "jcode-cloud-app")
			if ok != tt.wantOK || got.Full != tt.wantFull || got.Focus != tt.wantFocus {
				t.Fatalf("ParseReviewCommand()=(%+v,%v), want ok=%v full=%v focus=%q", got, ok, tt.wantOK, tt.wantFull, tt.wantFocus)
			}
		})
	}
}

func TestNormalizeGitHubCommentRequiresPullRequestMarker(t *testing.T) {
	issueOnly := []byte(`{
	  "action":"created",
	  "comment":{"id":44,"body":"@jcode-cloud-app review","user":{"id":9,"login":"contributor"}},
	  "issue":{"id":7,"number":7},
	  "repository":{"id":12,"full_name":"a/r"},
	  "sender":{"id":9,"login":"contributor"}
	}`)
	if _, err := Normalize(ProviderGitHub, "issue_comment", "issue", issueOnly, time.Now()); !errors.Is(err, ErrIgnored) {
		t.Fatalf("ordinary issue comment error=%v, want ErrIgnored", err)
	}
	prComment := []byte(`{
	  "action":"created",
	  "comment":{"id":45,"body":"@jcode-cloud-app review","user":{"id":9,"login":"contributor"}},
	  "issue":{"id":7,"number":7,"pull_request":{"url":"https://api.github.test/repos/a/r/pulls/7"}},
	  "repository":{"id":12,"full_name":"a/r"},
	  "sender":{"id":9,"login":"contributor"}
	}`)
	event, err := Normalize(ProviderGitHub, "issue_comment", "pr", prComment, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !event.IsPullRequestComment || event.Object.ID != "45" || event.Object.Number != 7 {
		t.Fatalf("normalized PR comment=%+v", event)
	}
}

func TestNormalizePullRequestCarriesDraftState(t *testing.T) {
	body := []byte(`{
	  "action":"opened",
	  "pull_request":{"id":8,"number":4,"draft":true,"head":{"ref":"feat","sha":"abc1234"},"base":{"ref":"main","sha":"def5678"}},
	  "repository":{"id":1,"full_name":"a/r"},
	  "sender":{"id":2,"login":"maintainer"}
	}`)
	event, err := Normalize(ProviderGitHub, "pull_request", "draft", body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !event.Draft {
		t.Fatal("draft pull request lost its draft state")
	}
	if event.HeadSHA != "abc1234" || event.BaseSHA != "def5678" {
		t.Fatalf("revision pair = %s...%s", event.BaseSHA, event.HeadSHA)
	}
}

func TestNormalizeProviderLifecycleEvents(t *testing.T) {
	tests := []struct {
		name      string
		provider  ProviderKind
		eventType string
		body      string
		family    Family
		action    Action
	}{
		{
			name: "github merged pull request", provider: ProviderGitHub, eventType: "pull_request",
			body:   `{"action":"closed","pull_request":{"id":8,"number":4,"merged":true,"head":{"ref":"feat","sha":"abc"},"base":{"ref":"main"}},"repository":{"id":1,"full_name":"a/r"},"sender":{"id":2,"login":"maintainer"}}`,
			family: FamilyPullRequest, action: ActionMerged,
		},
		{
			name: "gitea rejected review", provider: ProviderGitea, eventType: "pull_request_review_rejected",
			body:   `{"action":"reviewed","review":{"id":8,"type":"rejected","user":{"id":2,"login":"reviewer"}},"pull_request":{"number":4},"repository":{"id":1,"full_name":"a/r"},"sender":{"id":2,"login":"reviewer"}}`,
			family: FamilyReview, action: ActionChangesRequested,
		},
		{
			name: "gitlab approval", provider: ProviderGitLab, eventType: "Merge Request Hook",
			body:   `{"object_kind":"merge_request","user":{"id":2,"username":"reviewer"},"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"iid":4,"action":"approved","source_branch":"feat","target_branch":"main"}}`,
			family: FamilyReview, action: ActionApproved,
		},
		{
			name: "gitlab failed pipeline", provider: ProviderGitLab, eventType: "Pipeline Hook",
			body:   `{"object_kind":"pipeline","user":{"id":2,"username":"ci"},"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"status":"failed","ref":"main"}}`,
			family: FamilyCheck, action: ActionCompleted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := Normalize(tt.provider, tt.eventType, "delivery", []byte(tt.body), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if event.Family != tt.family || event.Action != tt.action {
				t.Fatalf("event = %s.%s, want %s.%s", event.Family, event.Action, tt.family, tt.action)
			}
			if tt.name == "gitlab failed pipeline" && event.Conclusion != "failure" {
				t.Fatalf("conclusion = %q", event.Conclusion)
			}
		})
	}
}

func TestNormalizeGiteaSpecificReviewEvents(t *testing.T) {
	tests := []struct {
		eventType string
		want      Action
	}{
		{"pull_request_review_approved", ActionApproved},
		{"pull_request_review_rejected", ActionChangesRequested},
		{"pull_request_review_comment", ActionCommented},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			body := []byte(`{"action":"reviewed","review":{"id":8,"type":"approved","user":{"id":2,"login":"reviewer"}},"pull_request":{"number":4},"repository":{"id":1,"full_name":"a/r"}}`)
			event, err := Normalize(ProviderGitea, tt.eventType, "delivery", body, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if event.Family != FamilyReview || event.Action != tt.want {
				t.Fatalf("event = %s.%s, want review.%s", event.Family, event.Action, tt.want)
			}
		})
	}
}

func TestNormalizeGitLabOnlyCreatesMentionComments(t *testing.T) {
	base := `{"object_kind":"note","user":{"id":2,"username":"reviewer"},"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"iid":4,"action":%q,"note":"please @jcode inspect this"}}`
	event, err := Normalize(ProviderGitLab, "Note Hook", "create", []byte(fmt.Sprintf(base, "create")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Family != FamilyComment || event.Body != "please @jcode inspect this" {
		t.Fatalf("unexpected create event: %+v", event)
	}
	for _, action := range []string{"update", ""} {
		_, err := Normalize(ProviderGitLab, "Note Hook", "ignored-"+action, []byte(fmt.Sprintf(base, action)), time.Now())
		if !errors.Is(err, ErrIgnored) {
			t.Fatalf("action %q error = %v, want ErrIgnored", action, err)
		}
	}
}

func TestNormalizeGitLabMRUpdateRequiresOldRevision(t *testing.T) {
	base := `{"object_kind":"merge_request","user":{"id":2,"username":"reviewer"},"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"iid":4,"action":"update","oldrev":%q,"source_branch":"feat","target_branch":"main"}}`
	_, err := Normalize(ProviderGitLab, "Merge Request Hook", "ordinary-update", []byte(fmt.Sprintf(base, "")), time.Now())
	if !errors.Is(err, ErrIgnored) {
		t.Fatalf("ordinary update error = %v, want ErrIgnored", err)
	}
	event, err := Normalize(ProviderGitLab, "Merge Request Hook", "code-update", []byte(fmt.Sprintf(base, "before-sha")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Family != FamilyPullRequest || event.Action != ActionSynchronized {
		t.Fatalf("event = %s.%s, want pull_request.synchronized", event.Family, event.Action)
	}
}

func TestNormalizePushCollectsChangedPathsWithoutPersistingThem(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main","after":"abc","commits":[{"added":["src/a.go"],"modified":["README.md"],"removed":["src/a.go","old.txt"]}],"repository":{"id":1,"full_name":"a/r"}}`)
	event, err := Normalize(ProviderGitHub, "push", "delivery", body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/a.go", "README.md", "old.txt"}
	if !reflect.DeepEqual(event.ChangedPaths, want) {
		t.Fatalf("changed paths = %#v, want %#v", event.ChangedPaths, want)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("src/a.go")) {
		t.Fatalf("changed paths leaked into persistence-safe JSON: %s", encoded)
	}
}

func TestNormalizeCheckEventsRequireTerminalState(t *testing.T) {
	tests := []struct {
		name      string
		provider  ProviderKind
		eventType string
		body      string
		want      string
		ignored   bool
	}{
		{"gitea success status", ProviderGitea, "status", `{"id":8,"sha":"abc","state":"success","context":"ci/test","repository":{"id":1,"full_name":"a/r"}}`, "success", false},
		{"gitea pending status", ProviderGitea, "status", `{"id":8,"sha":"abc","state":"pending","context":"ci/test","repository":{"id":1,"full_name":"a/r"}}`, "", true},
		{"gitlab failed pipeline", ProviderGitLab, "Pipeline Hook", `{"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"status":"failed","ref":"main"}}`, "failure", false},
		{"gitlab running pipeline", ProviderGitLab, "Pipeline Hook", `{"project":{"id":1,"path_with_namespace":"a/r"},"object_attributes":{"id":8,"status":"running","ref":"main"}}`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := Normalize(tt.provider, tt.eventType, tt.name, []byte(tt.body), time.Now())
			if tt.ignored {
				if !errors.Is(err, ErrIgnored) {
					t.Fatalf("error = %v, want ErrIgnored", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if event.Family != FamilyCheck || event.Action != ActionCompleted || event.Conclusion != tt.want {
				t.Fatalf("unexpected event: %+v", event)
			}
		})
	}
}

func TestCoalescePolicy(t *testing.T) {
	comment := NormalizedSCMEvent{Family: FamilyComment, Action: ActionCreated, Object: Object{ID: "1"}}
	if got := CoalesceKey("svc", comment); got != "" {
		t.Fatalf("comment coalesced under %q", got)
	}
	push := NormalizedSCMEvent{Family: FamilyPush, Action: ActionUpdated, Ref: "refs/heads/main"}
	if got := CoalesceKey("svc", push); got != "svc:push:refs/heads/main" {
		t.Fatalf("push key = %q", got)
	}
	check := NormalizedSCMEvent{Family: FamilyCheck, Action: ActionCompleted, Ref: "main", Object: Object{ID: "77"}}
	if got := CoalesceKey("svc", check); got != "svc:check:77:main" {
		t.Fatalf("check key = %q", got)
	}
}

func TestFilterMatchesBranchPathsAndConclusion(t *testing.T) {
	filter := Filter{
		Branch:       "main",
		IncludePaths: []string{"src/**", "*.go"},
		ExcludePaths: []string{"src/generated/**"},
		Conclusions:  []string{"failure"},
	}
	event := NormalizedSCMEvent{Family: FamilyPush, Ref: "refs/heads/main", Conclusion: "failure"}
	if !filter.Matches(event, []string{"README.md", "src/api/handler.go"}) {
		t.Fatal("expected matching branch/path/conclusion")
	}
	if filter.Matches(event, []string{"src/generated/client.go"}) {
		t.Fatal("excluded path matched")
	}
	event.Ref = "refs/heads/release"
	if filter.Matches(event, []string{"src/api/handler.go"}) {
		t.Fatal("wrong branch matched")
	}
}

func TestFilterBranchUsesFamilySpecificTargetAndSafeGlob(t *testing.T) {
	tests := []struct {
		name  string
		event NormalizedSCMEvent
		match bool
	}{
		{
			name:  "push head matches glob",
			event: NormalizedSCMEvent{Family: FamilyPush, Ref: "refs/heads/release/2026"},
			match: true,
		},
		{
			name:  "pull request target matches regardless of source",
			event: NormalizedSCMEvent{Family: FamilyPullRequest, Ref: "contributor/topic", BaseRef: "release/2026"},
			match: true,
		},
		{
			name:  "pull request source alone does not match",
			event: NormalizedSCMEvent{Family: FamilyPullRequest, Ref: "release/2026", BaseRef: "main"},
			match: false,
		},
		{
			name:  "check head matches",
			event: NormalizedSCMEvent{Family: FamilyCheck, Ref: "release/2026"},
			match: true,
		},
		{
			name:  "family without branch fails closed",
			event: NormalizedSCMEvent{Family: FamilyIssue, Ref: "release/2026"},
			match: false,
		},
	}
	filter := Filter{Branch: "release/*"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filter.Matches(tt.event, nil); got != tt.match {
				t.Fatalf("Matches=%v want %v for %+v", got, tt.match, tt.event)
			}
		})
	}
}

func TestJCodeGeneratedDetection(t *testing.T) {
	if !DetectJCodeGenerated(NormalizedSCMEvent{Ref: "agent/run-123"}) {
		t.Fatal("agent run branch not detected")
	}
	if !DetectJCodeGenerated(NormalizedSCMEvent{Actor: Actor{Login: "jcode-bot"}}) {
		t.Fatal("jcode bot actor not detected")
	}
	if DetectJCodeGenerated(NormalizedSCMEvent{Actor: Actor{Login: "contributor"}}) {
		t.Fatal("ordinary contributor detected as jcode")
	}
}
