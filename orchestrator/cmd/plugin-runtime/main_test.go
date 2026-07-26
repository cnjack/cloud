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

func TestInjectRuntimeSelectsOnlySnapshottedProviders(t *testing.T) {
	assets, destination := t.TempDir(), t.TempDir()
	for _, binary := range []string{"gh", "glab", "tea"} {
		writeTestFile(t, filepath.Join(assets, "bin", binary), binary)
	}
	for _, provider := range []string{"github", "gitlab", "gitea"} {
		writeTestFile(t, filepath.Join(assets, "skills", provider, "SKILL.md"), provider)
	}
	if err := os.Chmod(destination, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := injectRuntime(assets, destination, "jtype,github,github"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bin/gh", "skills/github/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(destination, path)); err != nil {
			t.Fatalf("missing injected %s: %v", path, err)
		}
	}
	for _, path := range []string{"bin/glab", "bin/tea", "skills/gitlab", "skills/gitea", "skills/jtype"} {
		if _, err := os.Stat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected runtime asset %s: %v", path, err)
		}
	}
	info, _ := os.Stat(destination)
	if got := info.Mode().Perm(); got != 0o770 {
		t.Fatalf("runtime mount mode=%o want 0770", got)
	}
}

func TestSyncPluginCredentialsWritesCLIAndMCPConfigs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"credentials":[
			{"provider":"github","base_url":"https://github.com","access_token":"gh-token","scheme":"Bearer"},
			{"provider":"jtype","base_url":"https://jtype.test","access_token":"jt-token","scheme":"Bearer"}
		]}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	c := &client{base: srv.URL, runID: "run-1", token: "run-token", providers: map[string]bool{"github": true, "jtype": true}, http: &http.Client{Timeout: time.Second}}
	if err := c.syncPluginCredentials(dir, time.Millisecond, true, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh/hosts.yml", "git/config", "git/credentials", "jtype/mcp.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode/error=%v/%v", name, info, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jtype", "mcp.json"))
	if !strings.Contains(string(data), "jtype.test/mcp") || !strings.Contains(string(data), "jt-token") {
		t.Fatalf("JType MCP config missing expected runtime data")
	}
	info, _ := os.Stat(dir)
	if got := info.Mode().Perm(); got != 0o770 {
		t.Fatalf("credential mount mode=%o want 0770", got)
	}
}

func TestParseProvidersRejectsUnknownProvider(t *testing.T) {
	if _, err := parseProviders("github,unknown"); err == nil {
		t.Fatal("unknown Provider must fail closed")
	}
}

func TestSyncPluginCredentialsRejectsProviderOutsideSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"credentials":[{"provider":"gitlab","base_url":"https://gitlab.test","access_token":"token","scheme":"Bearer"}]}`))
	}))
	defer srv.Close()
	c := &client{base: srv.URL, runID: "run-1", token: "run-token", providers: map[string]bool{"github": true}, http: &http.Client{Timeout: time.Second}}
	err := c.syncPluginCredentials(t.TempDir(), time.Millisecond, true, "")
	if err == nil || !strings.Contains(err.Error(), "outside the run snapshot") {
		t.Fatalf("sync error=%v, want out-of-snapshot rejection", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
