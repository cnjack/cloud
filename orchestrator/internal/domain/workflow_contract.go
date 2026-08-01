package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	WorkflowContractSchemaVersion = 1

	BuiltinImplementationTaskID = "builtin:implementation-task"
	BuiltinPullRequestReviewID  = "builtin:pull-request-review"
	BuiltinDeveloperProfileID   = "builtin:developer"
	BuiltinReviewerProfileID    = "builtin:reviewer"

	OutputDiffOnly          = "diff_only"
	OutputCreatePullRequest = "create_pull_request"
	OutputProviderReview    = "provider_review"

	WorkspaceReadOnly    = "read_only"
	WorkspaceReadWrite   = "read_write"
	TimeoutSourceProject = "project_override"
	TimeoutSourceCluster = "cluster_default"

	RequirementSourceRead          = "source.read"
	RequirementSourceWrite         = "source.write"
	RequirementGit                 = "git"
	RequirementShell               = "shell"
	RequirementRipgrep             = "ripgrep"
	RequirementSCMPullRequestWrite = "scm.pull_request.write"
	RequirementSCMReviewWrite      = "scm.review.write"
)

var validWorkflowRequirements = []string{
	RequirementGit, RequirementRipgrep, RequirementSCMPullRequestWrite,
	RequirementSCMReviewWrite, RequirementShell, RequirementSourceRead,
	RequirementSourceWrite,
}

type WorkflowContract struct {
	SchemaVersion int                  `json:"schema_version"`
	Hash          string               `json:"hash"`
	Workflow      WorkflowIdentity     `json:"workflow"`
	Profile       AgentProfileSnapshot `json:"profile"`
	Trigger       WorkflowTrigger      `json:"trigger"`
	Execution     WorkflowExecution    `json:"execution"`
	Delivery      WorkflowDelivery     `json:"delivery"`
	Verification  WorkflowVerification `json:"verification"`
	Requirements  []string             `json:"requirements"`
	ResolvedAt    time.Time            `json:"resolved_at"`
}

type WorkflowIdentity struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Revision       int    `json:"revision"`
	Source         string `json:"source"`
	DefinitionHash string `json:"definition_hash"`
}

type AgentProfileSnapshot struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Revision     int    `json:"revision"`
	Instructions string `json:"instructions"`
}

type WorkflowTrigger struct {
	Kind             string    `json:"kind"`
	Origin           RunOrigin `json:"origin"`
	RequestID        string    `json:"request_id,omitempty"`
	ReceiptID        string    `json:"receipt_id,omitempty"`
	Actor            string    `json:"actor,omitempty"`
	Repository       string    `json:"repository,omitempty"`
	Object           string    `json:"object,omitempty"`
	Action           string    `json:"action,omitempty"`
	Ref              string    `json:"ref,omitempty"`
	AutomationID     string    `json:"automation_id,omitempty"`
	IdempotencyKey   string    `json:"idempotency_key"`
	ConcurrencyGroup string    `json:"concurrency_group"`
}

type WorkflowExecution struct {
	RunKind             RunKind              `json:"run_kind"`
	LLMSelection        WorkflowLLMSelection `json:"llm_selection"`
	Session             bool                 `json:"session"`
	PermissionMode      string               `json:"permission_mode"`
	WorkspaceAccess     string               `json:"workspace_access"`
	ProviderCredentials string               `json:"provider_credentials"`
	BaseRef             string               `json:"base_ref,omitempty"`
	TimeoutSeconds      int64                `json:"timeout_seconds"`
	TimeoutSource       string               `json:"timeout_source"`
}

type WorkflowLLMSelection struct {
	ModelID   string `json:"model_id,omitempty"`
	ModelName string `json:"model_name"`
	Effort    string `json:"effort,omitempty"`
	Source    string `json:"source"`
}

type WorkflowDelivery struct {
	Outputs []WorkflowOutput `json:"outputs"`
	Merge   string           `json:"merge"`
}

type WorkflowOutput struct {
	Type        string        `json:"type"`
	Target      string        `json:"target,omitempty"`
	ReadyPolicy PRReadyPolicy `json:"ready_policy,omitempty"`
}

