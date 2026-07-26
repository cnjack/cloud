package scmevent

import (
	"sort"
	"strconv"
	"strings"
)

type Capability struct {
	Family  Family   `json:"family"`
	Actions []Action `json:"actions"`
}

type ProviderCapabilities struct {
	Provider       ProviderKind `json:"provider"`
	MinimumVersion string       `json:"minimum_version,omitempty"`
	Capabilities   []Capability `json:"capabilities"`
}

func Capabilities(provider ProviderKind) ProviderCapabilities {
	var min string
	supported := map[Family][]Action{
		FamilyPush:        {ActionUpdated},
		FamilyPullRequest: {ActionOpened, ActionReopened, ActionSynchronized, ActionReady, ActionClosed, ActionMerged},
		FamilyReview:      {ActionApproved, ActionChangesRequested, ActionCommented, ActionDismissed, ActionApprovalRemoved},
		FamilyComment:     {ActionCreated},
		FamilyIssue:       {ActionOpened, ActionReopened, ActionUpdated, ActionClosed},
		FamilyCheck:       {ActionCompleted},
		FamilyTag:         {ActionCreated, ActionDeleted},
		FamilyRelease:     {ActionPublished, ActionUpdated, ActionDeleted},
	}
	switch provider {
	case ProviderGitHub:
		// github.com is the sole supported GitHub instance.
		// GitHub reports an approved review being invalidated as the
		// pull_request_review "dismissed" action. It does not emit a second,
		// independently distinguishable approval-removed event, so advertising
		// both would create a selector that can never match.
		supported[FamilyReview] = []Action{ActionApproved, ActionChangesRequested, ActionCommented, ActionDismissed}
	case ProviderGitLab:
		min = "17.11"
		// GitLab exposes approvals as MR lifecycle actions and has no portable
		// equivalent of GitHub's dismissed review. A normal Note Hook is a
		// comment.created event, not an independently normalizable review.
		supported[FamilyReview] = []Action{ActionApproved, ActionApprovalRemoved}
		supported[FamilyPullRequest] = []Action{ActionOpened, ActionReopened, ActionSynchronized, ActionClosed, ActionMerged}
	case ProviderGitea:
		min = "1.25"
		supported[FamilyReview] = []Action{ActionApproved, ActionChangesRequested, ActionCommented}
		supported[FamilyPullRequest] = []Action{ActionOpened, ActionReopened, ActionSynchronized, ActionClosed, ActionMerged}
	case ProviderJType:
		return ProviderCapabilities{Provider: provider, MinimumVersion: "0.2", Capabilities: []Capability{}}
	default:
		return ProviderCapabilities{Provider: provider, Capabilities: []Capability{}}
	}

	families := make([]Family, 0, len(supported))
	for family := range supported {
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return families[i] < families[j] })
	out := ProviderCapabilities{Provider: provider, MinimumVersion: min, Capabilities: make([]Capability, 0, len(families))}
	for _, family := range families {
		out.Capabilities = append(out.Capabilities, Capability{Family: family, Actions: supported[family]})
	}
	return out
}

// CapabilitiesForVersion returns only event families that are safe for the
// provider release observed by the cluster probe. A missing version retains the
// declared matrix (GitHub is a hosted API and JType has no SCM actions); an
// unparseable or too-old self-hosted release fails visibly as no SCM support.
func CapabilitiesForVersion(provider ProviderKind, version string) ProviderCapabilities {
	out := Capabilities(provider)
	if out.MinimumVersion == "" {
		return out
	}
	if strings.TrimSpace(version) == "" {
		out.Capabilities = []Capability{}
		return out
	}
	if !versionAtLeast(version, out.MinimumVersion) {
		out.Capabilities = []Capability{}
	}
	return out
}

func versionAtLeast(raw, minimum string) bool {
	actual, ok := numericVersion(raw)
	if !ok {
		return false
	}
	need, ok := numericVersion(minimum)
	if !ok {
		return false
	}
	for i := 0; i < len(need); i++ {
		part := 0
		if i < len(actual) {
			part = actual[i]
		}
		if part != need[i] {
			return part > need[i]
		}
	}
	return true
}

func numericVersion(raw string) ([]int, bool) {
	raw = strings.TrimLeft(strings.TrimSpace(raw), "vV")
	parts := strings.Split(raw, ".")
	if len(parts) == 0 {
		return nil, false
	}
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		digits := part
		for i, r := range part {
			if r < '0' || r > '9' {
				digits = part[:i]
				break
			}
		}
		if digits == "" {
			return nil, false
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return nil, false
		}
		result = append(result, value)
	}
	return result, true
}

func (c ProviderCapabilities) Supports(family Family, action Action) bool {
	for _, capability := range c.Capabilities {
		if capability.Family != family {
			continue
		}
		for _, candidate := range capability.Actions {
			if candidate == action {
				return true
			}
		}
	}
	return false
}
