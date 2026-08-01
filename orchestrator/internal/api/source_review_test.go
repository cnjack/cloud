package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestIngestStructuredReviewValidatesBeforePersisting(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	project := &domain.Project{ID: domain.NewID(), Name: "review", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "repo", RepoKind: domain.RepoKindProvider,
		Provider: domain.ProviderGitHub, RepoOwnerName: "acme/repo", DefaultBranch: "main",
		GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "review",
		Status: domain.StatusRunning, Kind: domain.RunKindReview, CreatedAt: time.Now(),
		PRBaseSHA: strings.Repeat("a", 40), PRHeadSHA: strings.Repeat("b", 40),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	server := New(st, &config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	withoutPlanReq := httptest.NewRequest(http.MethodPost, "/internal/v1/runs/"+run.ID+"/review", bytes.NewBufferString(`{"summary":"No findings.","findings":[]}`))
	withoutPlanReq.Header.Set("Content-Type", structuredReviewMediaType)
	withoutPlanResp := httptest.NewRecorder()
	server.handleIngestReview(withoutPlanResp, withoutPlanReq, run.ID)
	if withoutPlanResp.Code != http.StatusConflict || !strings.Contains(withoutPlanResp.Body.String(), "review_plan_required") {
		t.Fatalf("review without plan status=%d body=%s", withoutPlanResp.Code, withoutPlanResp.Body.String())
	}
	planBody := `{"base_sha":"` + run.PRBaseSHA + `","head_sha":"` + run.PRHeadSHA + `","merge_base_sha":"` + strings.Repeat("c", 40) + `","diff":"diff --git a/ledger.py b/ledger.py\n--- a/ledger.py\n+++ b/ledger.py\n@@ -8 +8 @@\n-old\n+new\n"}`
	planReq := httptest.NewRequest(http.MethodPost, "/internal/v1/runs/"+run.ID+"/review-plan", bytes.NewBufferString(planBody))
	planResp := httptest.NewRecorder()
	server.handleIngestReviewPlan(planResp, planReq, run.ID)
	if planResp.Code != http.StatusCreated {
		t.Fatalf("review plan status=%d body=%s", planResp.Code, planResp.Body.String())
	}

	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/runs/"+run.ID+"/review", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", structuredReviewMediaType)
		response := httptest.NewRecorder()
		server.handleIngestReview(response, request, run.ID)
		return response
	}

	valid := `{"summary":"One verified defect.","findings":[{"path":"ledger.py","line":8,"severity":"P1","confidence":96,"title":"Balance guard is reversed","body":"Valid transfers are rejected while overdrafts proceed."}],"checks":["tests","changed lines"]}`
	if response := post(valid); response.Code != http.StatusCreated {
		t.Fatalf("valid review status=%d body=%s", response.Code, response.Body.String())
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewResult == nil || len(got.ReviewResult.Findings) != 1 || got.ReviewOutput == "" {
		t.Fatalf("structured review not persisted: %#v", got)
	}

	invalid := `{"summary":"Bad anchor.","findings":[{"path":"../secret","line":1,"severity":"P1","confidence":99,"title":"Unsafe","body":"No."}]}`
	if response := post(invalid); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid review status=%d body=%s", response.Code, response.Body.String())
	}
	after, _ := st.GetRun(ctx, run.ID)
	if len(after.ReviewResult.Findings) != 1 {
		t.Fatal("invalid structured upload replaced the validated result")
	}
}

func TestIngestReviewPlanIsFirstWriteAndRejectsRevisionMismatch(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	run := &domain.Run{ID: domain.NewID(), ProjectID: "p", ServiceID: "s", Prompt: "review", Status: domain.StatusRunning, Kind: domain.RunKindReview, PRBaseSHA: base, PRHeadSHA: head, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	server := New(st, &config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	post := func(baseSHA, diff string) *httptest.ResponseRecorder {
		body := `{"base_sha":"` + baseSHA + `","head_sha":"` + head + `","merge_base_sha":"` + strings.Repeat("c", 40) + `","diff":` + string(mustJSON(t, diff)) + `}`
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/runs/"+run.ID+"/review-plan", bytes.NewBufferString(body))
		response := httptest.NewRecorder()
		server.handleIngestReviewPlan(response, req, run.ID)
		return response
	}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	if response := post(base, diff); response.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	if response := post(base, diff); response.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	if response := post(strings.Repeat("d", 40), diff); response.Code != http.StatusConflict {
		t.Fatalf("mismatch status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestContractReviewRejectsLegacyMarkdownOutput(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	project := &domain.Project{ID: domain.NewID(), Name: "review", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "repo", RepoKind: domain.RepoKindProvider,
		Provider: domain.ProviderGitHub, RepoOwnerName: "acme/repo", DefaultBranch: "main",
		GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "review",
		Status: domain.StatusRunning, Kind: domain.RunKindReview, CreatedAt: time.Now(),
		PRBaseSHA: strings.Repeat("a", 40), PRHeadSHA: strings.Repeat("b", 40),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	server := New(st, &config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/runs/"+run.ID+"/review", strings.NewReader("legacy markdown"))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	server.handleIngestReview(response, req, run.ID)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "structured_review_required") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReviewOutput != "" {
		t.Fatal("legacy markdown was persisted for a contract review")
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
