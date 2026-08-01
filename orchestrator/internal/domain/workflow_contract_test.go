package domain

import (
	"testing"
	"time"
)

func TestResolveWorkflowContractBuiltins(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	svc := Service{ID: "svc", ProjectID: "project", RepoKind: RepoKindProvider, Provider: ProviderGitHub,
		RepoOwnerName: "cnjack/cloud", DefaultBranch: "main", GitMode: GitModeDraftPR,
		PRReadyPolicy: PRReadyPolicyLifecycleAware}
	tests := []struct {
		name        string
		run         Run
		workflowID  string
		profileID   string
		output      string
		workspace   string
		requirement string
	}{
		{name: "developer", run: Run{ID: "agent", Kind: RunKindAgent, Origin: RunOriginKanban, ModelName: "zhipu/glm-5.2"},
			workflowID: BuiltinImplementationTaskID, profileID: BuiltinDeveloperProfileID,
			output: OutputCreatePullRequest, workspace: WorkspaceReadWrite, requirement: RequirementSourceWrite},
		{name: "reviewer", run: Run{ID: "review", Kind: RunKindReview, Origin: RunOriginAutomation,
			ModelName: "zhipu/glm-5.2", PRNumber: 24, PRHeadBranch: "feature", PRBaseBranch: "main"},
			workflowID: BuiltinPullRequestReviewID, profileID: BuiltinReviewerProfileID,
			output: OutputProviderReview, workspace: WorkspaceReadOnly, requirement: RequirementSCMReviewWrite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contract, err := ResolveWorkflowContract(&tc.run, &svc, 3600, TimeoutSourceProject, now)
			if err != nil {
				t.Fatal(err)
			}
			if contract.Workflow.ID != tc.workflowID || contract.Profile.ID != tc.profileID {
				t.Fatalf("identity = %s/%s", contract.Workflow.ID, contract.Profile.ID)
			}
			if contract.Delivery.Outputs[0].Type != tc.output || contract.Execution.WorkspaceAccess != tc.workspace {
				t.Fatalf("delivery/workspace = %s/%s", contract.Delivery.Outputs[0].Type, contract.Execution.WorkspaceAccess)
			}
			if !containsRequirement(contract.Requirements, tc.requirement) {
				t.Fatalf("missing %s", tc.requirement)
			}
			if contract.Execution.TimeoutSeconds != 3600 || contract.Execution.TimeoutSource != TimeoutSourceProject {
				t.Fatalf("timeout = %d/%s", contract.Execution.TimeoutSeconds, contract.Execution.TimeoutSource)
			}
			if err := contract.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if contract.Hash == "" || contract.Workflow.DefinitionHash == "" {
				t.Fatal("hashes were not resolved")
			}
		})
	}
}

func TestWorkflowContractHashExcludesResolutionTimeAndDetectsTamper(t *testing.T) {
	svc := Service{RepoKind: RepoKindRaw, RawRepoURL: "https://example.invalid/repo.git", DefaultBranch: "main", GitMode: GitModeReadonly}
	run := Run{ID: "r", Kind: RunKindAgent, Origin: RunOriginAPI, ModelName: "zhipu/glm-5.2"}
	a, err := ResolveWorkflowContract(&run, &svc, 43200, TimeoutSourceCluster, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveWorkflowContract(&run, &svc, 43200, TimeoutSourceCluster, time.Unix(2, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatalf("resolved_at changed hash: %s != %s", a.Hash, b.Hash)
	}
	a.Execution.TimeoutSeconds++
	if err := a.Validate(); err == nil {
		t.Fatal("tampered contract accepted")
	}
}

func TestWorkflowContractRejectsDeliveryConflict(t *testing.T) {
	svc := Service{RepoKind: RepoKindProvider, Provider: ProviderGitHub, RepoOwnerName: "cnjack/cloud", DefaultBranch: "main", GitMode: GitModeDraftPR, PRReadyPolicy: PRReadyPolicyLifecycleAware}
	run := Run{ID: "r", Kind: RunKindReview, Origin: RunOriginAPI, ModelName: "zhipu/glm-5.2"}
	c, err := ResolveWorkflowContract(&run, &svc, 43200, TimeoutSourceCluster, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	c.Delivery.Outputs[0].Type = OutputCreatePullRequest
	c.Hash, _ = c.CanonicalHash()
	if err := c.Validate(); err == nil {
		t.Fatal("review contract accepted pull-request delivery")
	}
}

func TestBuiltinWorkflowDefinitionHashesRequireExplicitRevisionBump(t *testing.T) {
	// If a built-in definition intentionally changes, bump its Revision and then
	// update this golden hash. This prevents a semantic edit from masquerading as
	// the same code-owned Workflow version in already persisted Run contracts.
	want := map[string]string{
		BuiltinImplementationTaskID: "sha256:3fafba335a9fea06aec3861c40f886d377f84b3198caf8540b1c8e3d599eae85",
		BuiltinPullRequestReviewID:  "sha256:8851abf6a4ba55cde7f37a8423ef93a1ff4e8659b6e010bc53c86030c53de62f",
	}
	for _, definition := range []builtinWorkflowDefinition{implementationDefinition, reviewDefinition} {
		if got := workflowIdentity(definition).DefinitionHash; got != want[definition.ID] {
			t.Fatalf("%s revision %d definition changed: got %s", definition.ID, definition.Revision, got)
		}
	}
}

func containsRequirement(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