type WorkflowVerification struct {
	Mode              string   `json:"mode"`
	RulesRevision     string   `json:"rules_revision,omitempty"`
	MaxFindings       int      `json:"max_findings,omitempty"`
	MinimumConfidence int      `json:"minimum_confidence,omitempty"`
	RequiredRecords   []string `json:"required_records,omitempty"`
}

// Delivers reports whether the immutable contract authorizes an output type.
// Runtime delivery code must consult this projection instead of re-reading a
// Service policy which may have changed after the Run was created.
func (c *WorkflowContract) Delivers(outputType string) bool {
	if c == nil {
		return false
	}
	for _, output := range c.Delivery.Outputs {
		if output.Type == outputType {
			return true
		}
	}
	return false
}

// DeliveryTarget returns the frozen target for an authorized output.
func (c *WorkflowContract) DeliveryTarget(outputType string) string {
	if c == nil {
		return ""
	}
	for _, output := range c.Delivery.Outputs {
		if output.Type == outputType {
			return output.Target
		}
	}
	return ""
}

// DeliveryReadyPolicy returns the policy frozen with a pull-request output.
func (c *WorkflowContract) DeliveryReadyPolicy(outputType string) PRReadyPolicy {
	if c == nil {
		return ""
	}
	for _, output := range c.Delivery.Outputs {
		if output.Type == outputType {
			return output.ReadyPolicy
		}
	}
	return ""
}

type builtinWorkflowDefinition struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Revision     int      `json:"revision"`
	ProfileID    string   `json:"profile_id"`
	Requirements []string `json:"requirements"`
	Outputs      []string `json:"outputs"`
	Prompt       string   `json:"prompt"`
}

var implementationDefinition = builtinWorkflowDefinition{
	ID: BuiltinImplementationTaskID, Name: "Implementation task", Revision: 1,
	ProfileID:    BuiltinDeveloperProfileID,
	Requirements: []string{RequirementSourceRead, RequirementSourceWrite, RequirementGit, RequirementShell, RequirementRipgrep},
	Outputs:      []string{OutputDiffOnly, OutputCreatePullRequest},
	Prompt:       "Implement the requested change in the frozen repository context, verify it proportionately, and emit only the delivery outputs allowed by the contract.",
}

var reviewDefinition = builtinWorkflowDefinition{
	ID: BuiltinPullRequestReviewID, Name: "Pull Request Review", Revision: 1,
	ProfileID:    BuiltinReviewerProfileID,
	Requirements: []string{RequirementSourceRead, RequirementGit, RequirementRipgrep, RequirementSCMReviewWrite},
	Outputs:      []string{OutputProviderReview},
	Prompt:       "Review only the exact frozen revision pair and anchor every finding to a changed right-side line in the server-accepted review plan.",
}

