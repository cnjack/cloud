package scmevent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestNormalizeGitHubSupportedEventMatrix is the executable counterpart of the
// public event catalog. The Console may disclose uncommon actions under
// "More events", but every action advertised for GitHub must keep normalizing.
func TestNormalizeGitHubSupportedEventMatrix(t *testing.T) {
	type matrixCase struct {
		name      string
		eventType string
		payload   map[string]any
		family    Family
		action    Action
	}
	repo := map[string]any{"id": 1, "full_name": "acme/widget", "default_branch": "main"}
	sender := map[string]any{"id": 2, "login": "maintainer"}
	pr := func(action string, merged bool) map[string]any {
		return map[string]any{
			"action": action, "repository": repo, "sender": sender,
			"pull_request": map[string]any{
				"id": 10, "number": 7, "merged": merged,
				"head": map[string]any{"ref": "feature", "sha": "abc"},
				"base": map[string]any{"ref": "main"},
			},
		}
	}
	issue := func(action string) map[string]any {
		return map[string]any{
			"action": action, "repository": repo, "sender": sender,
			"issue": map[string]any{"id": 20, "number": 8, "title": "Issue"},
		}
	}
	release := func(action string) map[string]any {
		return map[string]any{
			"action": action, "repository": repo, "sender": sender,
			"release": map[string]any{"id": 30, "name": "v1", "tag_name": "v1.0.0"},
		}
	}
	cases := []matrixCase{
		{"push updated", "push", map[string]any{"ref": "refs/heads/main", "after": "abc", "repository": repo, "sender": sender}, FamilyPush, ActionUpdated},
		{"pull request opened", "pull_request", pr("opened", false), FamilyPullRequest, ActionOpened},
		{"pull request reopened", "pull_request", pr("reopened", false), FamilyPullRequest, ActionReopened},
		{"pull request synchronized", "pull_request", pr("synchronize", false), FamilyPullRequest, ActionSynchronized},
		{"pull request ready", "pull_request", pr("ready_for_review", false), FamilyPullRequest, ActionReady},
		{"pull request closed", "pull_request", pr("closed", false), FamilyPullRequest, ActionClosed},
		{"pull request merged", "pull_request", pr("closed", true), FamilyPullRequest, ActionMerged},
		{"review approved", "pull_request_review", reviewPayload(repo, sender, "submitted", "approved"), FamilyReview, ActionApproved},
		{"review changes requested", "pull_request_review", reviewPayload(repo, sender, "submitted", "changes_requested"), FamilyReview, ActionChangesRequested},
		{"review commented", "pull_request_review", reviewPayload(repo, sender, "submitted", "commented"), FamilyReview, ActionCommented},
		{"review dismissed", "pull_request_review", reviewPayload(repo, sender, "dismissed", "approved"), FamilyReview, ActionDismissed},
		{"comment created", "issue_comment", map[string]any{
			"action": "created", "repository": repo, "sender": sender,
			"issue":   map[string]any{"number": 7},
			"comment": map[string]any{"id": 40, "body": "please @jcode investigate", "user": sender},
		}, FamilyComment, ActionCreated},
		{"issue opened", "issues", issue("opened"), FamilyIssue, ActionOpened},
		{"issue reopened", "issues", issue("reopened"), FamilyIssue, ActionReopened},
		{"issue updated", "issues", issue("edited"), FamilyIssue, ActionUpdated},
		{"issue closed", "issues", issue("closed"), FamilyIssue, ActionClosed},
		{"check completed", "check_run", map[string]any{
			"action": "completed", "repository": repo, "sender": sender,
			"check_run": map[string]any{"id": 50, "name": "test", "conclusion": "success", "head_sha": "abc", "check_suite": map[string]any{"head_branch": "main"}},
		}, FamilyCheck, ActionCompleted},
		{"tag created", "create", map[string]any{"ref_type": "tag", "ref": "v1.0.0", "repository": repo, "sender": sender}, FamilyTag, ActionCreated},
		{"tag deleted", "delete", map[string]any{"ref_type": "tag", "ref": "v1.0.0", "repository": repo, "sender": sender}, FamilyTag, ActionDeleted},
		{"release published", "release", release("published"), FamilyRelease, ActionPublished},
		{"release updated", "release", release("edited"), FamilyRelease, ActionUpdated},
		{"release deleted", "release", release("deleted"), FamilyRelease, ActionDeleted},
	}
	covered := make(map[Family]map[Action]bool)

	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := json.Marshal(testCase.payload)
			if err != nil {
				t.Fatal(err)
			}
			event, err := Normalize(ProviderGitHub, testCase.eventType, fmt.Sprintf("matrix-%d", index), body, time.Unix(100, 0))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if event.Family != testCase.family || event.Action != testCase.action {
				t.Fatalf("normalized %s.%s, want %s.%s", event.Family, event.Action, testCase.family, testCase.action)
			}
			if covered[testCase.family] == nil {
				covered[testCase.family] = make(map[Action]bool)
			}
			covered[testCase.family][testCase.action] = true
		})
	}
	for _, capability := range Capabilities(ProviderGitHub).Capabilities {
		for _, action := range capability.Actions {
			if !covered[capability.Family][action] {
				t.Errorf("GitHub advertises %s.%s without a normalization fixture", capability.Family, action)
			}
		}
	}
}

