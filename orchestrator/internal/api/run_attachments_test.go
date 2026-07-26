package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type attachmentObjectFake struct {
	putURL  string
	putErr  error
	deleted []string
}

func (f *attachmentObjectFake) PresignPut(string, time.Duration) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	return f.putURL, nil
}
func (f *attachmentObjectFake) PresignGet(string, time.Duration) (string, error) {
	return "https://get.test", nil
}
func (f *attachmentObjectFake) Stat(context.Context, string) (int64, string, error) {
	return 0, "", errors.New("unused")
}
func (f *attachmentObjectFake) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func attachmentServer(t *testing.T) (*Server, *store.MemStore, *domain.Service, *domain.User) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	u := &domain.User{ID: "u", DisplayName: "u", CreatedAt: time.Now()}
	if _, err := st.CreateUserWithIdentity(t.Context(), u, &domain.UserIdentity{ID: "i", Provider: domain.ProviderGitHub, ProviderUID: "u", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	p := &domain.Project{ID: "p", Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(t.Context(), &domain.ProjectMember{ProjectID: p.ID, UserID: u.ID, Role: domain.RoleOwner, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s", ProjectID: p.ID, Name: "s", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(t.Context(), svc); err != nil {
		t.Fatal(err)
	}
	return srv, st, svc, u
}
func attachmentReq(t *testing.T, method, path string, body []byte, u *domain.User) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), principalCtxKey{}, &principal{user: u}))
	r.SetPathValue("id", "s")
	if strings.Contains(path, "/attachments/a/content") {
		r.SetPathValue("stage", "a")
	}
	return r
}

func TestAttachmentIntentValidatesNameSizeMIMEAndUser(t *testing.T) {
	srv, _, svc, u := attachmentServer(t)
	srv.attachmentStore = &attachmentObjectFake{}
	for _, in := range []map[string]any{{"name": "../x", "size_bytes": 1}, {"name": "x", "size_bytes": attachmentMaxBytes + 1}, {"name": "x", "size_bytes": 1, "content_type": "application/x-msdownload"}} {
		b, _ := json.Marshal(in)
		w := httptest.NewRecorder()
		srv.handleCreateAttachmentIntent(w, attachmentReq(t, http.MethodPost, "/api/v1/services/"+svc.ID+"/attachments/intents", b, u))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("input=%v status=%d", in, w.Code)
		}
	}
	b, _ := json.Marshal(map[string]any{"name": "notes.txt", "size_bytes": 3, "content_type": "text/plain"})
	w := httptest.NewRecorder()
	srv.handleCreateAttachmentIntent(w, attachmentReq(t, http.MethodPost, "/api/v1/services/"+svc.ID+"/attachments/intents", b, u))
	if w.Code != http.StatusCreated {
		t.Fatalf("valid intent=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("object_key")) {
		t.Fatal("intent leaked object key")
	}
}

func TestAttachmentUploadRejectsLengthAndDeletesFailedObject(t *testing.T) {
	srv, st, svc, u := attachmentServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer upstream.Close()
	f := &attachmentObjectFake{putURL: upstream.URL}
	srv.attachmentStore = f
	now := time.Now()
	stage := &domain.AttachmentStage{ID: "a", ProjectID: "p", CreatedBy: u.ID, ObjectKey: "run-inputs/p/a", DisplayName: "a", SizeBytes: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateAttachmentStage(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := attachmentReq(t, http.MethodPut, "/api/v1/services/"+svc.ID+"/attachments/a/content", []byte("no"), u)
	req.ContentLength = 2
	srv.handleUploadAttachmentContent(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("length status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	req = attachmentReq(t, http.MethodPut, "/api/v1/services/"+svc.ID+"/attachments/a/content", []byte("yes"), u)
	req.ContentLength = 3
	srv.handleUploadAttachmentContent(w, req)
	if w.Code != http.StatusBadGateway || len(f.deleted) != 1 {
		t.Fatalf("failed put status=%d deletes=%v", w.Code, f.deleted)
	}
}

func TestAttachmentPresignFailureReleasesUploadClaim(t *testing.T) {
	srv, st, svc, u := attachmentServer(t)
	srv.attachmentStore = &attachmentObjectFake{putErr: errors.New("sign unavailable")}
	now := time.Now()
	stage := &domain.AttachmentStage{ID: "a", ProjectID: "p", CreatedBy: u.ID, ObjectKey: "run-inputs/p/a", DisplayName: "a", SizeBytes: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateAttachmentStage(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := attachmentReq(t, http.MethodPut, "/api/v1/services/"+svc.ID+"/attachments/a/content", []byte("yes"), u)
	req.ContentLength = 3
	srv.handleUploadAttachmentContent(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", w.Code)
	}
	if _, err := st.ClaimAttachmentStageUpload(t.Context(), stage.ID, "p", u.ID, time.Now()); err != nil {
		t.Fatalf("stage stuck uploading after presign failure: %v", err)
	}
}
