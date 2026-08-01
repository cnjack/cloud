package scmevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrIgnored     = errors.New("webhook event ignored")
	ErrUnsupported = errors.New("webhook event unsupported")
)

type commonUser struct {
	ID        any    `json:"id"`
	Login     string `json:"login"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

func (u commonUser) actor() Actor {
	login := u.Login
	if login == "" {
		login = u.Username
	}
	return Actor{ID: anyID(u.ID), Login: login, AvatarURL: u.AvatarURL}
}

type commonRepo struct {
	ID                any    `json:"id"`
	FullName          string `json:"full_name"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	HTMLURL           string `json:"html_url"`
	WebURL            string `json:"web_url"`
}

type commonCommit struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

func (r commonRepo) repository() Repository {
	fullName := r.FullName
	if fullName == "" {
		fullName = r.PathWithNamespace
	}
	htmlURL := r.HTMLURL
	if htmlURL == "" {
		htmlURL = r.WebURL
	}
	return Repository{ID: anyID(r.ID), FullName: fullName, DefaultBranch: r.DefaultBranch, HTMLURL: htmlURL}
}

type commonPayload struct {
	Action     string         `json:"action"`
	ID         any            `json:"id"`
	Number     int64          `json:"number"`
	Ref        string         `json:"ref"`
	RefType    string         `json:"ref_type"`
	After      string         `json:"after"`
	SHA        string         `json:"sha"`
	State      string         `json:"state"`
	Context    string         `json:"context"`
	TargetURL  string         `json:"target_url"`
	Sender     commonUser     `json:"sender"`
	User       commonUser     `json:"user"`
	Pusher     commonUser     `json:"pusher"`
	Repository commonRepo     `json:"repository"`
	Commits    []commonCommit `json:"commits"`
	Project    commonRepo     `json:"project"`
	Issue      struct {
		ID          any    `json:"id"`
		Number      int64  `json:"number"`
		Title       string `json:"title"`
		HTMLURL     string `json:"html_url"`
		PullRequest struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	PullRequest struct {
		ID      any    `json:"id"`
		Number  int64  `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		Draft   bool   `json:"draft"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
	Review struct {
		ID      any        `json:"id"`
		State   string     `json:"state"`
		Type    string     `json:"type"`
		HTMLURL string     `json:"html_url"`
		User    commonUser `json:"user"`
	} `json:"review"`
	Comment struct {
		ID      any        `json:"id"`
		Body    string     `json:"body"`
		HTMLURL string     `json:"html_url"`
		User    commonUser `json:"user"`
	} `json:"comment"`
	CheckRun struct {
		ID         any    `json:"id"`
		Name       string `json:"name"`
		HTMLURL    string `json:"html_url"`
		Conclusion string `json:"conclusion"`
		HeadSHA    string `json:"head_sha"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
	} `json:"check_run"`
	WorkflowRun struct {
		ID         any    `json:"id"`
		Name       string `json:"name"`
		HTMLURL    string `json:"html_url"`
		Conclusion string `json:"conclusion"`
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
	} `json:"workflow_run"`
	Release struct {
		ID      any    `json:"id"`
		Name    string `json:"name"`
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	} `json:"release"`
}

// Normalize decodes a provider webhook into the persistence-safe event
// contract. Callers authenticate the webhook before invoking this function.
// The original payload must be discarded after this call.
func Normalize(provider ProviderKind, eventType, deliveryID string, payload []byte, receivedAt time.Time) (NormalizedSCMEvent, error) {
	return NormalizeForApp(provider, eventType, deliveryID, payload, receivedAt, "jcode-cloud-app")
}

// NormalizeForApp recognizes the provider-observed GitHub App slug while
// retaining @jcode as a compatibility alias.
func NormalizeForApp(provider ProviderKind, eventType, deliveryID string, payload []byte, receivedAt time.Time, appSlug string) (NormalizedSCMEvent, error) {
	if provider == ProviderGitLab {
		return normalizeGitLab(eventType, deliveryID, payload, receivedAt, appSlug)
	}
	if provider != ProviderGitHub && provider != ProviderGitea {
		return NormalizedSCMEvent{}, ErrUnsupported
	}
	var p commonPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return NormalizedSCMEvent{}, fmt.Errorf("decode webhook: %w", err)
	}
	repo := p.Repository.repository()
	actor := p.Sender.actor()
	if actor.ID == "" {
		actor = p.User.actor()
	}
	if actor.ID == "" {
		actor = p.Pusher.actor()
	}
	event := NormalizedSCMEvent{
		Provider: provider, DeliveryID: strings.TrimSpace(deliveryID), Actor: actor,
		Repository: repo, OccurredAt: receivedAt.UTC(),
	}
	kind := strings.ToLower(strings.TrimSpace(eventType))
	switch kind {
	case "push":
		event.Family, event.Action, event.Ref, event.HeadSHA = FamilyPush, ActionUpdated, p.Ref, p.After
		event.ChangedPaths = collectChangedPaths(p.Commits)
	case "pull_request", "pull_request_sync":
		event.Family = FamilyPullRequest
		event.Action = normalizePullRequestAction(p.Action, p.PullRequest.Merged, kind == "pull_request_sync")
		event.Object = Object{ID: anyID(p.PullRequest.ID), Number: p.PullRequest.Number, Title: p.PullRequest.Title, URL: p.PullRequest.HTMLURL}
		event.Ref, event.BaseRef, event.HeadSHA, event.BaseSHA = p.PullRequest.Head.Ref, p.PullRequest.Base.Ref, p.PullRequest.Head.SHA, p.PullRequest.Base.SHA
		event.Draft = p.PullRequest.Draft
	case "pull_request_review":
		event.Family = FamilyReview
		event.Action = normalizeReviewAction(p.Action, firstNonEmpty(p.Review.State, p.Review.Type))
		event.Object = Object{ID: anyID(p.Review.ID), Number: firstNonZero(p.PullRequest.Number, p.Number), URL: p.Review.HTMLURL}
		if reviewActor := p.Review.User.actor(); reviewActor.ID != "" {
			event.Actor = reviewActor
		}
	case "pull_request_review_approved", "pull_request_review_rejected", "pull_request_review_comment":
		event.Family = FamilyReview
		switch kind {
		case "pull_request_review_approved":
			event.Action = ActionApproved
		case "pull_request_review_rejected":
			event.Action = ActionChangesRequested
		case "pull_request_review_comment":
			event.Action = ActionCommented
		}
		event.Object = Object{ID: anyID(p.Review.ID), Number: firstNonZero(p.PullRequest.Number, p.Number), URL: p.Review.HTMLURL}
		if reviewActor := p.Review.User.actor(); reviewActor.ID != "" {
			event.Actor = reviewActor
		}
	case "issue_comment", "pull_request_comment":
		if !strings.EqualFold(p.Action, "created") || !ContainsJCodeMentionForApp(p.Comment.Body, appSlug) {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		isPRComment := kind == "pull_request_comment" || strings.TrimSpace(p.Issue.PullRequest.URL) != ""
		if provider == ProviderGitHub && !isPRComment {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action, event.Body = FamilyComment, ActionCreated, p.Comment.Body
		event.Object = Object{ID: anyID(p.Comment.ID), Number: p.Issue.Number, URL: p.Comment.HTMLURL}
		event.IsPullRequestComment = isPRComment
		if commentActor := p.Comment.User.actor(); commentActor.ID != "" {
			event.Actor = commentActor
		}
	case "issues":
		event.Family, event.Action = FamilyIssue, normalizeIssueAction(p.Action)
		event.Object = Object{ID: anyID(p.Issue.ID), Number: p.Issue.Number, Title: p.Issue.Title, URL: p.Issue.HTMLURL}
	case "check_run":
		if !strings.EqualFold(p.Action, "completed") {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action = FamilyCheck, ActionCompleted
		event.Object = Object{ID: anyID(p.CheckRun.ID), Title: p.CheckRun.Name, URL: p.CheckRun.HTMLURL}
		event.Ref, event.HeadSHA, event.Conclusion = p.CheckRun.CheckSuite.HeadBranch, p.CheckRun.HeadSHA, strings.ToLower(p.CheckRun.Conclusion)
	case "workflow_run":
		if !strings.EqualFold(p.Action, "completed") {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action = FamilyCheck, ActionCompleted
		event.Object = Object{ID: anyID(p.WorkflowRun.ID), Title: p.WorkflowRun.Name, URL: p.WorkflowRun.HTMLURL}
		event.Ref, event.HeadSHA, event.Conclusion = p.WorkflowRun.HeadBranch, p.WorkflowRun.HeadSHA, normalizeConclusion(p.WorkflowRun.Conclusion)
	case "status":
		event.Conclusion = normalizeConclusion(p.State)
		if !terminalConclusion(event.Conclusion) {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action = FamilyCheck, ActionCompleted
		event.Object = Object{ID: anyID(p.ID), Title: p.Context, URL: p.TargetURL}
		event.Ref, event.HeadSHA = p.Ref, p.SHA
	case "create", "delete":
		if !strings.EqualFold(p.RefType, "tag") {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Ref = FamilyTag, p.Ref
		if kind == "create" {
			event.Action = ActionCreated
		} else {
			event.Action = ActionDeleted
		}
	case "release":
		event.Family, event.Action = FamilyRelease, normalizeReleaseAction(p.Action)
		event.Ref = p.Release.TagName
		event.Object = Object{ID: anyID(p.Release.ID), Title: p.Release.Name, URL: p.Release.HTMLURL}
	default:
		return NormalizedSCMEvent{}, ErrUnsupported
	}
	if event.Action == "" {
		return NormalizedSCMEvent{}, ErrIgnored
	}
	event.GeneratedByJCode = DetectJCodeGenerated(event)
	if err := event.Validate(); err != nil {
		return NormalizedSCMEvent{}, err
	}
	return event, nil
}

type gitLabPayload struct {
	ObjectKind       string         `json:"object_kind"`
	EventName        string         `json:"event_name"`
	User             commonUser     `json:"user"`
	Project          commonRepo     `json:"project"`
	Ref              string         `json:"ref"`
	After            string         `json:"after"`
	Commits          []commonCommit `json:"commits"`
	ObjectAttributes struct {
		ID           any    `json:"id"`
		IID          int64  `json:"iid"`
		Action       string `json:"action"`
		OldRev       string `json:"oldrev"`
		State        string `json:"state"`
		Title        string `json:"title"`
		URL          string `json:"url"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		DiffRefs struct {
			BaseSHA string `json:"base_sha"`
			HeadSHA string `json:"head_sha"`
		} `json:"diff_refs"`
		Status      string `json:"status"`
		Ref         string `json:"ref"`
		Tag         string `json:"tag"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"object_attributes"`
	MergeRequest struct {
		ID    any    `json:"id"`
		IID   int64  `json:"iid"`
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"merge_request"`
}

func normalizeGitLab(eventType, deliveryID string, payload []byte, receivedAt time.Time, appSlug string) (NormalizedSCMEvent, error) {
	var p gitLabPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return NormalizedSCMEvent{}, fmt.Errorf("decode webhook: %w", err)
	}
	event := NormalizedSCMEvent{
		Provider: ProviderGitLab, DeliveryID: strings.TrimSpace(deliveryID),
		Actor: p.User.actor(), Repository: p.Project.repository(), OccurredAt: receivedAt.UTC(),
	}
	kind := strings.ToLower(strings.TrimSpace(eventType))
	if kind == "" {
		kind = strings.ToLower(p.ObjectKind)
	}
	switch kind {
	case "push hook", "push":
		event.Family, event.Action, event.Ref, event.HeadSHA = FamilyPush, ActionUpdated, p.Ref, p.After
		event.ChangedPaths = collectChangedPaths(p.Commits)
	case "tag push hook", "tag_push":
		event.Family, event.Ref = FamilyTag, p.Ref
		if strings.Trim(p.After, "0") == "" {
			event.Action = ActionDeleted
		} else {
			event.Action = ActionCreated
		}
	case "merge request hook", "merge_request":
		event.Family = FamilyPullRequest
		if strings.EqualFold(p.ObjectAttributes.Action, "update") && strings.TrimSpace(p.ObjectAttributes.OldRev) == "" {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Action = normalizeGitLabMRAction(p.ObjectAttributes.Action, p.ObjectAttributes.State)
		event.Object = Object{ID: anyID(p.ObjectAttributes.ID), Number: p.ObjectAttributes.IID, Title: p.ObjectAttributes.Title, URL: p.ObjectAttributes.URL}
		event.Ref, event.BaseRef = p.ObjectAttributes.SourceBranch, p.ObjectAttributes.TargetBranch
		event.HeadSHA = firstNonEmpty(p.ObjectAttributes.DiffRefs.HeadSHA, p.ObjectAttributes.LastCommit.ID)
		event.BaseSHA = p.ObjectAttributes.DiffRefs.BaseSHA
		if event.Action == ActionApproved || event.Action == ActionApprovalRemoved {
			event.Family = FamilyReview
		}
	case "note hook", "note":
		if action := strings.ToLower(strings.TrimSpace(p.ObjectAttributes.Action)); action != "create" && action != "created" {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		if !ContainsJCodeMentionForApp(p.ObjectAttributes.Note, appSlug) {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action, event.Body = FamilyComment, ActionCreated, p.ObjectAttributes.Note
		number := p.MergeRequest.IID
		if number == 0 {
			number = p.ObjectAttributes.IID
		}
		event.Object = Object{ID: anyID(p.ObjectAttributes.ID), Number: number, URL: p.ObjectAttributes.URL}
	case "issue hook", "issue":
		event.Family, event.Action = FamilyIssue, normalizeIssueAction(p.ObjectAttributes.Action)
		event.Object = Object{ID: anyID(p.ObjectAttributes.ID), Number: p.ObjectAttributes.IID, Title: p.ObjectAttributes.Title, URL: p.ObjectAttributes.URL}
	case "pipeline hook", "pipeline", "job hook", "build":
		event.Conclusion = normalizeConclusion(p.ObjectAttributes.Status)
		if !terminalConclusion(event.Conclusion) {
			return NormalizedSCMEvent{}, ErrIgnored
		}
		event.Family, event.Action = FamilyCheck, ActionCompleted
		event.Ref = p.ObjectAttributes.Ref
		event.Object = Object{ID: anyID(p.ObjectAttributes.ID)}
	case "release hook", "release":
		event.Family, event.Action = FamilyRelease, normalizeReleaseAction(p.ObjectAttributes.Action)
		event.Ref = p.ObjectAttributes.Tag
		event.Object = Object{ID: anyID(p.ObjectAttributes.ID), Title: p.ObjectAttributes.Name, URL: p.ObjectAttributes.URL}
	default:
		return NormalizedSCMEvent{}, ErrUnsupported
	}
	if event.Action == "" {
		return NormalizedSCMEvent{}, ErrIgnored
	}
	event.GeneratedByJCode = DetectJCodeGenerated(event)
	if err := event.Validate(); err != nil {
		return NormalizedSCMEvent{}, err
	}
	return event, nil
}

func normalizePullRequestAction(action string, merged, sync bool) Action {
	if sync {
		return ActionSynchronized
	}
	switch strings.ToLower(action) {
	case "opened", "open":
		return ActionOpened
	case "reopened", "reopen":
		return ActionReopened
	case "synchronize", "synchronized", "sync":
		return ActionSynchronized
	case "ready_for_review", "ready":
		return ActionReady
	case "closed", "close":
		if merged {
			return ActionMerged
		}
		return ActionClosed
	case "merged", "merge":
		return ActionMerged
	}
	return ""
}

func normalizeGitLabMRAction(action, state string) Action {
	switch strings.ToLower(action) {
	case "approval", "approved":
		return ActionApproved
	case "unapproval", "unapproved":
		return ActionApprovalRemoved
	case "update":
		// The caller has already verified object_attributes.oldrev, which
		// distinguishes code synchronization from metadata-only updates.
		return ActionSynchronized
	}
	return normalizePullRequestAction(action, strings.EqualFold(state, "merged"), false)
}

func normalizeReviewAction(action, state string) Action {
	if strings.EqualFold(action, "dismissed") {
		return ActionDismissed
	}
	switch strings.ToLower(state) {
	case "approved":
		return ActionApproved
	case "changes_requested", "request_changes", "rejected":
		return ActionChangesRequested
	case "commented", "comment":
		return ActionCommented
	}
	return ""
}

func normalizeIssueAction(action string) Action {
	switch strings.ToLower(action) {
	case "opened", "open":
		return ActionOpened
	case "reopened", "reopen":
		return ActionReopened
	case "edited", "updated", "update":
		return ActionUpdated
	case "closed", "close":
		return ActionClosed
	}
	return ""
}

func normalizeReleaseAction(action string) Action {
	switch strings.ToLower(action) {
	case "published", "created", "create":
		return ActionPublished
	case "edited", "updated", "update":
		return ActionUpdated
	case "deleted", "delete":
		return ActionDeleted
	}
	return ""
}

func normalizeConclusion(status string) string {
	switch strings.ToLower(status) {
	case "success", "succeeded", "passed":
		return "success"
	case "failed", "failure", "error":
		return "failure"
	case "canceled", "cancelled":
		return "cancelled"
	case "skipped":
		return "skipped"
	case "timed_out", "timeout":
		return "timed_out"
	case "manual", "action_required":
		return "action_required"
	default:
		return strings.ToLower(status)
	}
}

func terminalConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "failure", "cancelled", "skipped", "timed_out", "action_required":
		return true
	}
	return false
}

func DetectJCodeGenerated(event NormalizedSCMEvent) bool {
	login := strings.ToLower(event.Actor.Login)
	return strings.Contains(login, "jcode") ||
		strings.HasPrefix(event.Ref, "agent/run-") ||
		strings.HasPrefix(event.Ref, "jcode/") ||
		strings.Contains(strings.ToLower(event.Body), "<!-- jcode:")
}

func anyID(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprint(v)
	}
}

func collectChangedPaths(commits []commonCommit) []string {
	seen := make(map[string]struct{})
	var paths []string
	for _, commit := range commits {
		for _, candidates := range [][]string{commit.Added, commit.Modified, commit.Removed} {
			for _, candidate := range candidates {
				candidate = strings.TrimSpace(candidate)
				if candidate == "" {
					continue
				}
				if _, exists := seen[candidate]; exists {
					continue
				}
				seen[candidate] = struct{}{}
				paths = append(paths, candidate)
			}
		}
	}
	return paths
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
