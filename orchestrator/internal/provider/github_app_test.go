package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testGitHubAppKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestGitHubAppIssuerMintsInstallationToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "token":"ghs_short_lived",
		  "expires_at":"2026-07-25T13:00:00Z",
		  "repository_selection":"selected",
		  "permissions":{"contents":"write","pull_requests":"write"}
		}`))
	}))
	defer server.Close()

	issuer, err := NewGitHubAppIssuer("1234", testGitHubAppKey(t))
	if err != nil {
		t.Fatal(err)
	}
	issuer.apiBase = server.URL
	issuer.http = server.Client()
	issuer.now = func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) }

	token, err := issuer.IssueInstallationToken(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "ghs_short_lived" || token.RepositorySelection != "selected" ||
		token.Permissions["contents"] != "write" {
		t.Fatalf("token = %+v", token)
	}
	parts := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("Authorization does not contain a JWT: %q", authorization)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "1234" {
		t.Fatalf("JWT iss = %#v", claims["iss"])
	}
}

func TestGitHubAppIssuerListsOnlyUserManageableInstallations(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations" ||
			r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"installations":[{
			"id":42,"target_type":"Organization","repository_selection":"selected",
			"account":{"id":99,"login":"octo-org"}
		}]}`))
	}))
	defer server.Close()
	issuer, err := NewGitHubAppIssuer("1", testGitHubAppKey(t))
	if err != nil {
		t.Fatal(err)
	}
	issuer.apiBase, issuer.http = server.URL, server.Client()
	got, err := issuer.ListUserInstallations(context.Background(), "ghu_user")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer ghu_user" || len(got) != 1 ||
		got[0].ID != "42" || got[0].AccountID != "99" || got[0].Account != "octo-org" {
		t.Fatalf("authorization=%q installations=%+v", authorization, got)
	}
}

func TestGitHubAppIssuerRejectsBadConfiguration(t *testing.T) {
	if _, err := NewGitHubAppIssuer("", testGitHubAppKey(t)); err == nil {
		t.Fatal("empty app id accepted")
	}
	if _, err := NewGitHubAppIssuer("1", []byte("not pem")); err == nil {
		t.Fatal("invalid private key accepted")
	}
	for _, value := range []string{"", "0", "-1", "1/2", "abc"} {
		if err := ValidateGitHubInstallationID(value); err == nil {
			t.Fatalf("installation id %q accepted", value)
		}
	}
	if err := ValidateGitHubInstallationID("42"); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubAppIssuerFailsOnProviderErrorAndIncompleteToken(t *testing.T) {
	responses := []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"message":"bad credentials"}`},
		{http.StatusCreated, `{"token":"","expires_at":"0001-01-01T00:00:00Z"}`},
	}
	for _, response := range responses {
		t.Run(http.StatusText(response.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(response.status)
				_, _ = w.Write([]byte(response.body))
			}))
			defer server.Close()
			issuer, err := NewGitHubAppIssuer("1", testGitHubAppKey(t))
			if err != nil {
				t.Fatal(err)
			}
			issuer.apiBase, issuer.http = server.URL, server.Client()
			if _, err := issuer.IssueInstallationToken(context.Background(), "42"); err == nil {
				t.Fatal("provider failure accepted")
			}
		})
	}
}
