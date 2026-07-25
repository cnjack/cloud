package scmevent

import "sort"

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
	case ProviderGitLab:
		min = "17.11"
		// GitLab exposes approvals as MR lifecycle actions and has no portable
		// equivalent of GitHub's dismissed review.
		supported[FamilyReview] = []Action{ActionApproved, ActionApprovalRemoved, ActionCommented}
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
