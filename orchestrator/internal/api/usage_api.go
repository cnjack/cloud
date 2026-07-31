package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

type usageSummaryEnvelope struct {
	Summary domain.UsageSummary `json:"summary"`
	Groups  []domain.UsageGroup `json:"groups"`
}

func unavailableUsageSummary(reason string) domain.UsageSummary {
	return domain.UsageSummary{
		Availability: "unavailable",
		Reason:       reason,
		Costs: domain.UsageCostTotals{
			Reported:  []domain.UsageMoneyTotal{},
			Estimated: []domain.UsageMoneyTotal{},
			Uncosted:  []domain.UsageUncostedTotal{},
		},
	}
}

func (s *Server) usageQueryFromRequest(r *http.Request) (domain.UsageSummaryQuery, error) {
	var query domain.UsageSummaryQuery
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return query, err
		}
		value = value.UTC()
		query.From = &value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return query, err
		}
		value = value.UTC()
		query.To = &value
	}
	if query.From != nil && query.To != nil && !query.From.Before(*query.To) {
		return query, errors.New("from must be before to")
	}
	now := time.Now().UTC()
	retention := s.cfg.UsageRollupRetention
	if retention <= 0 {
		retention = 365 * 24 * time.Hour
	}
	lowerBound := now.Add(-retention)
	if query.From == nil || query.From.Before(lowerBound) {
		query.From = &lowerBound
	}
	if query.To == nil || query.To.After(now) {
		query.To = &now
	}
	if !query.From.Before(*query.To) {
		return query, errors.New("requested usage range is outside retention")
	}
	return query, nil
}

func (s *Server) handleGetProjectUsage(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	query, err := s.usageQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to must be RFC3339 instants with from before to")
		return
	}
	query.SubjectKind = domain.UsageSubjectRun
	query.ProjectID = projectID
	groupBy := strings.TrimSpace(r.URL.Query().Get("group_by"))
	if groupBy == "" {
		groupBy = "service"
	}
	if groupBy != "service" && groupBy != "automation" && groupBy != "model" {
		writeError(w, http.StatusBadRequest, "bad_request", "group_by must be service, automation, or model")
		return
	}
	summary, err := s.st.GetUsageSummary(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project usage")
		return
	}
	groups, err := s.st.ListUsageGroups(r.Context(), query, groupBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not group project usage")
		return
	}
	writeJSON(w, http.StatusOK, usageSummaryEnvelope{Summary: summary, Groups: groups})
}

func (s *Server) handleGetAccountUsage(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	userID := ""
	if principal != nil {
		userID = principal.userID()
	}
	if userID == "" {
		writeError(w, http.StatusForbidden, "forbidden", "Account usage requires a user session")
		return
	}
	query, err := s.usageQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to must be RFC3339 instants with from before to")
		return
	}
	query.SubjectKind = domain.UsageSubjectDevice
	query.UserID = userID
	query.DeviceID = strings.TrimSpace(r.URL.Query().Get("device_id"))
	query.ModelID = strings.TrimSpace(r.URL.Query().Get("model_id"))
	query.GrantScope = strings.TrimSpace(r.URL.Query().Get("grant_scope"))
	if query.GrantScope != "" && query.GrantScope != "account" &&
		query.GrantScope != "project" && query.GrantScope != "cluster" {
		writeError(w, http.StatusBadRequest, "bad_request", "grant_scope must be account, project, or cluster")
		return
	}
	groupBy := strings.TrimSpace(r.URL.Query().Get("group_by"))
	if groupBy == "" {
		groupBy = "device"
	}
	if groupBy != "device" && groupBy != "model" && groupBy != "grant" {
		writeError(w, http.StatusBadRequest, "bad_request", "group_by must be device, model, or grant")
		return
	}
	summary, err := s.st.GetUsageSummary(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Account usage")
		return
	}
	groups, err := s.st.ListUsageGroups(r.Context(), query, groupBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not group Account usage")
		return
	}
	writeJSON(w, http.StatusOK, usageSummaryEnvelope{Summary: summary, Groups: groups})
}

func (s *Server) handleGetServiceUsage(w http.ResponseWriter, r *http.Request) {
	service, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), service.ProjectID, domain.RoleViewer) {
		return
	}
	query, err := s.usageQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to must be RFC3339 instants with from before to")
		return
	}
	query.SubjectKind = domain.UsageSubjectRun
	query.ProjectID = service.ProjectID
	query.ServiceID = service.ID
	summary, err := s.st.GetUsageSummary(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Service usage")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetAutomationUsage(w http.ResponseWriter, r *http.Request) {
	spec, _, ok := s.loadPluginAutomationForMember(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found")
		return
	}
	query, err := s.usageQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to must be RFC3339 instants with from before to")
		return
	}
	query.SubjectKind = domain.UsageSubjectRun
	query.AutomationID = spec.Automation.ID
	summary, err := s.st.GetUsageSummary(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Automation usage")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
