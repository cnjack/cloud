package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	server := New(st, &config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)

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
