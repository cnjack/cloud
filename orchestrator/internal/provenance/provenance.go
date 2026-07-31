package provenance

import (
	"context"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const (
	PrecisionExact          = "exact"
	PrecisionLinkedExternal = "linked_external"
	PrecisionRuleOwner      = "rule_owner"
	PrecisionUnattributed   = "unattributed"
)

type TriggerRef struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Ref   string `json:"ref,omitempty"`
	Href  string `json:"href,omitempty"`
}

type ExecutedFor struct {
	ProjectID    string `json:"project_id"`
	ProjectLabel string `json:"project_label"`
	ServiceID    string `json:"service_id"`
	ServiceLabel string `json:"service_label"`
	Repository   string `json:"repository,omitempty"`
	Model        string `json:"model,omitempty"`
}

type RunProvenance struct {
	RequestedActor    *domain.ProvenanceActorRef `json:"requested_actor,omitempty"`
	AccountableActor  *domain.ProvenanceActorRef `json:"accountable_actor,omitempty"`
	AttributionSource string                     `json:"attribution_source"`
	Precision         string                     `json:"precision"`
	Trigger           TriggerRef                 `json:"trigger"`
	ExecutedFor       ExecutedFor                `json:"executed_for"`
	RuntimePrincipal  domain.ProvenanceActorRef  `json:"runtime_principal"`
	WritebackActor    *domain.ProvenanceActorRef `json:"writeback_actor,omitempty"`
}

type ExternalActor struct {
	Provider          string
	ID                string
	Label             string
	Source            string
	AccountableUserID string
}

// Stamp freezes identity facts before CreateRun. Any lookup failure degrades to
// an explicit unattributed/rule-owner snapshot; it never invents a user.
func Stamp(ctx context.Context, st store.Store, run *domain.Run, external *ExternalActor) {
	if run == nil || !run.ProvenanceSnapshot.Empty() {
		return
	}
	snapshot := domain.RunProvenanceSnapshot{
		AttributionSource: attributionSource(run, external),
		Precision:         PrecisionUnattributed,
		RuntimePrincipal: domain.ProvenanceActorRef{
			Kind: "service_principal", Label: "Cloud Service",
		},
	}
	if run.OriginAutomationID != "" || run.Origin == domain.RunOriginSchedule ||
		run.Origin == domain.RunOriginKanban || run.Origin == domain.RunOriginAutomation {
		snapshot.RuntimePrincipal = domain.ProvenanceActorRef{
			Kind: "automation_principal", Label: "Cloud Automation",
		}
	}

	hasExternalActor := external != nil && strings.TrimSpace(external.Label) != ""
	isRuleExecution := run.OriginAutomationID != "" &&
		(run.Origin == domain.RunOriginAutomation ||
			run.Origin == domain.RunOriginSchedule ||
			run.Origin == domain.RunOriginKanban)
	isManualAutomation := run.OriginAutomationID != "" && run.Origin == domain.RunOriginAPI
	if run.TriggeredByUserID != nil && *run.TriggeredByUserID != "" &&
		(!isRuleExecution || hasExternalActor) {
		actor := cloudUserActor(ctx, st, *run.TriggeredByUserID)
		if hasExternalActor {
			actor.Provider = external.Provider
			actor.ExternalID = external.ID
			actor.ExternalLabel = external.Label
		}
		snapshot.RequestedActor = &actor
		if !isManualAutomation {
			snapshot.AccountableActor = cloneActor(actor)
		}
		snapshot.Precision = PrecisionExact
	} else if hasExternalActor {
		snapshot.RequestedActor = &domain.ProvenanceActorRef{
			Kind: "external_actor", ID: external.ID, Label: external.Label,
			Provider: external.Provider,
		}
		snapshot.Precision = PrecisionLinkedExternal
	}

	if run.OriginAutomationID != "" && snapshot.AccountableActor == nil {
		if spec, err := st.GetPluginAutomationSpec(ctx, run.OriginAutomationID); err == nil &&
			spec.Automation.CreatedBy != "" {
			actor := cloudUserActor(ctx, st, spec.Automation.CreatedBy)
			snapshot.AccountableActor = &actor
			snapshot.Precision = PrecisionRuleOwner
		} else if run.TriggeredByUserID != nil && *run.TriggeredByUserID != "" {
			// Legacy SCM Automations stored their rule owner in triggered_by.
			// Keep that authorization field untouched, but project it as
			// accountable—not as a human who directly requested this event.
			actor := cloudUserActor(ctx, st, *run.TriggeredByUserID)
			snapshot.AccountableActor = &actor
			snapshot.Precision = PrecisionRuleOwner
		}
	}
	if snapshot.AccountableActor == nil && external != nil && external.AccountableUserID != "" {
		actor := cloudUserActor(ctx, st, external.AccountableUserID)
		snapshot.AccountableActor = &actor
		snapshot.Precision = PrecisionRuleOwner
	}
	snapshot.WritebackActor = writebackActor(ctx, st, run)
	run.ProvenanceSnapshot = snapshot
}

