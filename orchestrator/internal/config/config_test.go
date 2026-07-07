package config

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// baseEnv sets the always-required env for a minimal (K8s-disabled) Load.
func baseEnv(t *testing.T) {
	t.Setenv("CONSOLE_TOKEN", "tok")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("DISABLE_K8S", "1")
}

func validKey() string {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

// TestLoadNoProvidersBackwardCompatible: with no AUTH_*_CLIENT_ID and no
// AUTH_TOKEN_KEY, Load succeeds and reports zero providers — the system runs on
// CONSOLE_TOKEN alone.
func TestLoadNoProvidersBackwardCompatible(t *testing.T) {
	baseEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load with no providers should succeed: %v", err)
	}
	if len(c.OAuthProviders) != 0 {
		t.Fatalf("expected 0 providers, got %d", len(c.OAuthProviders))
	}
}

// TestLoadProviderRequiresTokenKey: configuring a provider CLIENT_ID without
// AUTH_TOKEN_KEY is a startup error.
func TestLoadProviderRequiresTokenKey(t *testing.T) {
	baseEnv(t)
	t.Setenv("AUTH_GITEA_CLIENT_ID", "cid")
	if _, err := Load(); err == nil {
		t.Fatal("provider configured without AUTH_TOKEN_KEY should error")
	}
}

// TestLoadProviderBadTokenKey: a non-32-byte key is rejected.
func TestLoadProviderBadTokenKey(t *testing.T) {
	baseEnv(t)
	t.Setenv("AUTH_GITEA_CLIENT_ID", "cid")
	t.Setenv("AUTH_TOKEN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if _, err := Load(); err == nil {
		t.Fatal("16-byte AUTH_TOKEN_KEY should error (need 32)")
	}
}

// TestLoadProviderConfigured: a fully-configured gitea provider is parsed with
// its dual URLs.
func TestLoadProviderConfigured(t *testing.T) {
	baseEnv(t)
	t.Setenv("AUTH_TOKEN_KEY", validKey())
	t.Setenv("AUTH_GITEA_CLIENT_ID", "cid")
	t.Setenv("AUTH_GITEA_CLIENT_SECRET", "sec")
	t.Setenv("AUTH_GITEA_EXTERNAL_URL", "http://localhost:3000")
	t.Setenv("AUTH_GITEA_INTERNAL_URL", "http://gitea.svc:3000")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.OAuthProviders) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(c.OAuthProviders))
	}
	p := c.OAuthProviders[0]
	if p.ID != "gitea" || p.ClientID != "cid" || p.ClientSecret != "sec" ||
		p.ExternalURL != "http://localhost:3000" || p.InternalURL != "http://gitea.svc:3000" {
		t.Fatalf("provider config wrong: %+v", p)
	}
}

// TestLoadGithubDefaultsPublicHosts: a github provider with only CLIENT_ID gets
// the public host defaults.
func TestLoadGithubDefaultsPublicHosts(t *testing.T) {
	baseEnv(t)
	t.Setenv("AUTH_TOKEN_KEY", validKey())
	t.Setenv("AUTH_GITHUB_CLIENT_ID", "gid")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.OAuthProviders) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(c.OAuthProviders))
	}
	p := c.OAuthProviders[0]
	if p.ID != "github" || p.ExternalURL != "https://github.com" || p.InternalURL != "https://github.com" {
		t.Fatalf("github defaults wrong: %+v", p)
	}
}
