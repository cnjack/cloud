package store

import (
	"fmt"

	"github.com/cnjack/jcloud/internal/domain"
)

// validatePluginAutomationAggregate keeps the Store boundary strict: a unified
// Automation has exactly one typed trigger matching TriggerKind. PostgreSQL
// also enforces this at commit time (migration 0044), while this helper makes
// the same invariant immediate and useful to memory-backed tests.
func validatePluginAutomationAggregate(a *domain.PluginAutomation, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger, cron *domain.CronTrigger) error {
	if a == nil || !domain.ValidPluginAutomationTrigger(a.TriggerKind) {
		return fmt.Errorf("invalid plugin automation trigger")
	}
	if a.RunKind == "" {
		a.RunKind = domain.RunKindAgent
	}
	if !domain.ValidRunKind(a.RunKind) {
		return fmt.Errorf("invalid plugin automation run kind")
	}
	switch a.ModelEffort {
	case "":
	case "low", "medium", "high":
		if a.ModelID == "" {
			return fmt.Errorf("plugin automation effort requires a model")
		}
	default:
		return fmt.Errorf("invalid plugin automation model effort")
	}
	children := 0
	if scm != nil {
		children++
	}
	if kanban != nil {
		children++
	}
	if cron != nil {
		children++
	}
	if children != 1 {
		return fmt.Errorf("plugin automation needs exactly one matching trigger")
	}
	switch a.TriggerKind {
	case "scm":
		if scm == nil || len(actions) == 0 || scm.AutomationID != a.ID {
			return fmt.Errorf("scm automation needs exactly one trigger and action")
		}
		for _, action := range actions {
			if action.AutomationID != a.ID || action.ServiceID != a.ServiceID {
				return fmt.Errorf("scm action aggregate mismatch")
			}
			if a.RunKind == domain.RunKindReview && action.EventFamily != "pull_request" {
				return fmt.Errorf("review automation requires pull request actions")
			}
		}
	case "kanban":
		if a.RunKind == domain.RunKindReview {
			return fmt.Errorf("review automation requires an scm trigger")
		}
		if kanban == nil || kanban.AutomationID != a.ID {
			return fmt.Errorf("kanban automation needs exactly one trigger")
		}
	case "cron":
		if a.RunKind == domain.RunKindReview {
			return fmt.Errorf("review automation requires an scm trigger")
		}
		if cron == nil || cron.AutomationID != a.ID {
			return fmt.Errorf("cron automation needs exactly one trigger")
		}
		if cron.OutputMode == "" {
			cron.OutputMode = domain.AutomationOutputRunOnly
		}
		if !domain.ValidAutomationOutputMode(cron.OutputMode) {
			return fmt.Errorf("invalid cron automation output mode")
		}
	}
	return nil
}

func validateServiceRepositoryBinding(svc *domain.Service, installation *domain.PluginInstallation) error {
	if svc == nil || installation == nil || svc.ProjectID != installation.ProjectID ||
		svc.RepoKind != domain.RepoKindProvider || string(svc.Provider) != string(installation.Provider) {
		return fmt.Errorf("service repository binding project/provider mismatch")
	}
	return nil
}