func ResolveWorkflowContract(run *Run, service *Service, timeoutSeconds int64, timeoutSource string, resolvedAt time.Time) (*WorkflowContract, error) {
	if run == nil || service == nil {
		return nil, errors.New("run and service are required")
	}
	if timeoutSeconds <= 0 {
		return nil, errors.New("effective timeout must be positive")
	}
	if timeoutSource != TimeoutSourceProject && timeoutSource != TimeoutSourceCluster {
		return nil, errors.New("invalid timeout source")
	}
	kind := run.Kind
	if kind == "" {
		kind = RunKindAgent
	}
	permissionMode := strings.TrimSpace(run.PermissionMode)
	if permissionMode == "" {
		permissionMode = "full_access"
	}
	baseRef := strings.TrimSpace(run.BaseBranch)
	if baseRef == "" {
		baseRef = strings.TrimSpace(service.DefaultBranch)
	}
	if kind == RunKindReview && run.PRBaseBranch != "" {
		baseRef = run.PRBaseBranch
	}
	contract := &WorkflowContract{
		SchemaVersion: WorkflowContractSchemaVersion,
		Trigger:       resolveWorkflowTrigger(run, service),
		Execution: WorkflowExecution{
			RunKind: kind, LLMSelection: WorkflowLLMSelection{ModelName: run.ModelName, Effort: run.ModelEffort, Source: "resolved_run"},
			Session: run.Session, PermissionMode: permissionMode, ProviderCredentials: "none", BaseRef: baseRef,
			TimeoutSeconds: timeoutSeconds, TimeoutSource: timeoutSource,
		},
		Delivery:   WorkflowDelivery{Merge: "never"},
		ResolvedAt: resolvedAt.UTC(),
	}
	if run.ModelID != nil {
		contract.Execution.LLMSelection.ModelID = *run.ModelID
	}
	if kind == RunKindReview {
		contract.Workflow = workflowIdentity(reviewDefinition)
		contract.Profile = AgentProfileSnapshot{ID: BuiltinReviewerProfileID, Name: "Reviewer", Role: "reviewer", Revision: 1,
			Instructions: "Review the exact frozen change and report only validated, changed-line findings."}
		contract.Execution.WorkspaceAccess = WorkspaceReadOnly
		contract.Delivery.Outputs = []WorkflowOutput{{Type: OutputProviderReview, Target: "trigger_pr"}}
		contract.Verification = WorkflowVerification{Mode: "structured_review", RulesRevision: "review-v2", MaxFindings: MaxReviewFindings, MinimumConfidence: 80}
		contract.Requirements = append([]string(nil), reviewDefinition.Requirements...)
	} else {
		contract.Workflow = workflowIdentity(implementationDefinition)
		contract.Profile = AgentProfileSnapshot{ID: BuiltinDeveloperProfileID, Name: "Developer", Role: "developer", Revision: 1,
			Instructions: "Implement the request in the frozen repository context and verify the resulting change."}
		contract.Execution.WorkspaceAccess = WorkspaceReadWrite
		contract.Verification = WorkflowVerification{Mode: "runner_reported"}
		contract.Requirements = append([]string(nil), implementationDefinition.Requirements...)
		if service.GitMode == GitModeDraftPR && service.RepoKind == RepoKindProvider {
			policy := run.PRReadyPolicy
			if policy == "" {
				policy = service.PRReadyPolicy
			}
			if policy == "" {
				policy = PRReadyPolicyAlwaysDraft
			}
			target := "service_repository"
			if run.Origin == RunOriginWebhook && run.PRNumber > 0 {
				target = "trigger_pr"
			}
			contract.Delivery.Outputs = []WorkflowOutput{{Type: OutputCreatePullRequest, Target: target, ReadyPolicy: policy}}
			contract.Requirements = append(contract.Requirements, RequirementSCMPullRequestWrite)
		} else {
			contract.Delivery.Outputs = []WorkflowOutput{{Type: OutputDiffOnly}}
		}
	}
	hash, err := contract.CanonicalHash()
	if err != nil {
		return nil, err
	}
	contract.Hash = hash
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	return contract, nil
}

func workflowIdentity(def builtinWorkflowDefinition) WorkflowIdentity {
	return WorkflowIdentity{ID: def.ID, Name: def.Name, Revision: def.Revision, Source: "builtin", DefinitionHash: hashJSON(def)}
}

func resolveWorkflowTrigger(run *Run, service *Service) WorkflowTrigger {
	kind := "api"
	switch run.Origin {
	case RunOriginKanban:
		kind = "jtype"
	case RunOriginSchedule:
		kind = "cron"
	case RunOriginAutomation, RunOriginWebhook:
		kind = "scm"
	case RunOriginAPI:
		if run.TriggeredByUserID != nil {
			kind = "manual"
		}
	}
	actor := ""
	if run.ProvenanceSnapshot.RequestedActor != nil {
		actor = run.ProvenanceSnapshot.RequestedActor.Kind + ":" + run.ProvenanceSnapshot.RequestedActor.ID
	} else if run.ProvenanceSnapshot.AccountableActor != nil {
		actor = run.ProvenanceSnapshot.AccountableActor.Kind + ":" + run.ProvenanceSnapshot.AccountableActor.ID
	} else if run.TriggeredByUserID != nil {
		actor = "cloud_user:" + *run.TriggeredByUserID
	}
	repository := service.RepoOwnerName
	if repository == "" {
		repository = service.RawRepoURL
	}
	object := ""
	if run.PRNumber > 0 {
		object = fmt.Sprintf("pull_request:%d", run.PRNumber)
	}
	idempotency := run.OriginEventKey
	if idempotency == "" {
		idempotency = run.OriginCommentID
	}
	if idempotency == "" {
		idempotency = run.ID
	}
	ref := run.BaseBranch
	if run.PRHeadBranch != "" {
		ref = run.PRHeadBranch
	}
	group := service.ID
	if object != "" {
		group += ":" + object
	}
	return WorkflowTrigger{Kind: kind, Origin: run.Origin, RequestID: run.ID, ReceiptID: firstNonEmpty(run.OriginEventKey, run.OriginCommentID),
		Actor: actor, Repository: repository, Object: object, Ref: ref, AutomationID: run.OriginAutomationID,
		IdempotencyKey: idempotency, ConcurrencyGroup: group}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c WorkflowContract) CanonicalHash() (string, error) {
	view := struct {
		SchemaVersion int                  `json:"schema_version"`
		Workflow      WorkflowIdentity     `json:"workflow"`
		Profile       AgentProfileSnapshot `json:"profile"`
		Trigger       WorkflowTrigger      `json:"trigger"`
		Execution     WorkflowExecution    `json:"execution"`
		Delivery      WorkflowDelivery     `json:"delivery"`
		Verification  WorkflowVerification `json:"verification"`
		Requirements  []string             `json:"requirements"`
	}{c.SchemaVersion, c.Workflow, c.Profile, c.Trigger, c.Execution, c.Delivery, c.Verification, c.Requirements}
	data, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("encode workflow contract: %w", err)
	}
	return sha256String(data), nil
}

