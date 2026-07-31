package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func TestProjectUsageSummaryGroupsSnapshotsAndKeepsEmptyUnavailable(t *testing.T) {
	ts, st, _ := newTestServer(t)
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "commerce", CreatedAt: now}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	services := []domain.Service{
		{ID: domain.NewID(), ProjectID: project.ID, Name: "payments-api", RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/payments.git", DefaultBranch: "main", CreatedAt: now},
		{ID: domain.NewID(), ProjectID: project.ID, Name: "web-checkout", RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/web.git", DefaultBranch: "main", CreatedAt: now},
	}
	for i := range services {
		if err := st.CreateService(context.Background(), &services[i]); err != nil {
			t.Fatal(err)
		}
	}
	values := []int64{10, 20}
	for i := range services {
		event := &domain.UsageEvent{
			ID: domain.NewID(), RequestID: domain.NewID(),
			SubjectKind: domain.UsageSubjectRun, SubjectID: "run-" + services[i].ID, RunID: "run-" + services[i].ID,
			ProjectID: project.ID, ProjectName: project.Name,
			ServiceID: services[i].ID, ServiceName: services[i].Name,
			InputTokens: &values[i], CaptureStatus: domain.UsageCapturePartial,
			OccurredAt: now.Add(time.Duration(i) * time.Minute), CreatedAt: now, Version: 1,
		}
		if _, err := st.RecordUsageEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	resp := do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+project.ID+"/usage?group_by=service", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("project usage status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Summary domain.UsageSummary `json:"summary"`
		Groups  []struct {
			ID      string              `json:"id"`
			Name    string              `json:"name"`
			Summary domain.UsageSummary `json:"summary"`
		} `json:"groups"`
	}
	decode(t, resp, &body)
	if body.Summary.Availability != "available" || body.Summary.Requests != 2 ||
		body.Summary.Tokens.Input == nil || *body.Summary.Tokens.Input != 30 {
		t.Fatalf("project summary=%+v", body.Summary)
	}
	if len(body.Groups) != 2 || body.Groups[0].ID != services[1].ID ||
		body.Groups[0].Name != services[1].Name ||
		body.Groups[0].Summary.Tokens.Input == nil || *body.Groups[0].Summary.Tokens.Input != 20 {
		t.Fatalf("service groups=%+v", body.Groups)
	}

	empty := &domain.Project{ID: domain.NewID(), Name: "empty", CreatedAt: now}
	if err := st.CreateProject(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+empty.ID+"/usage?group_by=service", consoleToken, nil)
	var emptyBody struct {
		Summary domain.UsageSummary `json:"summary"`
	}
	decode(t, resp, &emptyBody)
	if emptyBody.Summary.Availability != "unavailable" ||
		emptyBody.Summary.Reason != "no_requests" ||
		emptyBody.Summary.Tokens.Input != nil {
		t.Fatalf("empty usage must be unavailable, not zero: %+v", emptyBody.Summary)
	}
}

func TestUsageAuthorizationAllowsProjectViewerAndKeepsPricingAdminOnly(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "audited", CreatedAt: now}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Burn the first-user Cluster Admin grant, then create two ordinary users.
	_ = mkUser(t, st, "seed")
	viewer := mkUser(t, st, "viewer")
	outsider := mkUser(t, st, "outsider")
	if err := st.UpsertMember(ctx, &domain.ProjectMember{
		ProjectID: project.ID, UserID: viewer.ID, Role: domain.RoleViewer, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	viewerToken := mkSession(t, st, viewer.ID)
	outsiderToken := mkSession(t, st, outsider.ID)

	resp := do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+project.ID+"/usage", viewerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer project usage status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+project.ID+"/usage", outsiderToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider project usage status=%d want 403", resp.StatusCode)
	}
	var denied struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp, &denied)
	if denied.Error.Code != "forbidden" {
		t.Fatalf("outsider error code=%q want forbidden", denied.Error.Code)
	}

	model := &domain.Model{
		ID: domain.NewID(), Name: "priced-model", ModelName: "provider/model",
		BaseURL: "https://example.invalid/v1", CreatedAt: now,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet,
		ts.URL+"/api/v1/system/models/"+model.ID+"/pricing-revisions", viewerToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer pricing status=%d want 403", resp.StatusCode)
	}
	decode(t, resp, &denied)
	if denied.Error.Code != "forbidden" {
		t.Fatalf("pricing error code=%q want forbidden", denied.Error.Code)
	}
}

type usageDimensionFailureStore struct {
	store.Store
	recorded chan domain.UsageEvent
}

func (s *usageDimensionFailureStore) GetRunUsageDimensions(
	context.Context,
	string,
) (domain.RunUsageDimensions, error) {
	return domain.RunUsageDimensions{}, errors.New("temporary dimension lookup failure")
}

func (s *usageDimensionFailureStore) RecordUsageEvent(
	_ context.Context,
	event *domain.UsageEvent,
) (bool, error) {
	s.recorded <- *event
	return true, nil
}

func TestUsageAttributionFailureKeepsSynchronousProjectFenceHonest(t *testing.T) {
	fake := &usageDimensionFailureStore{
		Store: store.NewMemStore(), recorded: make(chan domain.UsageEvent, 2),
	}
	srv := &Server{
		st:              fake,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		usageWriteSlots: make(chan struct{}, 1),
	}
	at := time.Now().UTC()
	srv.submitUsageEvent(domain.UsageEvent{
		ID: domain.NewID(), RequestID: domain.NewID(),
		SubjectKind: domain.UsageSubjectRun, SubjectID: "run-known", RunID: "run-known",
		ProjectID: "project-known", ServiceID: "service-known",
		CaptureStatus: domain.UsageCaptureUnavailable,
		OccurredAt:    at, CreatedAt: at, Version: 1,
	})
	select {
	case event := <-fake.recorded:
		if event.ProjectID != "project-known" || event.ServiceID != "service-known" {
			t.Fatalf("synchronous Run dimensions were erased: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("usage event with synchronous Project attribution was not recorded")
	}

	srv.submitUsageEvent(domain.UsageEvent{
		ID: domain.NewID(), RequestID: domain.NewID(),
		SubjectKind: domain.UsageSubjectRun, SubjectID: "run-unknown", RunID: "run-unknown",
		CaptureStatus: domain.UsageCaptureUnavailable,
		OccurredAt:    at, CreatedAt: at, Version: 1,
	})
	select {
	case event := <-fake.recorded:
		t.Fatalf("event without required Project attribution was fenced: %+v", event)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUsageAPIClampsRangesToRollupRetention(t *testing.T) {
	ts, st, _ := newTestServer(t)
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "retained", CreatedAt: now}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+project.ID+"/usage?from=2000-01-01T00:00:00Z&to=2099-01-01T00:00:00Z",
		consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clamped usage status=%d want 200", resp.StatusCode)
	}
	var body usageSummaryEnvelope
	decode(t, resp, &body)
	if body.Summary.From == nil || body.Summary.To == nil {
		t.Fatalf("clamped range missing: %+v", body.Summary)
	}
	span := body.Summary.To.Sub(*body.Summary.From)
	if span > 365*24*time.Hour+time.Minute || body.Summary.To.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("usage range escaped retention: from=%v to=%v", body.Summary.From, body.Summary.To)
	}
}
