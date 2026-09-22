package modeloauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func harness(t *testing.T, h http.HandlerFunc) (*Service, *store.MemStore, string) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	st := store.NewMemStore()
	id := "personal-test"
	if err := st.CreateModelProvider(context.Background(), &domain.ModelProvider{ID: id, Name: "fixture", Kind: Kind, AuthType: domain.ModelProviderAuthOAuth, OwnerUserID: "alice"}); err != nil {
		t.Fatal(err)
	}
	cipher, err := auth.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, cipher)
	s.authBase = up.URL
	s.client = up.Client()
	return s, st, id
}
func jwt(account string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"`+account+`","email":"fixture@example.test"}`)) + ".signature"
}
func ready(t *testing.T, s *Service, id string, expired bool) {
	t.Helper()
	err := s.update(context.Background(), id, func(v *state) error {
		v.Status = Status{State: "ready"}
		v.Access = "access-secret"
		v.Refresh = "refresh-secret"
		v.AccountID = "account-fixture"
		v.TokenExpires = time.Now().Add(time.Hour)
		if expired {
			v.TokenExpires = time.Now().Add(-time.Second)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestDeviceFlowEncryptedRestartAndPollBound(t *testing.T) {
	var polls atomic.Int32
	s, st, id := harness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"private-code","user_code":"QA-CODE","interval":"1","expires_in":900}`)
		case "/api/accounts/deviceauth/token":
			if polls.Add(1) == 1 {
				w.WriteHeader(403)
				fmt.Fprint(w, `{}`)
			} else {
				fmt.Fprint(w, `{"authorization_code":"private-authorization","code_verifier":"private-verifier"}`)
			}
		case "/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("code_verifier") != "private-verifier" {
				t.Error("missing PKCE verifier")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id_token": jwt("account-fixture"), "access_token": "access-secret", "refresh_token": "refresh-secret", "expires_in": 3600})
		}
	})
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	flow, err := s.Start(ctx, id)
	if err != nil || flow.State != "pending" || flow.VerificationURI != verificationURI {
		t.Fatalf("start %+v %v", flow, err)
	}
	if _, err = s.Poll(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Poll(ctx, id); err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 1 {
		t.Fatal("poll interval ignored")
	}
	restart := New(st, s.cipher)
	restart.authBase = s.authBase
	restart.client = s.client
	now = now.Add(5 * time.Second)
	restart.now = func() time.Time { return now }
	result, err := restart.Poll(ctx, id)
	if err != nil || result.State != "ready" {
		t.Fatalf("restart poll %+v %v", result, err)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-code", "access-secret", "refresh-secret", "account-fixture"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret in public status")
		}
	}
	if err := st.WithModelOAuth(ctx, id, func(enc []byte) ([]byte, error) {
		for _, secret := range []string{"access-secret", "refresh-secret", "QA-CODE"} {
			if strings.Contains(string(enc), secret) {
				t.Error("plaintext persisted")
			}
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	token, headers, err := restart.Credential(ctx, id)
	if err != nil || token != "access-secret" || headers["ChatGPT-Account-ID"] != "account-fixture" {
		t.Fatalf("credential error %v", err)
	}
}
func TestRefreshSerializedRotatedAndInvalidGrantPersisted(t *testing.T) {
	var calls atomic.Int32
	var reject atomic.Bool
	s, st, id := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if reject.Load() {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"SECRET-do-not-echo"}`)
			return
		}
		if r.Form.Get("refresh_token") != "refresh-secret" {
			t.Error("unexpected refresh token")
		}
		fmt.Fprint(w, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`)
	})
	ready(t, s, id, true)
	second := New(st, s.cipher)
	second.authBase = s.authBase
	second.client = s.client
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := s
			if i%2 == 1 {
				svc = second
			}
			token, _, err := svc.Credential(context.Background(), id)
			if err != nil || token != "rotated-access" {
				t.Errorf("refresh error %v", err)
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("rotated concurrently %d", calls.Load())
	}
	if err := s.update(context.Background(), id, func(v *state) error {
		if v.Refresh != "rotated-refresh" {
			t.Error("rotation not persisted")
		}
		v.TokenExpires = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reject.Store(true)
	_, _, err := s.Credential(context.Background(), id)
	if !errors.Is(err, ErrReauthorize) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid-grant error %v", err)
	}
	status, err := second.Status(context.Background(), id)
	if err != nil || status.State != "requires_reauth" {
		t.Fatal("recovery not persisted")
	}
	_, _, _ = second.Credential(context.Background(), id)
	if calls.Load() != 2 {
		t.Fatal("rejected credential retried")
	}
}
func TestExpiryCancellationAndUpstreamRedaction(t *testing.T) {
	s, _, id := harness(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"device_auth_id":"secret","user_code":"CODE","expires_in":1}`)
	})
	now := time.Now()
	s.now = func() time.Time { return now }
	if _, err := s.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	status, err := s.Poll(context.Background(), id)
	if err != nil || status.State != "expired" || status.UserCode != "" {
		t.Fatalf("expiry %+v %v", status, err)
	}
	if err := s.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Credential(context.Background(), id)
	if !errors.Is(err, ErrReauthorize) {
		t.Fatal(err)
	}
}

func TestReauthorizationCannotReplaceAccountAndStaleRejectionCannotRevoke(t *testing.T) {
	s, _, id := harness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"code","user_code":"USER"}`)
		case "/api/accounts/deviceauth/token":
			fmt.Fprint(w, `{"authorization_code":"authorization","code_verifier":"verifier"}`)
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"id_token": jwt("different-account"), "access_token": "other-access", "refresh_token": "other-refresh"})
		}
	})
	ready(t, s, id, false)
	ctx := context.Background()
	if err := s.Reject(ctx, id, "stale-token"); err != nil {
		t.Fatal(err)
	}
	status, _ := s.Status(ctx, id)
	if status.State != "ready" {
		t.Fatal("stale rejection revoked active token")
	}
	if _, err := s.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Poll(ctx, id); err == nil || !strings.Contains(err.Error(), "original ChatGPT account") {
		t.Fatalf("account changed: %v", err)
	}
	status, _ = s.Status(ctx, id)
	if status.State != "requires_reauth" {
		t.Fatal("different account became ready")
	}
}

func TestCancelPreservesOldLoginIdempotentlyAndRevokesRacingCompletion(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			s, _, id := harness(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					fmt.Fprint(w, `{"device_auth_id":"code","user_code":"USER"}`)
				case "/api/accounts/deviceauth/token":
					fmt.Fprint(w, `{"authorization_code":"authorization","code_verifier":"verifier"}`)
				case "/oauth/token":
					_ = json.NewEncoder(w).Encode(map[string]any{"id_token": jwt("account-fixture"), "access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
				}
			})
			ready(t, s, id, false)
			ctx := context.Background()
			if _, err := s.Start(ctx, id); err != nil {
				t.Fatal(err)
			}
			if completed {
				result, err := s.Poll(ctx, id)
				if err != nil || result.State != "ready" {
					t.Fatalf("poll: %+v %v", result, err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := s.Cancel(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			token, _, err := s.Credential(ctx, id)
			if completed {
				if !errors.Is(err, ErrReauthorize) {
					t.Fatalf("racing completion survived cancellation: %v", err)
				}
			} else if err != nil || token != "access-secret" {
				t.Fatalf("old login revoked by retried cancellation: %v", err)
			}
		})
	}
}
