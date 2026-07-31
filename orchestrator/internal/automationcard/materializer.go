package automationcard

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/store"
)

type documentAPI interface {
	ListDocuments(ctx context.Context, workspace string) ([]jtype.Doc, error)
	GetDocument(ctx context.Context, workspace, id string) (*jtype.Document, error)
	SaveDocument(ctx context.Context, workspace, path, content, baseContentHash string) error
}

type Materializer struct {
	st        store.Store
	decrypt   func([]byte) (string, error)
	clientFor func(baseURL, token string) documentAPI
}

func New(st store.Store, decrypt func([]byte) (string, error)) *Materializer {
	return &Materializer{
		st: st, decrypt: decrypt,
		clientFor: func(baseURL, token string) documentAPI {
			return jtype.NewClient(baseURL, token, 20*time.Second)
		},
	}
}

// Materialize creates or recovers one deterministic Card. allowSave is true
// only for the process that changed planned -> creating. A restarted creating
// execution may resolve an existing Card but can never issue another save.
func (m *Materializer) Materialize(ctx context.Context, execution domain.AutomationExecution, allowSave bool) domain.AutomationCardMaterializationResult {
	result := domain.AutomationCardMaterializationResult{DocumentPath: execution.CardPath, CardState: "creating"}
	spec, installation, client, blocked := m.resolveTarget(ctx, execution)
	if blocked.ReasonCode != "" {
		blocked.DocumentPath = execution.CardPath
		return blocked
	}
	result.CardAutomationID = spec.Automation.ID
	result.WorkspaceID = installation.WorkspaceID

	docs, err := client.ListDocuments(ctx, installation.WorkspaceID)
	if err != nil {
		if allowSave {
			result.CardState = "planned"
		}
		return retryableResult(result, "card_read_unavailable", "JType cards could not be read.", "project_owner")
	}
	if doc := findPath(docs, execution.CardPath); doc != nil {
		return m.bindExisting(ctx, client, execution, result, *doc)
	}
	if !allowSave {
		return blockedResult(result, "card_creation_uncertain", "Cloud cannot prove whether JType created this Card. It will not create another one.", "project_owner")
	}

	content := cardContent(spec.Kanban.BoardRef, spec.Kanban.TriggerColumn, execution)
	if err := client.SaveDocument(ctx, installation.WorkspaceID, execution.CardPath, content, ""); err != nil {
		return retryableResult(result, "card_write_unavailable", "JType did not confirm Card creation. Cloud will only try to resolve the same path.", "project_owner")
	}
	docs, err = client.ListDocuments(ctx, installation.WorkspaceID)
	if err != nil {
		return retryableResult(result, "card_creation_unconfirmed", "JType accepted the save, but Cloud could not resolve the Card yet.", "project_owner")
	}
	doc := findPath(docs, execution.CardPath)
	if doc == nil {
		return retryableResult(result, "card_creation_unconfirmed", "JType accepted the save, but the Card path is not visible yet.", "project_owner")
	}
	return m.bindExisting(ctx, client, execution, result, *doc)
}

