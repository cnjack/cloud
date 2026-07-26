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
	for _, path := range []string{"bin/glab", "bin/tea", "skills/gitlab/SKILL.md", "skills/gitea/SKILL.md", "skills/jtype"} {
		if _, err := os.Stat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected runtime asset %s: %v", path, err)
		}
	}
	for _, provider := range []string{"github", "gitlab", "gitea"} {
		if info, err := os.Stat(filepath.Join(destination, "skills", provider)); err != nil || !info.IsDir() {
			t.Fatalf("managed Skill mask %s missing: %v", provider, err)
		}
	}
	info, _ := os.Stat(destination)
	if got := info.Mode().Perm(); got != 0o770 {
		t.Fatalf("runtime mount mode=%o want 0770", got)
	}
}

func TestInjectRuntimeProviderSkillAndCLIMatrix(t *testing.T) {
	assets := t.TempDir()
	for _, binary := range []string{"gh", "glab", "tea"} {
		writeTestFile(t, filepath.Join(assets, "bin", binary), binary)
	}
	for _, provider := range []string{"github", "gitlab", "gitea"} {
		writeTestFile(t, filepath.Join(assets, "skills", provider, "SKILL.md"), provider)
	}
	matrix := []struct {
		provider string
		binary   string
	}{
		{provider: "github", binary: "gh"},
		{provider: "gitlab", binary: "glab"},
		{provider: "gitea", binary: "tea"},
		{provider: "jtype"},
	}
	for _, tc := range matrix {
		t.Run(tc.provider, func(t *testing.T) {
			destination := t.TempDir()
			if err := injectRuntime(assets, destination, tc.provider); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range matrix[:3] {
				_, cliErr := os.Stat(filepath.Join(destination, "bin", candidate.binary))
				_, skillErr := os.Stat(filepath.Join(destination, "skills", candidate.provider, "SKILL.md"))
				want := tc.provider == candidate.provider
				if want && (cliErr != nil || skillErr != nil) {
					t.Fatalf("%s snapshot missing matching CLI/Skill: cli=%v skill=%v", tc.provider, cliErr, skillErr)
				}
				if !want && (!os.IsNotExist(cliErr) || !os.IsNotExist(skillErr)) {
					t.Fatalf("%s snapshot exposed %s runtime: cli=%v skill=%v", tc.provider, candidate.provider, cliErr, skillErr)
				}
			}
		})
	}
}

func TestSyncPluginCredentialsWritesCLIAndMCPConfigs(t *testing.T) {
	githubToken := "gh-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"credentials":[{"provider":"github","base_url":"https://github.com","access_token":"` + githubToken + `","scheme":"Bearer"},{"provider":"jtype","base_url":"https://jtype.test","access_token":"jt-token","scheme":"Bearer"}]}`))
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
	for _, name := range []string{"gh/config.yml", "gh/hosts.yml", "git/config", "git/credentials", "jtype/mcp.json"} {
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
	hosts, err := os.ReadFile(filepath.Join(dir, "gh", "hosts.yml"))
	if err != nil || !strings.Contains(string(hosts), "users:\n        jcloud:\n            oauth_token: gh-token") {
		t.Fatalf("gh hosts config does not use the current multi-user schema: contents=%q err=%v", hosts, err)
	}
	version, err := os.ReadFile(filepath.Join(dir, "gh", "config.yml"))
	if err != nil || string(version) != "version: \"1\"\n" {
		t.Fatalf("gh config version marker not written: contents=%q err=%v", version, err)
	}
	githubToken = "gh-token-refreshed"
	if err := c.syncPluginCredentials(dir, time.Millisecond, true, ""); err != nil {
		t.Fatal(err)
	}
	refreshed, err := os.ReadFile(filepath.Join(dir, "gh", "hosts.yml"))
	if err != nil || !strings.Contains(string(refreshed), "gh-token-refreshed") || strings.Contains(string(refreshed), "oauth_token: gh-token\n") {
		t.Fatalf("atomic refresh did not replace the last-good gh config: %v", err)
	}
	version, err = os.ReadFile(filepath.Join(dir, "gh", "config.yml"))
	if err != nil || string(version) != "version: \"1\"\n" {
		t.Fatalf("gh config version marker did not survive refresh: contents=%q err=%v", version, err)
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
