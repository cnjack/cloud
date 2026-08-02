package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

type artifactObjectFake struct {
	mu       sync.Mutex
	server   *httptest.Server
	objects  map[string][]byte
	statSize *int64
}

func newArtifactObjectFake(t *testing.T) *artifactObjectFake {
	t.Helper()
	fake := &artifactObjectFake{objects: make(map[string][]byte)}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fake.objects[key] = body
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			body, ok := fake.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *artifactObjectFake) PresignPut(key string, _ time.Duration) (string, error) {
	return f.server.URL + "?key=" + url.QueryEscape(key), nil
}
func (f *artifactObjectFake) PresignGet(key string, _ time.Duration) (string, error) {
	return f.server.URL + "?key=" + url.QueryEscape(key), nil
}
func (f *artifactObjectFake) Stat(_ context.Context, key string) (int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statSize != nil {
		return *f.statSize, "application/octet-stream", nil
	}
	return int64(len(f.objects[key])), "application/octet-stream", nil
}
func (f *artifactObjectFake) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func artifactShareServer(t *testing.T) (*Server, *store.MemStore, *domain.User, *domain.Device, *artifactObjectFake) {
	t.Helper()
	st := store.NewMemStore()
	srv := New(st, withTestModel(&config.Config{ConsoleToken: consoleToken, ConsoleURL: "https://cloud.example"}), slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	user := &domain.User{ID: "artifact-user", DisplayName: "Artifact User", CreatedAt: time.Now().UTC()}
	identity := &domain.UserIdentity{ID: "artifact-identity", Provider: domain.ProviderGitHub, ProviderUID: "artifact-user", CreatedAt: time.Now().UTC()}
	if _, err := st.CreateUserWithIdentity(t.Context(), user, identity); err != nil {
		t.Fatal(err)
	}
	device := &domain.Device{ID: "artifact-device", UserID: user.ID, Name: "test", KeyGen: 1, CreatedAt: time.Now().UTC()}
	if err := st.CreateDevice(t.Context(), device); err != nil {
		t.Fatal(err)
	}
	objects := newArtifactObjectFake(t)
	srv.WithAttachmentObjectStore(objects)
	return srv, st, user, device, objects
}

func artifactDeviceRequest(t *testing.T, method, path string, body []byte, user *domain.User, device *domain.Device) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r = r.WithContext(withPrincipal(r.Context(), &principal{deviceID: device.ID, deviceUserID: user.ID}))
	return r
}

func TestDeviceArtifactShareLifecycleServesCiphertextOnlyUntilRevoked(t *testing.T) {
	srv, _, user, device, _ := artifactShareServer(t)
	content := bytes.Repeat([]byte{0xA5}, 32)
	intentBody, _ := json.Marshal(map[string]any{
		"protocol":    domain.ArtifactShareProtocolV1,
		"artifact_id": "artifact-1", "revision": 2,
		"ciphertext_size": len(content), "expires_in_seconds": 3600,
	})
	intentReq := artifactDeviceRequest(t, http.MethodPost, "/internal/v1/device/artifact-shares/intents", intentBody, user, device)
	intentW := httptest.NewRecorder()
	srv.handleCreateArtifactShareIntent(intentW, intentReq)
	if intentW.Code != http.StatusCreated {
		t.Fatalf("intent status=%d body=%s", intentW.Code, intentW.Body.String())
	}
	var intent struct {
		ShareID     string `json:"share_id"`
		UploadURL   string `json:"upload_url"`
		CompleteURL string `json:"complete_url"`
		BaseURL     string `json:"base_url"`
	}
	if err := json.Unmarshal(intentW.Body.Bytes(), &intent); err != nil {
		t.Fatal(err)
	}
	if intent.ShareID == "" || intent.BaseURL != "https://cloud.example/s/"+intent.ShareID || bytes.Contains(intentW.Body.Bytes(), []byte("object_key")) {
		t.Fatalf("intent response=%s", intentW.Body.String())
	}

	uploadReq := artifactDeviceRequest(t, http.MethodPut, intent.UploadURL, content, user, device)
	uploadReq.SetPathValue("shareID", intent.ShareID)
	uploadReq.ContentLength = int64(len(content))
	uploadW := httptest.NewRecorder()
	srv.handleUploadArtifactShareContent(uploadW, uploadReq)
	if uploadW.Code != http.StatusNoContent {
		t.Fatalf("upload status=%d body=%s", uploadW.Code, uploadW.Body.String())
	}

	digest := sha256.Sum256(content)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 12))
	metadataCiphertext := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 20))
	completeBody, _ := json.Marshal(map[string]any{
		"ciphertext_sha256":  hex.EncodeToString(digest[:]),
		"encrypted_metadata": map[string]any{"nonce": nonce, "ciphertext": metadataCiphertext, "plaintext_length": 4},
	})
	completeReq := artifactDeviceRequest(t, http.MethodPost, intent.CompleteURL, completeBody, user, device)
	completeReq.SetPathValue("shareID", intent.ShareID)
	completeW := httptest.NewRecorder()
	srv.handleCompleteArtifactShare(completeW, completeReq)
	if completeW.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeW.Code, completeW.Body.String())
	}

	metadataReq := httptest.NewRequest(http.MethodGet, "/api/v1/shared-artifacts/"+intent.ShareID, nil)
	metadataReq.SetPathValue("shareID", intent.ShareID)
	metadataW := httptest.NewRecorder()
	srv.handleGetSharedArtifact(metadataW, metadataReq)
	if metadataW.Code != http.StatusOK || !bytes.Contains(metadataW.Body.Bytes(), []byte(metadataCiphertext)) || bytes.Contains(metadataW.Body.Bytes(), []byte("artifact-user")) {
		t.Fatalf("public metadata status=%d body=%s", metadataW.Code, metadataW.Body.String())
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff"} {
		if got := metadataW.Header().Get(header); got != want {
			t.Fatalf("header %s=%q want %q", header, got, want)
		}
	}

	contentReq := httptest.NewRequest(http.MethodGet, "/api/v1/shared-artifacts/"+intent.ShareID+"/content", nil)
	contentReq.SetPathValue("shareID", intent.ShareID)
	contentW := httptest.NewRecorder()
	srv.handleGetSharedArtifactContent(contentW, contentReq)
	if contentW.Code != http.StatusOK || !bytes.Equal(contentW.Body.Bytes(), content) || contentW.Header().Get("Location") != "" {
		t.Fatalf("public content status=%d location=%q body=%x", contentW.Code, contentW.Header().Get("Location"), contentW.Body.Bytes())
	}

	revokeReq := artifactDeviceRequest(t, http.MethodDelete, "/internal/v1/device/artifact-shares/"+intent.ShareID, nil, user, device)
	revokeReq.SetPathValue("shareID", intent.ShareID)
	revokeW := httptest.NewRecorder()
	srv.handleRevokeArtifactShare(revokeW, revokeReq)
	if revokeW.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revokeW.Code, revokeW.Body.String())
	}
	metadataW = httptest.NewRecorder()
	srv.handleGetSharedArtifact(metadataW, metadataReq)
	if metadataW.Code != http.StatusNotFound {
		t.Fatalf("revoked public status=%d body=%s", metadataW.Code, metadataW.Body.String())
	}
}

