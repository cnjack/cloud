// Package scmevent defines the provider-neutral event contract used by Plugin
// Automations. Provider payloads are normalized at the webhook boundary and
// only this deliberately small, whitelisted shape may be persisted.
package scmevent

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
)

type ProviderKind string

const (
	ProviderGitHub ProviderKind = "github"
	ProviderGitLab ProviderKind = "gitlab"
	ProviderGitea  ProviderKind = "gitea"
	ProviderJType  ProviderKind = "jtype"
)

func (p ProviderKind) Valid() bool {
	switch p {
	case ProviderGitHub, ProviderGitLab, ProviderGitea, ProviderJType:
		return true
	}
	return false
}

type Family string

const (
	FamilyPush        Family = "push"
	FamilyPullRequest Family = "pull_request"
	FamilyReview      Family = "review"
	FamilyComment     Family = "comment"
	FamilyIssue       Family = "issue"
	FamilyCheck       Family = "check"
	FamilyTag         Family = "tag"
	FamilyRelease     Family = "release"
)

type Action string

const (
	ActionUpdated          Action = "updated"
	ActionOpened           Action = "opened"
	ActionReopened         Action = "reopened"
	ActionSynchronized     Action = "synchronized"
	ActionReady            Action = "ready"
	ActionClosed           Action = "closed"
	ActionMerged           Action = "merged"
	ActionApproved         Action = "approved"
	ActionChangesRequested Action = "changes_requested"
	ActionCommented        Action = "commented"
	ActionDismissed        Action = "dismissed"
	ActionApprovalRemoved  Action = "approval_removed"
	ActionCreated          Action = "created"
	ActionCompleted        Action = "completed"
	ActionDeleted          Action = "deleted"
	ActionPublished        Action = "published"
)

type Actor struct {
	ID        string `json:"id"`
	Login     string `json:"login,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type Repository struct {
	ID            string `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	HTMLURL       string `json:"html_url,omitempty"`
}

type Object struct {
	ID     string `json:"id,omitempty"`
	Number int64  `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

// NormalizedSCMEvent intentionally excludes a provider payload or headers. Body
// is populated only for a newly-created @jcode comment and contains the complete
// comment, as approved by the product contract.
type NormalizedSCMEvent struct {
	Provider         ProviderKind `json:"provider"`
	DeliveryID       string       `json:"delivery_id"`
	Family           Family       `json:"family"`
	Action           Action       `json:"action"`
	Actor            Actor        `json:"actor"`
	Repository       Repository   `json:"repository"`
	Object           Object       `json:"object,omitempty"`
	Ref              string       `json:"ref,omitempty"`
	BaseRef          string       `json:"base_ref,omitempty"`
	HeadSHA          string       `json:"head_sha,omitempty"`
	Conclusion       string       `json:"conclusion,omitempty"`
	Body             string       `json:"body,omitempty"`
	OccurredAt       time.Time    `json:"occurred_at"`
	Correlation      string       `json:"correlation,omitempty"`
	GeneratedByJCode bool         `json:"generated_by_jcode,omitempty"`
	// ChangedPaths is used only while matching Automations. It is intentionally
	// excluded from the persisted receipt and prompt-safe JSON contract.
	ChangedPaths []string `json:"-"`
}

func (e NormalizedSCMEvent) Validate() error {
	if !e.Provider.Valid() || e.Provider == ProviderJType {
		return errors.New("invalid SCM provider")
	}
	if strings.TrimSpace(e.DeliveryID) == "" {
		return errors.New("delivery_id is required")
	}
	if !ValidAction(e.Family, e.Action) {
		return errors.New("unsupported event family/action")
	}
	if strings.TrimSpace(e.Repository.ID) == "" || strings.TrimSpace(e.Repository.FullName) == "" {
		return errors.New("repository id and full_name are required")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	if e.Family == FamilyComment {
		if e.Action != ActionCreated || !ContainsJCodeMention(e.Body) {
			return errors.New("comment events require a newly-created @jcode comment")
		}
	} else if e.Body != "" {
		return errors.New("body is only allowed for @jcode comments")
	}
	return nil
}

func ValidAction(f Family, a Action) bool {
	switch f {
	case FamilyPush:
		return a == ActionUpdated
	case FamilyPullRequest:
		return a == ActionOpened || a == ActionReopened || a == ActionSynchronized ||
			a == ActionReady || a == ActionClosed || a == ActionMerged
	case FamilyReview:
		return a == ActionApproved || a == ActionChangesRequested || a == ActionCommented ||
			a == ActionDismissed || a == ActionApprovalRemoved
	case FamilyComment:
		return a == ActionCreated
	case FamilyIssue:
		return a == ActionOpened || a == ActionReopened || a == ActionUpdated || a == ActionClosed
	case FamilyCheck:
		return a == ActionCompleted
	case FamilyTag:
		return a == ActionCreated || a == ActionDeleted
	case FamilyRelease:
		return a == ActionPublished || a == ActionUpdated || a == ActionDeleted
	}
	return false
}

var jcodeMentionPattern = regexp.MustCompile(`(?i)(^|[^[:alnum:]_.-])@jcode([^[:alnum:]_-]|$)`)

func ContainsJCodeMention(body string) bool { return jcodeMentionPattern.MatchString(body) }

// CoalesceKey is empty for events that must run individually. Push, pull
// request synchronization and check completion retain one running event plus
// the newest queued event for the returned key.
func CoalesceKey(serviceID string, e NormalizedSCMEvent) string {
	var object string
	switch {
	case e.Family == FamilyPush:
		object = e.Ref
	case e.Family == FamilyPullRequest && e.Action == ActionSynchronized:
		object = e.Object.ID
		if object == "" {
			object = stringInt(e.Object.Number)
		}
	case e.Family == FamilyCheck:
		object = e.Object.ID + ":" + e.Ref
	default:
		return ""
	}
	if strings.TrimSpace(object) == "" {
		return ""
	}
	return strings.Join([]string{serviceID, string(e.Family), object}, ":")
}

type Filter struct {
	Branch       string
	IncludePaths []string
	ExcludePaths []string
	Conclusions  []string
}

// Matches applies the deliberately small first-release filter vocabulary.
// Changed paths are supplied by the normalizer only when the provider includes
// them; include-path filters fail closed when no path information is available.
func (f Filter) Matches(e NormalizedSCMEvent, changedPaths []string) bool {
	if f.Branch != "" {
		var branch string
		switch e.Family {
		case FamilyPush, FamilyCheck:
			branch = e.Ref
		case FamilyPullRequest:
			// Pull-request Automations are scoped to the destination branch;
			// source branches are contributor-controlled and are not the branch
			// the change will land in.
			branch = e.BaseRef
		}
		branch = strings.TrimPrefix(branch, "refs/heads/")
		if branch == "" || !matchesAny([]string{f.Branch}, branch) {
			return false
		}
	}
	if len(f.Conclusions) > 0 && !containsFold(f.Conclusions, e.Conclusion) {
		return false
	}
	if len(f.IncludePaths) > 0 {
		matched := false
		for _, changed := range changedPaths {
			if matchesAny(f.IncludePaths, changed) && !matchesAny(f.ExcludePaths, changed) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func matchesAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, value); ok {
			return true
		}
		// path.Match("src/**", "src/a/b") does not cross separators. Preserve
		// the common prefix form without introducing another glob dependency.
		if strings.HasSuffix(pattern, "/**") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "**")) {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func stringInt(v int64) string {
	if v == 0 {
		return ""
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
