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

func TestDeriveWorkflowContractModelOverrideChangesOnlyLLMSelection(t *testing.T) {
	svc := Service{ID: "svc", ProjectID: "project", RepoKind: RepoKindProvider, Provider: ProviderGitHub,
		RepoOwnerName: "cnjack/cloud", DefaultBranch: "main", GitMode: GitModeDraftPR,
		PRReadyPolicy: PRReadyPolicyLifecycleAware}
	originalModelID := "model-a"
	run := Run{ID: "review", Kind: RunKindReview, Origin: RunOriginAutomation,
		ModelID: &originalModelID, ModelName: "provider/model-a", ModelEffort: "high",
		PRNumber: 24, PRHeadBranch: "feature", PRBaseBranch: "main"}
	original, err := ResolveWorkflowContract(&run, &svc, 3600, TimeoutSourceProject, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	originalHash := original.Hash
	derivedAt := time.Unix(2, 0).UTC()
	derived, err := DeriveWorkflowContractModelOverride(original, "model-b", "provider/model-b", derivedAt)
	if err != nil {
		t.Fatal(err)
	}

	if derived == original {
		t.Fatal("derivation mutated the original contract in place")
	}
	if original.Hash != originalHash || original.Execution.LLMSelection.ModelID != originalModelID {
		t.Fatalf("original contract changed: hash=%q model=%q", original.Hash, original.Execution.LLMSelection.ModelID)
	}
	if derived.Execution.LLMSelection.ModelID != "model-b" || derived.Execution.LLMSelection.ModelName != "provider/model-b" {
		t.Fatalf("derived model = %q/%q", derived.Execution.LLMSelection.ModelID, derived.Execution.LLMSelection.ModelName)
	}
	if derived.Execution.LLMSelection.Effort != "high" || derived.Execution.LLMSelection.Source != WorkflowLLMSourceRetryOverride {
		t.Fatalf("derived effort/source = %q/%q", derived.Execution.LLMSelection.Effort, derived.Execution.LLMSelection.Source)
	}
	if !derived.ResolvedAt.Equal(derivedAt) || derived.Hash == original.Hash {
		t.Fatalf("derived resolved_at/hash = %s/%q", derived.ResolvedAt, derived.Hash)
	}
	if derived.Trigger != original.Trigger || derived.Workflow != original.Workflow || derived.Profile != original.Profile {
		t.Fatal("derivation changed frozen workflow identity")
	}
	if err := derived.Validate(); err != nil {
		t.Fatalf("derived contract invalid: %v", err)
	}

	// Prove the helper deep-copies slices instead of aliasing the original.
	derived.Requirements[0] = "changed"
	derived.Delivery.Outputs[0].Target = "changed"
	if original.Requirements[0] == "changed" || original.Delivery.Outputs[0].Target == "changed" {
		t.Fatal("derived contract aliases original slices")
	}
}

func TestDeriveWorkflowContractModelOverrideRejectsInvalidInputs(t *testing.T) {
	if _, err := DeriveWorkflowContractModelOverride(nil, "model-b", "provider/model-b", time.Now()); err == nil {
		t.Fatal("nil original accepted")
	}
	svc := Service{RepoKind: RepoKindRaw, RawRepoURL: "https://example.invalid/repo.git", DefaultBranch: "main", GitMode: GitModeReadonly}
	run := Run{ID: "r", Kind: RunKindAgent, Origin: RunOriginAPI, ModelName: "provider/model-a"}
	original, err := ResolveWorkflowContract(&run, &svc, 3600, TimeoutSourceProject, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveWorkflowContractModelOverride(original, "", "provider/model-b", time.Now()); err == nil {
		t.Fatal("empty model id accepted")
	}
	if _, err := DeriveWorkflowContractModelOverride(original, "model-b", "", time.Now()); err == nil {
		t.Fatal("empty model name accepted")
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