func TestDeviceArtifactShareIntentRejectsUnknownFieldsAndNonASCIIArtifactIDs(t *testing.T) {
	srv, _, user, device, _ := artifactShareServer(t)
	tests := []struct {
		name string
		body string
	}{
		{name: "strict json", body: `{"protocol":"jcode-artifact-share-v1","artifact_id":"artifact-1","revision":1,"ciphertext_size":32,"expires_in_seconds":3600,"plaintext_title":"leak"}`},
		{name: "opaque ascii id", body: `{"protocol":"jcode-artifact-share-v1","artifact_id":"产物","revision":1,"ciphertext_size":32,"expires_in_seconds":3600}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := artifactDeviceRequest(t, http.MethodPost, "/internal/v1/device/artifact-shares/intents", []byte(tt.body), user, device)
			w := httptest.NewRecorder()
			srv.handleCreateArtifactShareIntent(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeviceArtifactShareUploadRejectsStoredSizeMismatch(t *testing.T) {
	srv, _, user, device, objects := artifactShareServer(t)
	content := bytes.Repeat([]byte{0xA5}, 32)
	intentBody := []byte(`{"protocol":"jcode-artifact-share-v1","artifact_id":"artifact-1","revision":1,"ciphertext_size":32,"expires_in_seconds":3600}`)
	intentReq := artifactDeviceRequest(t, http.MethodPost, "/internal/v1/device/artifact-shares/intents", intentBody, user, device)
	intentW := httptest.NewRecorder()
	srv.handleCreateArtifactShareIntent(intentW, intentReq)
	if intentW.Code != http.StatusCreated {
		t.Fatalf("intent status=%d body=%s", intentW.Code, intentW.Body.String())
	}
	var intent struct {
		ShareID   string `json:"share_id"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.Unmarshal(intentW.Body.Bytes(), &intent); err != nil {
		t.Fatal(err)
	}
	wrongSize := int64(len(content) - 1)
	objects.statSize = &wrongSize
	uploadReq := artifactDeviceRequest(t, http.MethodPut, intent.UploadURL, content, user, device)
	uploadReq.SetPathValue("shareID", intent.ShareID)
	uploadReq.ContentLength = int64(len(content))
	uploadW := httptest.NewRecorder()
	srv.handleUploadArtifactShareContent(uploadW, uploadReq)
	if uploadW.Code != http.StatusBadGateway {
		t.Fatalf("upload status=%d body=%s", uploadW.Code, uploadW.Body.String())
	}
	objects.mu.Lock()
	remaining := len(objects.objects)
	objects.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("mismatched object was not deleted: %d objects", remaining)
	}
}

func TestArtifactShareRoutesKeepPublicReadUnauthenticatedAndDeviceWritesAuthenticated(t *testing.T) {
	srv, _, _, _, _ := artifactShareServer(t)
	h := srv.Handler()

	publicW := httptest.NewRecorder()
	h.ServeHTTP(publicW, httptest.NewRequest(http.MethodGet, "/api/v1/shared-artifacts/missing", nil))
	if publicW.Code != http.StatusNotFound {
		t.Fatalf("public status=%d body=%s", publicW.Code, publicW.Body.String())
	}

	intentW := httptest.NewRecorder()
	h.ServeHTTP(intentW, httptest.NewRequest(http.MethodPost, "/internal/v1/device/artifact-shares/intents", strings.NewReader(`{}`)))
	if intentW.Code != http.StatusUnauthorized {
		t.Fatalf("intent status=%d body=%s", intentW.Code, intentW.Body.String())
	}
}