func (c WorkflowContract) Validate() error {
	if c.SchemaVersion != WorkflowContractSchemaVersion {
		return errors.New("unsupported workflow contract schema")
	}
	if c.Execution.TimeoutSeconds <= 0 {
		return errors.New("workflow timeout must be positive")
	}
	if c.Execution.TimeoutSource != TimeoutSourceProject && c.Execution.TimeoutSource != TimeoutSourceCluster {
		return errors.New("invalid workflow timeout source")
	}
	if c.Execution.ProviderCredentials != "none" {
		return errors.New("provider credentials may not enter the Runner")
	}
	if c.Delivery.Merge != "never" || len(c.Delivery.Outputs) != 1 {
		return errors.New("workflow must have exactly one non-merge delivery output")
	}
	for _, req := range c.Requirements {
		if !slices.Contains(validWorkflowRequirements, req) {
			return fmt.Errorf("unknown workflow requirement %q", req)
		}
	}
	var def builtinWorkflowDefinition
	switch c.Workflow.ID {
	case BuiltinImplementationTaskID:
		def = implementationDefinition
		if c.Workflow.Revision != 1 || c.Profile.ID != BuiltinDeveloperProfileID || c.Execution.RunKind != RunKindAgent || c.Execution.WorkspaceAccess != WorkspaceReadWrite {
			return errors.New("invalid implementation workflow mapping")
		}
		out := c.Delivery.Outputs[0]
		if out.Type != OutputDiffOnly && out.Type != OutputCreatePullRequest {
			return errors.New("implementation workflow has an invalid output")
		}
		if out.Type == OutputCreatePullRequest && !ValidPRReadyPolicy(out.ReadyPolicy) {
			return errors.New("pull-request output requires ready_policy")
		}
		if out.Type == OutputDiffOnly && out.ReadyPolicy != "" {
			return errors.New("diff output forbids ready_policy")
		}
	case BuiltinPullRequestReviewID:
		def = reviewDefinition
		out := c.Delivery.Outputs[0]
		if c.Workflow.Revision != 1 || c.Profile.ID != BuiltinReviewerProfileID || c.Execution.RunKind != RunKindReview || c.Execution.WorkspaceAccess != WorkspaceReadOnly || out.Type != OutputProviderReview || out.ReadyPolicy != "" {
			return errors.New("invalid review workflow mapping")
		}
		if c.Verification.Mode != "structured_review" || c.Verification.RulesRevision != "review-v2" {
			return errors.New("invalid review verification contract")
		}
	default:
		return errors.New("unknown built-in workflow")
	}
	if c.Workflow.DefinitionHash != hashJSON(def) {
		return errors.New("workflow definition hash mismatch")
	}
	want, err := c.CanonicalHash()
	if err != nil {
		return err
	}
	if c.Hash != want {
		return errors.New("workflow contract hash mismatch")
	}
	return nil
}

func hashJSON(value any) string {
	data, _ := json.Marshal(value)
	return sha256String(data)
}

func sha256String(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
