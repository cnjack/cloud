package api

import (
	"context"
	"github.com/cnjack/jcloud/internal/domain"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountAttachmentCreatesOwnedRepositoryAndRealTaskStage(t *testing.T) {
	ts, st, token, uid, _ := newAccountTaskServer(t, func(s *Server) { s.attachmentStore = &attachmentObjectFake{} })
	request := map[string]any{"name": "brief.txt", "content_type": "text/plain", "size_bytes": 3}
	response := do(t, http.MethodPost, ts.URL+"/api/v1/account/repositories/gitea/42/attachments/intents", token, request)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("intent status %d", response.StatusCode)
	}
	var intent struct {
		Stage     domain.AttachmentStage `json:"stage"`
		UploadURL string                 `json:"upload_url"`
	}
	decode(t, response, &intent)
	if intent.Stage.ID == "" || !strings.HasPrefix(intent.UploadURL, "/api/v1/repositories/") {
		t.Fatalf("bad intent: %+v", intent)
	}
	stored, err := st.GetAttachmentStage(context.Background(), intent.Stage.ID)
	if err != nil || stored.CreatedBy != uid {
		t.Fatalf("wrong owner: %+v %v", stored, err)
	}
	// Existing proxy upload tests cover bytes/size/expiry. Mark this fixture's
	// upload complete to verify the account-task path consumes its own stage.
	if _, err := st.ClaimAttachmentStageUpload(context.Background(), stored.ID, stored.ProjectID, uid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachmentStageUploaded(context.Background(), stored.ID, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	response = do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, map[string]any{
		"provider": "gitea", "provider_repo_id": "42", "prompt": "Read the attached brief", "model_id": accountTaskModelID, "attachment_stage_ids": []string{stored.ID},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("task status %d", response.StatusCode)
	}
	var task accountTaskResponse
	decode(t, response, &task)
	attachments, err := st.ListRunAttachments(context.Background(), task.Run.ID)
	if err != nil || task.Run.ProjectID != stored.ProjectID || len(attachments) != 1 || attachments[0].DisplayName != "brief.txt" {
		t.Fatalf("attachment not consumed by task: count=%d err=%v", len(attachments), err)
	}
}

func TestAccountAttachmentRejectsUnavailableDependenciesAndService(t *testing.T) {
	ts, st, token, uid, _ := newAccountTaskServer(t)
	for _, c := range []struct {
		token  string
		status int
	}{{consoleToken, 403}, {token, 409}} {
		resp := do(t, http.MethodPost, ts.URL+"/api/v1/account/repositories/gitea/42/attachments/intents", c.token, map[string]any{"name": "x.txt", "size_bytes": 3})
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Fatalf("status=%d want=%d", resp.StatusCode, c.status)
		}
	}
	repos, err := st.ListRepositoriesForUser(context.Background(), uid)
	if err != nil || len(repos) != 0 {
		t.Fatalf("unavailable storage must not materialize a repository: %v %v", repos, err)
	}
}