func reviewPayload(repo, sender map[string]any, action, state string) map[string]any {
	return map[string]any{
		"action": action, "repository": repo, "sender": sender,
		"pull_request": map[string]any{"number": 7},
		"review":       map[string]any{"id": 60, "state": state, "user": sender},
	}
}

func TestNormalizeSelfHostedProviderCommonMatrix(t *testing.T) {
	tests := []struct {
		name      string
		provider  ProviderKind
		eventType string
		body      string
		family    Family
		action    Action
	}{
		{"gitea push updated", ProviderGitea, "push", `{"ref":"refs/heads/main","after":"abc","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"}}`, FamilyPush, ActionUpdated},
		{"gitea pull request opened", ProviderGitea, "pull_request", `{"action":"opened","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"id":10,"number":7,"head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}}`, FamilyPullRequest, ActionOpened},
		{"gitea pull request reopened", ProviderGitea, "pull_request", `{"action":"reopened","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"id":10,"number":7,"head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}}`, FamilyPullRequest, ActionReopened},
		{"gitea pull request synchronized", ProviderGitea, "pull_request_sync", `{"repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"id":10,"number":7,"head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}}`, FamilyPullRequest, ActionSynchronized},
		{"gitea pull request closed", ProviderGitea, "pull_request", `{"action":"closed","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"id":10,"number":7,"merged":false,"head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}}`, FamilyPullRequest, ActionClosed},
		{"gitea pull request merged", ProviderGitea, "pull_request", `{"action":"closed","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"id":10,"number":7,"merged":true,"head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}}`, FamilyPullRequest, ActionMerged},
		{"gitea review approved", ProviderGitea, "pull_request_review_approved", `{"repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"number":7},"review":{"id":11,"user":{"id":2,"login":"owner"}}}`, FamilyReview, ActionApproved},
		{"gitea review changes requested", ProviderGitea, "pull_request_review_rejected", `{"repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"number":7},"review":{"id":11,"user":{"id":2,"login":"owner"}}}`, FamilyReview, ActionChangesRequested},
		{"gitea review commented", ProviderGitea, "pull_request_review_comment", `{"repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"pull_request":{"number":7},"review":{"id":11,"user":{"id":2,"login":"owner"}}}`, FamilyReview, ActionCommented},
		{"gitea comment created", ProviderGitea, "issue_comment", `{"action":"created","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"issue":{"number":8},"comment":{"id":12,"body":"please @jcode investigate","user":{"id":2,"login":"owner"}}}`, FamilyComment, ActionCreated},
		{"gitea issue opened", ProviderGitea, "issues", `{"action":"opened","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"issue":{"id":20,"number":8}}`, FamilyIssue, ActionOpened},
		{"gitea issue reopened", ProviderGitea, "issues", `{"action":"reopened","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"issue":{"id":20,"number":8}}`, FamilyIssue, ActionReopened},
		{"gitea issue updated", ProviderGitea, "issues", `{"action":"edited","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"issue":{"id":20,"number":8}}`, FamilyIssue, ActionUpdated},
		{"gitea issue closed", ProviderGitea, "issues", `{"action":"closed","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"issue":{"id":20,"number":8}}`, FamilyIssue, ActionClosed},
		{"gitea status completed", ProviderGitea, "status", `{"id":30,"ref":"main","sha":"abc","state":"failure","context":"ci","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"}}`, FamilyCheck, ActionCompleted},
		{"gitea tag created", ProviderGitea, "create", `{"ref_type":"tag","ref":"v1.0.0","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"}}`, FamilyTag, ActionCreated},
		{"gitea tag deleted", ProviderGitea, "delete", `{"ref_type":"tag","ref":"v1.0.0","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"}}`, FamilyTag, ActionDeleted},
		{"gitea release published", ProviderGitea, "release", `{"action":"published","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"release":{"id":40,"name":"v1","tag_name":"v1.0.0"}}`, FamilyRelease, ActionPublished},
		{"gitea release updated", ProviderGitea, "release", `{"action":"edited","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"release":{"id":40,"name":"v1","tag_name":"v1.0.0"}}`, FamilyRelease, ActionUpdated},
		{"gitea release deleted", ProviderGitea, "release", `{"action":"deleted","repository":{"id":1,"full_name":"acme/widget"},"sender":{"id":2,"login":"owner"},"release":{"id":40,"name":"v1","tag_name":"v1.0.0"}}`, FamilyRelease, ActionDeleted},
		{"gitlab push updated", ProviderGitLab, "Push Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"ref":"refs/heads/main","after":"abc"}`, FamilyPush, ActionUpdated},
		{"gitlab pull request opened", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"open","source_branch":"feature","target_branch":"main"}}`, FamilyPullRequest, ActionOpened},
		{"gitlab pull request reopened", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"reopen","source_branch":"feature","target_branch":"main"}}`, FamilyPullRequest, ActionReopened},
		{"gitlab pull request synchronized", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"update","oldrev":"before","source_branch":"feature","target_branch":"main"}}`, FamilyPullRequest, ActionSynchronized},
		{"gitlab pull request closed", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"close","state":"closed","source_branch":"feature","target_branch":"main"}}`, FamilyPullRequest, ActionClosed},
		{"gitlab pull request merged", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"merge","state":"merged","source_branch":"feature","target_branch":"main"}}`, FamilyPullRequest, ActionMerged},
		{"gitlab approval", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"approved","source_branch":"feature","target_branch":"main"}}`, FamilyReview, ActionApproved},
		{"gitlab approval removed", ProviderGitLab, "Merge Request Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":10,"iid":7,"action":"unapproved","source_branch":"feature","target_branch":"main"}}`, FamilyReview, ActionApprovalRemoved},
		{"gitlab comment created", ProviderGitLab, "Note Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":12,"iid":7,"action":"create","note":"please @jcode investigate"},"merge_request":{"iid":7}}`, FamilyComment, ActionCreated},
		{"gitlab issue opened", ProviderGitLab, "Issue Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":20,"iid":8,"action":"open"}}`, FamilyIssue, ActionOpened},
		{"gitlab issue reopened", ProviderGitLab, "Issue Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":20,"iid":8,"action":"reopen"}}`, FamilyIssue, ActionReopened},
		{"gitlab issue updated", ProviderGitLab, "Issue Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":20,"iid":8,"action":"update"}}`, FamilyIssue, ActionUpdated},
		{"gitlab issue closed", ProviderGitLab, "Issue Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":20,"iid":8,"action":"close"}}`, FamilyIssue, ActionClosed},
		{"gitlab pipeline completed", ProviderGitLab, "Pipeline Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":30,"status":"success","ref":"main"}}`, FamilyCheck, ActionCompleted},
		{"gitlab tag created", ProviderGitLab, "Tag Push Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"ref":"refs/tags/v1.0.0","after":"abc"}`, FamilyTag, ActionCreated},
		{"gitlab tag deleted", ProviderGitLab, "Tag Push Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"ref":"refs/tags/v1.0.0","after":"0000000000000000000000000000000000000000"}`, FamilyTag, ActionDeleted},
		{"gitlab release published", ProviderGitLab, "Release Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":40,"action":"create","tag":"v1.0.0","name":"v1"}}`, FamilyRelease, ActionPublished},
		{"gitlab release updated", ProviderGitLab, "Release Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":40,"action":"update","tag":"v1.0.0","name":"v1"}}`, FamilyRelease, ActionUpdated},
		{"gitlab release deleted", ProviderGitLab, "Release Hook", `{"project":{"id":1,"path_with_namespace":"acme/widget"},"user":{"id":2,"username":"owner"},"object_attributes":{"id":40,"action":"delete","tag":"v1.0.0","name":"v1"}}`, FamilyRelease, ActionDeleted},
	}
	covered := map[ProviderKind]map[Family]map[Action]bool{}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			event, err := Normalize(testCase.provider, testCase.eventType, fmt.Sprintf("self-hosted-%d", index), []byte(testCase.body), time.Unix(100, 0))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if event.Family != testCase.family || event.Action != testCase.action {
				t.Fatalf("normalized %s.%s, want %s.%s", event.Family, event.Action, testCase.family, testCase.action)
			}
			if covered[testCase.provider] == nil {
				covered[testCase.provider] = map[Family]map[Action]bool{}
			}
			if covered[testCase.provider][testCase.family] == nil {
				covered[testCase.provider][testCase.family] = map[Action]bool{}
			}
			covered[testCase.provider][testCase.family][testCase.action] = true
		})
	}
	for _, provider := range []ProviderKind{ProviderGitLab, ProviderGitea} {
		for _, capability := range Capabilities(provider).Capabilities {
			for _, action := range capability.Actions {
				if !covered[provider][capability.Family][action] {
					t.Errorf("%s advertises %s.%s without a normalization fixture", provider, capability.Family, action)
				}
			}
		}
	}
}
