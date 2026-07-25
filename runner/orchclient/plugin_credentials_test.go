package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncPluginCredentialsWritesCLIAndMCPConfigsWithoutRefreshMaterial(t *testing.T) {
	const secret = "short-lived-access-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/internal/v1/runs/run-1/plugins/credentials" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"credentials":[
			{"provider":"github","base_url":"https://github.com","access_token":"short-lived-access-token","scheme":"Bearer"},
			{"provider":"gitlab","base_url":"https://gitlab.example.test","access_token":"gl-token","scheme":"Bearer"},
			{"provider":"gitea","base_url":"https://git.example.test","access_token":"tea-token","scheme":"token"},
			{"provider":"jtype","base_url":"https://jtype.example.test","access_token":"jtype-token","scheme":"Bearer"}
		]}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := &client{base: srv.URL, runID: "run-1", token: "run-token", http: &http.Client{Timeout: time.Second}}
	if err := c.syncPluginCredentials(dir, time.Millisecond, true, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh/hosts.yml", "glab/config.yml", "tea/config.yml", "git/config", "git/credentials", "jtype/mcp.json"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "refresh_token") || strings.Contains(string(data), "JCLOUD_MASTER_KEY") || strings.Contains(string(data), "private_key") {
			t.Fatalf("%s contains prohibited material", name)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o want 0600", name, info.Mode().Perm())
		}
	}
	gh, _ := os.ReadFile(filepath.Join(dir, "gh", "hosts.yml"))
	if !strings.Contains(string(gh), secret) {
		t.Fatalf("gh config missing access token")
	}
}

func TestSyncPluginCredentialsFailsBeforeFirstSuccessfulSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := &client{base: srv.URL, runID: "run-1", token: "run-token", http: &http.Client{Timeout: time.Second}}
	if err := c.syncPluginCredentials(t.TempDir(), time.Millisecond, true, ""); err == nil {
		t.Fatal("first credential sync should fail visibly on unavailable credentials")
	}
}

func TestSyncPluginCredentialsStopsWhenRunnerFinishes(t *testing.T) {
	dir := t.TempDir()
	stop := filepath.Join(dir, "runner-finished")
	if err := os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &client{base: "http://unreachable.invalid", runID: "run-1", token: "run-token", http: &http.Client{Timeout: time.Millisecond}}
	if err := c.syncPluginCredentials(filepath.Join(dir, "plugins"), time.Hour, false, stop); err != nil {
		t.Fatal(err)
	}
}

func TestWritePluginConfigsAlwaysCreatesEmptyJTypeMCPFile(t *testing.T) {
	dir := t.TempDir()
	if err := writePluginConfigs(dir, []pluginCredential{{
		Provider: "github", BaseURL: "https://github.com", AccessToken: "token",
	}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "jtype", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != `{"mcpServers":{}}` {
		t.Fatalf("empty JType MCP config=%s", data)
	}
}

func TestWritePluginConfigsRemovesUninstalledProviderFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writePluginConfigs(dir, []pluginCredential{
		{Provider: "github", BaseURL: "https://github.com", AccessToken: "gh"},
		{Provider: "gitlab", BaseURL: "https://gitlab.example.test", AccessToken: "gl"},
		{Provider: "gitea", BaseURL: "https://gitea.example.test", AccessToken: "tea"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writePluginConfigs(dir, []pluginCredential{
		{Provider: "github", BaseURL: "https://github.com", AccessToken: "gh-new"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"glab", "tea"} {
		if _, err := os.Stat(filepath.Join(dir, removed)); !os.IsNotExist(err) {
			t.Fatalf("stale %s config remains: %v", removed, err)
		}
	}
	credentials, err := os.ReadFile(filepath.Join(dir, "git", "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(credentials), "gitlab") || strings.Contains(string(credentials), "gitea") {
		t.Fatalf("stale Git credential remained: %s", credentials)
	}
}