func (m *Materializer) resolveTarget(
	ctx context.Context,
	execution domain.AutomationExecution,
) (*domain.PluginAutomationSpec, *domain.PluginInstallation, documentAPI, domain.AutomationCardMaterializationResult) {
	automations, err := m.st.ListPluginAutomationsByProject(ctx, execution.ProjectID)
	if err != nil {
		return nil, nil, nil, blockedResult(domain.AutomationCardMaterializationResult{}, "kanban_policy_unavailable", "Service Kanban policy could not be read.", "project_owner")
	}
	var spec *domain.PluginAutomationSpec
	for i := range automations {
		candidate := automations[i]
		if candidate.ServiceID != execution.ServiceID || candidate.TriggerKind != "kanban" || !candidate.Enabled {
			continue
		}
		loaded, loadErr := m.st.GetPluginAutomationSpec(ctx, candidate.ID)
		if loadErr == nil && loaded.Kanban != nil {
			spec = loaded
			break
		}
	}
	if spec == nil {
		return nil, nil, nil, blockedResult(domain.AutomationCardMaterializationResult{}, "kanban_not_configured", "Enable Service Kanban before using Card output.", "project_owner")
	}
	installation, err := m.st.GetPluginInstallation(ctx, spec.Kanban.InstallationID)
	if err != nil || installation.Status != domain.PluginStatusEnabled ||
		installation.Provider != domain.PluginJType || installation.WorkspaceID == "" ||
		installation.LastHealthError != "" {
		return nil, nil, nil, blockedResult(domain.AutomationCardMaterializationResult{}, "jtype_unavailable", "Reconnect the Project JType plugin.", "project_owner")
	}
	cfg, err := m.st.GetProviderConfig(ctx, domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || cfg.BaseURL == "" || cfg.LastHealthError != "" ||
		cfg.ConfigRevision != installation.ConfigRevision {
		return nil, nil, nil, blockedResult(domain.AutomationCardMaterializationResult{}, "jtype_provider_unavailable", "The cluster JType Provider needs attention.", "cluster_admin")
	}
	token, _, err := jtype.ResolveToken(installation.AccessTokenEnc, m.decrypt)
	if err != nil {
		return nil, nil, nil, blockedResult(domain.AutomationCardMaterializationResult{}, "jtype_credential_unavailable", "Reconnect the Project JType plugin.", "project_owner")
	}
	return spec, installation, m.clientFor(cfg.BaseURL, token), domain.AutomationCardMaterializationResult{}
}

func (m *Materializer) bindExisting(
	ctx context.Context,
	client documentAPI,
	execution domain.AutomationExecution,
	result domain.AutomationCardMaterializationResult,
	doc jtype.Doc,
) domain.AutomationCardMaterializationResult {
	full, err := client.GetDocument(ctx, result.WorkspaceID, doc.ID)
	if err != nil {
		return retryableResult(result, "card_read_unavailable", "The deterministic Card path exists but cannot be read yet.", "project_owner")
	}
	if !strings.Contains(full.Content, executionMarker(execution.ID)) {
		return blockedResult(result, "card_path_conflict", "The deterministic Card path is occupied by a different document.", "project_owner")
	}
	result.DocumentID = doc.ID
	result.DocumentPath = doc.Path
	result.CardState = "bound"
	return result
}

func blockedResult(result domain.AutomationCardMaterializationResult, code, message, role string) domain.AutomationCardMaterializationResult {
	result.CardState = "unavailable"
	result.ReasonCode = code
	result.ReasonMessage = message
	result.RepairRole = role
	return result
}

func retryableResult(result domain.AutomationCardMaterializationResult, code, message, role string) domain.AutomationCardMaterializationResult {
	if result.CardState == "" {
		result.CardState = "creating"
	}
	result.ReasonCode = code
	result.ReasonMessage = message
	result.RepairRole = role
	return result
}

func findPath(docs []jtype.Doc, wanted string) *jtype.Doc {
	for i := range docs {
		if docs[i].Path == wanted {
			return &docs[i]
		}
	}
	return nil
}

func executionMarker(id string) string {
	return "<!-- jcode-cloud-automation-execution:" + id + " -->"
}

func cardContent(board, status string, execution domain.AutomationExecution) string {
	title := execution.AutomationName
	if title == "" {
		title = "Automation work"
	}
	return fmt.Sprintf("---\nboard: %s\nstatus: %s\ntitle: %s\n---\n%s\n\n%s\n",
		strconv.Quote(board), strconv.Quote(status), strconv.Quote(title),
		executionMarker(execution.ID), strings.TrimSpace(execution.PromptSnapshot))
}

func DeterministicPath(automationID, executionID string) string {
	return path.Join("jcode-automation", automationID, executionID+".md")
}
