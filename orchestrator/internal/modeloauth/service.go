// Package modeloauth owns encrypted personal model authorization. Only this
// control-plane package handles refresh tokens; runners receive run tokens.
package modeloauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
)

const (
	Kind            = "openai-codex"
	BaseURL         = "https://chatgpt.com/backend-api/codex"
	Protocol        = "codex_responses"
	clientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	verificationURI = "https://auth.openai.com/codex/device"
)

var ErrReauthorize = errors.New("model authorization expired; reauthorize this account in Settings > Models")
var ErrPending = errors.New("complete model authorization in Settings > Models")

type Store interface {
	WithModelOAuth(context.Context, string, func([]byte) ([]byte, error)) error
}
type Service struct {
	st       Store
	cipher   *auth.Cipher
	client   *http.Client
	authBase string
	now      func() time.Time
}

// Status is the only browser-visible representation. Device codes used for
// polling, access/refresh tokens and account identifiers never cross this API.
type Status struct {
	State           string     `json:"state"`
	UserCode        string     `json:"user_code,omitempty"`
	VerificationURI string     `json:"verification_uri,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	IntervalSeconds int        `json:"interval_seconds,omitempty"`
	Login           string     `json:"login,omitempty"`
}
type state struct {
	FlowActive    bool
	Status        Status
	Authorization string
	Verifier      string
	DeviceCode    string
	NextPoll      time.Time
	Access        string
	Refresh       string
	AccountID     string
	TokenExpires  time.Time
}

func New(st Store, cipher *auth.Cipher) *Service {
	return &Service{st: st, cipher: cipher, client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, authBase: "https://auth.openai.com", now: time.Now}
}
func (s *Service) update(ctx context.Context, id string, fn func(*state) error) error {
	if s.cipher == nil {
		return errors.New("model authorization requires the server encryption key")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.st.WithModelOAuth(ctx, id, func(enc []byte) ([]byte, error) {
		v := state{Status: Status{State: "requires_reauth"}}
		if len(enc) > 0 {
			raw, err := s.cipher.DecryptString(enc)
			if err != nil {
				return nil, errors.New("could not decrypt model authorization")
			}
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				return nil, errors.New("invalid model authorization state")
			}
		}
		if err := fn(&v); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return s.cipher.EncryptString(string(raw))
	})
}
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	var out Status
	err := s.update(ctx, id, func(v *state) error { s.expire(v); out = v.Status; return nil })
	return out, err
}
func (s *Service) expire(v *state) {
	if v.Status.State == "pending" && v.Status.ExpiresAt != nil && !s.now().Before(*v.Status.ExpiresAt) {
		v.Status.State = "expired"
		v.DeviceCode = ""
		v.Authorization = ""
		v.Verifier = ""
		v.Status.UserCode = ""
	}
}
func (s *Service) Start(ctx context.Context, id string) (Status, error) {
	var out Status
	err := s.update(ctx, id, func(v *state) error {
		code, data, err := s.request(ctx, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": clientID}, nil)
		if err != nil {
			return err
		}
		if code != 200 {
			return upstreamError("start authorization", code)
		}
		device, user := field(data, "device_auth_id"), field(data, "user_code")
		if device == "" || user == "" {
			return errors.New("authorization response omitted the device code")
		}
		expires := s.now().Add(time.Duration(seconds(data, "expires_in", 900, 86400)) * time.Second)
		v.Status = Status{State: "pending", UserCode: user, VerificationURI: verificationURI, ExpiresAt: &expires, IntervalSeconds: seconds(data, "interval", 5, 60) + 3, Login: v.Status.Login}
		v.FlowActive = true
		v.DeviceCode = device
		v.Authorization = ""
		v.Verifier = ""
		v.NextPoll = s.now()
		out = v.Status
		return nil
	})
	return out, err
}
func (s *Service) Cancel(ctx context.Context, id string) error {
	return s.update(ctx, id, func(v *state) error {
		if !v.FlowActive {
			return nil
		}
		v.FlowActive = false
		// A poll may finish while cancellation waits on the row lock. Honour that
		// cancellation without making a retried DELETE revoke a preserved old login.
		if v.Status.State == "ready" {
			v.Status.State = "requires_reauth"
			v.Access = ""
			v.Refresh = ""
			return nil
		}
		if v.Status.State == "pending" || v.Status.State == "expired" {
			login := v.Status.Login
			v.Status = Status{State: "requires_reauth", Login: login}
			v.DeviceCode = ""
			v.Authorization = ""
			v.Verifier = ""
			if v.Access != "" && v.Refresh != "" {
				v.Status.State = "ready"
			}
		}
		return nil
	})
}
func (s *Service) Poll(ctx context.Context, id string) (Status, error) {
	var out Status
	var publicErr error
	err := s.update(ctx, id, func(v *state) error {
		s.expire(v)
		if v.Status.State != "pending" || s.now().Before(v.NextPoll) {
			out = v.Status
			return nil
		}
		v.NextPoll = s.now().Add(time.Duration(v.Status.IntervalSeconds) * time.Second)
		if v.Authorization == "" {
			code, data, err := s.request(ctx, "/api/accounts/deviceauth/token", map[string]string{"device_auth_id": v.DeviceCode, "user_code": v.Status.UserCode}, nil)
			if err != nil {
				publicErr = err
				out = v.Status
				return nil
			}
			if code == 403 || code == 404 || code == 429 {
				if code == 429 {
					v.Status.IntervalSeconds = min(63, v.Status.IntervalSeconds+5)
					v.NextPoll = s.now().Add(time.Duration(v.Status.IntervalSeconds) * time.Second)
				}
				out = v.Status
				return nil
			}
			if code == 410 {
				v.Status.State = "expired"
				v.Status.UserCode = ""
				v.DeviceCode = ""
				v.Authorization = ""
				v.Verifier = ""
				out = v.Status
				return nil
			}
			if code != 200 {
				publicErr = upstreamError("poll authorization", code)
				out = v.Status
				return nil
			}
			v.Authorization, v.Verifier = field(data, "authorization_code"), field(data, "code_verifier")
			if v.Authorization == "" || v.Verifier == "" {
				publicErr = errors.New("authorization response omitted exchange fields")
				return nil
			}
		}

		code, data, err := s.request(ctx, "/oauth/token", nil, url.Values{"grant_type": {"authorization_code"}, "code": {v.Authorization}, "code_verifier": {v.Verifier}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}, "client_id": {clientID}})
		if err != nil {
			publicErr = err
			return nil
		}
		if code != 200 {
			publicErr = upstreamError("exchange authorization", code)
			return nil
		}
		account, login := identity(data)
		if account == "" || field(data, "access_token") == "" || field(data, "refresh_token") == "" {
			publicErr = errors.New("authorization response omitted account credentials")
			return nil
		}
		if v.AccountID != "" && v.AccountID != account {
			v.Status.State = "requires_reauth"
			v.DeviceCode = ""
			v.Authorization = ""
			v.Verifier = ""
			v.Status.UserCode = ""
			publicErr = errors.New("sign in to the original ChatGPT account, or add a separate connection")
			return nil
		}
		v.AccountID = account
		v.Access = field(data, "access_token")
		v.Refresh = field(data, "refresh_token")
		v.TokenExpires = s.now().Add(time.Duration(seconds(data, "expires_in", 3600, 86400)) * time.Second)
		v.DeviceCode = ""
		v.Authorization = ""
		v.Verifier = ""
		v.Status = Status{State: "ready", Login: login}
		out = v.Status
		return nil
	})
	if err != nil {
		return Status{}, err
	}
	return out, publicErr
}
func (s *Service) Credential(ctx context.Context, id string) (string, map[string]string, error) {
	var token, account string
	var publicErr error
	err := s.update(ctx, id, func(v *state) error {
		if v.Status.State != "ready" {
			publicErr = ErrReauthorize
			if v.Status.State == "pending" {
				publicErr = ErrPending
			}
			return nil
		}
		if v.Refresh == "" || v.AccountID == "" {
			publicErr = ErrReauthorize
			v.Status.State = "requires_reauth"
			return nil
		}
		if v.Access == "" || !s.now().Add(60*time.Second).Before(v.TokenExpires) {
			code, data, err := s.request(ctx, "/oauth/token", nil, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {v.Refresh}, "client_id": {clientID}, "scope": {"openid profile email"}})
			if err != nil {
				return err
			}
			errorCode := field(data, "error")
			if code == 401 || code == 403 || errorCode == "invalid_grant" || errorCode == "invalid_token" {
				v.Status.State = "requires_reauth"
				v.Access = ""
				v.Refresh = ""
				publicErr = ErrReauthorize
				return nil
			}
			if code != 200 || errorCode != "" {
				return upstreamError("refresh authorization", code)
			}
			access := field(data, "access_token")
			if access == "" {
				return errors.New("authorization refresh omitted access token")
			}
			v.Access = access
			if refresh := field(data, "refresh_token"); refresh != "" {
				v.Refresh = refresh
			}
			v.TokenExpires = s.now().Add(time.Duration(seconds(data, "expires_in", 3600, 86400)) * time.Second)
		}
		token = v.Access
		account = v.AccountID
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if publicErr != nil {
		return "", nil, publicErr
	}
	return token, map[string]string{"ChatGPT-Account-ID": account, "OpenAI-Beta": "responses=experimental", "originator": "codex_cli_rs", "User-Agent": "jcode-cloud-codex-oauth", "version": "0.144.1"}, nil
}
func (s *Service) request(ctx context.Context, path string, body any, form url.Values) (int, map[string]any, error) {
	var reader io.Reader
	contentType := "application/json"
	if form != nil {
		reader = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+path, reader)
	if err != nil {
		return 0, nil, errors.New("could not create authorization request")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "jcode-codex-oauth")
	res, err := s.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("model authorization service is unreachable; retry")
	}
	defer res.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&data); err != nil && res.StatusCode == 200 {
		return res.StatusCode, nil, errors.New("invalid authorization service response")
	}
	return res.StatusCode, data, nil
}
func upstreamError(action string, code int) error {
	return fmt.Errorf("could not %s (provider HTTP %d); retry or reauthorize in Settings > Models", action, code)
}
func field(v map[string]any, key string) string { s, _ := v[key].(string); return s }
func seconds(v map[string]any, key string, fallback, maximum int) int {
	n := fallback
	switch x := v[key].(type) {
	case float64:
		n = int(x)
	case string:
		if parsed, e := strconv.Atoi(x); e == nil {
			n = parsed
		}
	}
	return max(1, min(maximum, n))
}

// Tokens come only from the fixed TLS token endpoint, never from a browser.
// Decoding extracts display/routing metadata; it is not a JWT verifier.
func identity(tokens map[string]any) (string, string) {
	for _, key := range []string{"id_token", "access_token"} {
		parts := strings.Split(field(tokens, key), ".")
		if len(parts) != 3 {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims map[string]any
		if json.Unmarshal(raw, &claims) != nil {
			continue
		}
		id := field(claims, "chatgpt_account_id")
		if id == "" {
			nested, _ := claims["https://api.openai.com/auth"].(map[string]any)
			id = field(nested, "chatgpt_account_id")
		}
		if id == "" {
			if orgs, ok := claims["organizations"].([]any); ok && len(orgs) > 0 {
				org, _ := orgs[0].(map[string]any)
				id = field(org, "id")
			}
		}
		if id != "" {
			return id, field(claims, "email")
		}
	}
	return "", ""
}

// Reject invalidates only the credential used by this request. A delayed 401
// must not revoke a newly authorized or rotated credential from another request.
func (s *Service) Reject(ctx context.Context, id, token string) error {
	return s.update(ctx, id, func(v *state) error {
		if token != "" && v.Access == token && v.Status.State == "ready" {
			v.Status.State = "requires_reauth"
			v.Access = ""
			v.Refresh = ""
		}
		return nil
	})
}
