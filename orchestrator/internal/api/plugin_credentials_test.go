package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestRunPluginCredentialsAreSnapshotScopedAndNeverExposeRefreshTokens(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: key})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	project := &domain.Project{ID: domain.NewID(), Name: "plugin project", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "svc", RepoKind: domain.RepoKindRaw, RawRepoURL: "https://example.test/repo.git", DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now().UTC()}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	runToken := "run-token"
	run := &domain.Run{ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "test", Status: domain.StatusRunning, TokenHash: auth.HashToken(runToken), CreatedAt: time.Now().UTC()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	cipher, err := auth.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	access, _ := cipher.EncryptString("short-access")
	refresh, _ := cipher.EncryptString("must-never-leak")
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginGitLab, Status: domain.PluginStatusEnabled, AccessTokenEnc: access, RefreshTokenEnc: refresh, ConfigRevision: 1, CreatedAt: time.Now().UTC()}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitLab, BaseURL: "https://gitlab.example.test", PluginEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID, CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", runToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got struct {
		Credentials []struct {
			Provider    string `json:"provider"`
			BaseURL     string `json:"base_url"`
			AccessToken string `json:"access_token"`
		} `json:"credentials"`
	}
	decode(t, resp, &got)
	if len(got.Credentials) != 1 || got.Credentials[0].AccessToken != "short-access" || got.Credentials[0].Provider != "gitlab" {
		t.Fatalf("credentials=%+v", got.Credentials)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "must-never-leak") || strings.Contains(string(encoded), "refresh_token") {
		t.Fatalf("response leaked refresh token: %s", encoded)
	}

	// Current disabled state does not change the immutable run snapshot: the
	// existing job can refresh until it terminates.
	installation.Status = domain.PluginStatusDisabled
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", runToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disabled snapshot status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Uninstalling an unrelated snapshotted Plugin must not kill an active task;
	// the removed Plugin is omitted while every remaining credential can refresh.
	if err := st.UninstallPlugin(ctx, installation.ID); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", runToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("removed unrelated snapshot status=%d want 200", resp.StatusCode)
	}
	var afterUninstall struct {
		Credentials []credentials.PluginCredential `json:"credentials"`
	}
	decode(t, resp, &afterUninstall)
	if len(afterUninstall.Credentials) != 0 {
		t.Fatalf("removed Plugin credential remained: %+v", afterUninstall.Credentials)
	}

	// A console token is not a run token and cannot read the secret endpoint.
	resp = do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", consoleToken, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("console token status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// The same valid token must stop minting current Plugin credentials as soon
	// as the task reaches a terminal state.
	if _, err := st.MarkSucceeded(ctx, run.ID, "Complete", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", runToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminal run credential status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, ts.URL+"/internal/v1/runs/"+run.ID+"/artifact", runToken, map[string]any{
		"kind": "diff", "content": "terminal pollution",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminal run generic internal endpoint status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRunPluginCredentialsSkipInvalidSnapshotAndReturnOtherPlugins(t *testing.T) {
	ctx := context.Background()
	ts, st, cfg := newCipherServer(t, nil, "")
	project := &domain.Project{ID: domain.NewID(), Name: "plugin project", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "svc", RepoKind: domain.RepoKindRaw, RawRepoURL: "https://example.test/repo.git", CreatedAt: time.Now().UTC()}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	runToken := "run-token-two"
	run := &domain.Run{ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "test", Status: domain.StatusRunning, TokenHash: auth.HashToken(runToken), CreatedAt: time.Now().UTC()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	cipher, err := auth.NewCipher(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, providerCfg := range []*domain.ProviderConfig{
		{Provider: domain.PluginGitea, BaseURL: "https://gitea.example.test", PluginEnabled: true},
		{Provider: domain.PluginJType, BaseURL: "https://jtype.example.test", PluginEnabled: true},
	} {
		if err := st.UpsertProviderConfig(ctx, providerCfg); err != nil {
			t.Fatal(err)
		}
	}
	giteaCfg, _ := st.GetProviderConfig(ctx, domain.PluginGitea)
	jtypeCfg, _ := st.GetProviderConfig(ctx, domain.PluginJType)
	past := time.Now().Add(-time.Hour)
	bad := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, AccessTokenEnc: mustCiphertext(t, cipher, "expired"), TokenExpiresAt: &past, ConfigRevision: giteaCfg.ConfigRevision, CreatedAt: time.Now().UTC()}
	good := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginJType, Status: domain.PluginStatusEnabled, AccessTokenEnc: mustCiphertext(t, cipher, "jtype-access"), ConfigRevision: jtypeCfg.ConfigRevision, CreatedAt: time.Now().UTC()}
	if err := st.CreatePluginInstallation(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginInstallation(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: bad.ID}, {RunID: run.ID, InstallationID: good.ID}}); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+run.ID+"/plugins/credentials", runToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got runPluginCredentialsResponse
	decode(t, resp, &got)
	if len(got.Credentials) != 1 || got.Credentials[0].Provider != domain.PluginJType || got.Credentials[0].AccessToken != "jtype-access" {
		t.Fatalf("partial credential response=%+v", got)
	}
}

func mustCiphertext(t *testing.T, cipher *auth.Cipher, plaintext string) []byte {
	t.Helper()
	encoded, err := cipher.EncryptString(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