func Resolve(ctx context.Context, st store.Store, run *domain.Run) RunProvenance {
	if run == nil {
		return RunProvenance{Precision: PrecisionUnattributed}
	}
	snapshot := run.ProvenanceSnapshot
	if snapshot.Empty() {
		copy := *run
		Stamp(ctx, st, &copy, nil)
		snapshot = copy.ProvenanceSnapshot
	}
	out := RunProvenance{
		RequestedActor: snapshot.RequestedActor, AccountableActor: snapshot.AccountableActor,
		AttributionSource: snapshot.AttributionSource, Precision: snapshot.Precision,
		RuntimePrincipal: snapshot.RuntimePrincipal, WritebackActor: snapshot.WritebackActor,
		Trigger: triggerRef(run),
		ExecutedFor: ExecutedFor{
			ProjectID: run.ProjectID, ServiceID: run.ServiceID, Model: run.ModelName,
		},
	}
	if out.Precision == "" {
		out.Precision = PrecisionUnattributed
	}
	if out.AttributionSource == "" {
		out.AttributionSource = "legacy"
	}
	if out.RuntimePrincipal.Kind == "" {
		out.RuntimePrincipal = domain.ProvenanceActorRef{
			Kind: "service_principal", Label: "Cloud Service",
		}
	}
	if project, err := st.GetProject(ctx, run.ProjectID); err == nil {
		out.ExecutedFor.ProjectLabel = project.Name
	}
	if service, err := st.GetService(ctx, run.ServiceID); err == nil {
		out.ExecutedFor.ServiceLabel = service.Name
		if service.RepoKind == domain.RepoKindProvider {
			out.ExecutedFor.Repository = service.RepoOwnerName
		} else {
			out.ExecutedFor.Repository = service.RawRepoURL
		}
	}
	return out
}

func cloudUserActor(ctx context.Context, st store.Store, id string) domain.ProvenanceActorRef {
	actor := domain.ProvenanceActorRef{Kind: "cloud_user", ID: id, Label: "Former member"}
	if user, err := st.GetUser(ctx, id); err == nil {
		actor.Label = strings.TrimSpace(user.DisplayName)
		if actor.Label == "" {
			actor.Label = id
		}
	}
	return actor
}

func cloneActor(actor domain.ProvenanceActorRef) *domain.ProvenanceActorRef {
	copy := actor
	return &copy
}

func attributionSource(run *domain.Run, external *ExternalActor) string {
	if external != nil && external.Source != "" {
		return external.Source
	}
	switch run.Origin {
	case domain.RunOriginKanban:
		return "kanban_event"
	case domain.RunOriginSchedule:
		return "automation_rule"
	case domain.RunOriginAutomation:
		return "scm_event"
	case domain.RunOriginWebhook:
		return "scm_comment"
	default:
		if run.OriginAutomationID != "" {
			return "manual_automation"
		}
		return "direct_user"
	}
}

func triggerRef(run *domain.Run) TriggerRef {
	switch run.Origin {
	case domain.RunOriginKanban:
		return TriggerRef{Kind: "kanban_card", Label: "JType Card", Ref: run.OriginEventKey}
	case domain.RunOriginSchedule:
		return TriggerRef{Kind: "cron", Label: "Scheduled Automation", Ref: run.OriginAutomationID}
	case domain.RunOriginAutomation:
		return TriggerRef{
			Kind: "scm_event", Label: "Repository event",
			Ref: run.OriginEventKey, Href: run.PRURL,
		}
	case domain.RunOriginWebhook:
		return TriggerRef{Kind: "scm_comment", Label: "PR comment", Ref: run.OriginCommentID, Href: run.OriginCommentURL}
	default:
		if run.OriginAutomationID != "" {
			return TriggerRef{Kind: "manual_automation", Label: "Automation Run now", Ref: run.OriginAutomationID}
		}
		return TriggerRef{Kind: "api", Label: "Cloud Console / API"}
	}
}

func writebackActor(ctx context.Context, st store.Store, run *domain.Run) *domain.ProvenanceActorRef {
	switch run.Origin {
	case domain.RunOriginKanban:
		return &domain.ProvenanceActorRef{
			Kind: "provider_bot", Label: "jcode Cloud Bot", Provider: "jtype",
		}
	case domain.RunOriginAutomation, domain.RunOriginWebhook:
		return providerBotActor(ctx, st, run.ServiceID)
	default:
		if run.Kind == domain.RunKindReview && (run.PRURL != "" || run.PRHeadBranch != "") {
			return providerBotActor(ctx, st, run.ServiceID)
		}
		return nil
	}
}

func providerBotActor(ctx context.Context, st store.Store, serviceID string) *domain.ProvenanceActorRef {
	provider := ""
	if service, err := st.GetService(ctx, serviceID); err == nil {
		provider = string(service.Provider)
	}
	label := "jcode Cloud Bot"
	if provider != "" {
		label += " · " + provider
	}
	return &domain.ProvenanceActorRef{
		Kind: "provider_bot", Label: label, Provider: provider,
	}
}
