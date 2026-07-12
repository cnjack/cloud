package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// providerDisplayNames maps a provider id to its human label for /auth/providers.
var providerDisplayNames = map[domain.GitProvider]string{
	domain.ProviderGitea:  "Gitea",
	domain.ProviderGitHub: "GitHub",
	domain.ProviderGitLab: "GitLab",
}

// authProviderInfo is one entry in GET /auth/providers.
type authProviderInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LoginURL string `json:"login_url"`
}

// handleAuthProviders lists the configured OAuth providers (unauthenticated).
// With none configured it returns an empty list — the console then shows only
// the CONSOLE_TOKEN "advanced" path (backward compatible).
func (s *Server) handleAuthProviders(w http.ResponseWriter, _ *http.Request) {
	out := make([]authProviderInfo, 0, len(s.oauth))
	for id := range s.oauth {
		name := providerDisplayNames[id]
		if name == "" {
			name = string(id)
		}
		out = append(out, authProviderInfo{ID: string(id), Name: name, LoginURL: "/auth/login/" + string(id)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// --- state signing ----------------------------------------------------------

// oauthState is the CSRF/flow state carried in the provider round trip. It is
// HMAC-signed (stateKey) and cross-checked against the nonce stored in the
// stateCookie, so a forged callback cannot drive a login/link.
type oauthState struct {
	Nonce    string `json:"n"`
	Provider string `json:"p"`
	Mode     string `json:"m"` // "login" | "link"
	UserID   string `json:"u"` // set for link mode
	// ReturnTo is a verified same-console relative path. It is signed together
	// with the rest of state so a post-OAuth redirect cannot be forged.
	ReturnTo string `json:"r,omitempty"`
}

const (
	oauthModeLogin = "login"
	oauthModeLink  = "link"
)

func (s *Server) signState(st oauthState) string {
	payload, _ := json.Marshal(st)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write([]byte(p))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return p + "." + sig
}

func (s *Server) parseState(raw string) (oauthState, bool) {
	p, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return oauthState{}, false
	}
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write([]byte(p))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return oauthState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return oauthState{}, false
	}
	var st oauthState
	if err := json.Unmarshal(payload, &st); err != nil {
		return oauthState{}, false
	}
	return st, true
}

// --- login / link start -----------------------------------------------------

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	s.startOAuth(w, r, oauthModeLogin, "")
}

// handleAuthLink starts an identity-link flow for the already-logged-in user. A
// service principal has no user to link to, so it is rejected.
func (s *Server) handleAuthLink(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.userID() == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot link an identity to the service principal")
		return
	}
	s.startOAuth(w, r, oauthModeLink, p.userID())
}

// startOAuth issues the CSRF nonce cookie + signed state and 302s to the
// provider authorize URL (built from the EXTERNAL host).
func (s *Server) startOAuth(w http.ResponseWriter, r *http.Request, mode, userID string) {
	providerID := r.PathValue("provider")
	prov, ok := s.oauth[domain.GitProvider(providerID)]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown or unconfigured provider")
		return
	}
	nonce := randToken()
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    nonce,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   requestScheme(r) == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 minutes to complete the round trip
	})
	state := s.signState(oauthState{
		Nonce: nonce, Provider: providerID, Mode: mode, UserID: userID,
		ReturnTo: safeOAuthReturnTo(r.URL.Query().Get("return_to")),
	})
	redirectURI := s.callbackRedirectURI(r, providerID)
	http.Redirect(w, r, prov.AuthorizeURL(state, redirectURI), http.StatusFound)
}

// --- callback ---------------------------------------------------------------

func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider")
	prov, ok := s.oauth[domain.GitProvider(providerID)]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown or unconfigured provider")
		return
	}
	// Always clear the state cookie: the round trip is over either way.
	s.clearStateCookie(w, r)

	if s.cipher == nil {
		s.redirectConsole(w, r, map[string]string{"login_error": "server_misconfigured"})
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.log.Warn("oauth callback provider error", "provider", providerID, "error", errParam)
		s.redirectConsole(w, r, map[string]string{"login_error": "provider_denied"})
		return
	}

	// Verify state signature + nonce cookie (double-submit CSRF check).
	st, valid := s.parseState(r.URL.Query().Get("state"))
	cookie, cerr := r.Cookie(stateCookieName)
	if !valid || st.Provider != providerID || cerr != nil ||
		subtle.ConstantTimeCompare([]byte(st.Nonce), []byte(cookie.Value)) != 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid or expired oauth state")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing authorization code")
		return
	}
	ctx := r.Context()
	redirectURI := s.callbackRedirectURI(r, providerID)
	tok, err := prov.Exchange(ctx, code, redirectURI)
	if err != nil {
		s.log.Error("oauth token exchange", "provider", providerID, "err", err)
		s.redirectConsole(w, r, map[string]string{"login_error": "exchange_failed"})
		return
	}
	ou, err := prov.FetchUser(ctx, tok)
	if err != nil {
		s.log.Error("oauth fetch user", "provider", providerID, "err", err)
		s.redirectConsole(w, r, map[string]string{"login_error": "profile_failed"})
		return
	}

	accessEnc, refreshEnc, err := s.encryptTokens(tok)
	if err != nil {
		s.log.Error("oauth encrypt tokens", "provider", providerID, "err", err)
		s.redirectConsole(w, r, map[string]string{"login_error": "server_error"})
		return
	}
	var expiresAt *time.Time
	if !tok.Expiry.IsZero() {
		e := tok.Expiry
		expiresAt = &e
	}

	if st.Mode == oauthModeLink {
		s.completeLink(w, r, prov.ID(), ou, accessEnc, refreshEnc, expiresAt, st.UserID, st.ReturnTo)
		return
	}
	s.completeLogin(w, r, prov.ID(), ou, accessEnc, refreshEnc, expiresAt)
}

// completeLink attaches or refreshes the freshly-authorized identity on the
// current user, or redirects with ?link_error=taken when it belongs to someone
// else. Reauthorization of an already-linked account is therefore the normal
// way to grant a newly requested scope.
func (s *Server) completeLink(w http.ResponseWriter, r *http.Request, providerID domain.GitProvider, ou *provider.OAuthUser, accessEnc, refreshEnc []byte, expiresAt *time.Time, userID, returnTo string) {
	id := &domain.UserIdentity{
		ID:              domain.NewID(),
		Provider:        providerID,
		ProviderUID:     ou.ProviderUID,
		Username:        ou.Username,
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		TokenExpiresAt:  expiresAt,
		CreatedAt:       time.Now().UTC(),
	}
	err := s.st.AttachIdentity(r.Context(), userID, id)
	if errors.Is(err, store.ErrIdentityTaken) {
		s.redirectConsoleTo(w, r, returnTo, map[string]string{"link_error": "taken"})
		return
	}
	if err != nil {
		s.log.Error("attach identity", "err", err)
		s.redirectConsoleTo(w, r, returnTo, map[string]string{"link_error": "server_error"})
		return
	}
	s.redirectConsoleTo(w, r, returnTo, map[string]string{"linked": string(providerID)})
}

// completeLogin upserts the user/identity, mints a session, sets the cookie and
// redirects to CONSOLE_URL with the welcome param (blueprint §2 seam contract:
// first user => ?welcome=first-admin, other new user => ?welcome=new, returning
// user => no param).
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, providerID domain.GitProvider, ou *provider.OAuthUser, accessEnc, refreshEnc []byte, expiresAt *time.Time) {
	ctx := r.Context()
	welcome := ""
	var user *domain.User

	existing, err := s.st.GetIdentity(ctx, providerID, ou.ProviderUID)
	switch {
	case err == nil:
		// Returning user: refresh stored tokens, no welcome param.
		if err := s.st.UpdateIdentityToken(ctx, existing.ID, accessEnc, refreshEnc, expiresAt); err != nil {
			s.log.Warn("update identity token", "err", err)
		}
		user, err = s.st.GetUser(ctx, existing.UserID)
		if err != nil {
			s.log.Error("load user for identity", "err", err)
			s.redirectConsole(w, r, map[string]string{"login_error": "server_error"})
			return
		}
	case errors.Is(err, store.ErrNotFound):
		// New user + identity. First user in the system becomes cluster admin.
		u := &domain.User{
			ID:          domain.NewID(),
			DisplayName: ou.DisplayName,
			AvatarURL:   ou.AvatarURL,
			CreatedAt:   time.Now().UTC(),
		}
		id := &domain.UserIdentity{
			ID:              domain.NewID(),
			Provider:        providerID,
			ProviderUID:     ou.ProviderUID,
			Username:        ou.Username,
			AccessTokenEnc:  accessEnc,
			RefreshTokenEnc: refreshEnc,
			TokenExpiresAt:  expiresAt,
			CreatedAt:       time.Now().UTC(),
		}
		first, err := s.st.CreateUserWithIdentity(ctx, u, id)
		if err != nil {
			s.log.Error("create user with identity", "err", err)
			s.redirectConsole(w, r, map[string]string{"login_error": "server_error"})
			return
		}
		user = u
		if first {
			welcome = "first-admin"
		} else {
			welcome = "new"
		}
	default:
		s.log.Error("lookup identity", "err", err)
		s.redirectConsole(w, r, map[string]string{"login_error": "server_error"})
		return
	}

	if err := s.startSession(w, r, user.ID); err != nil {
		s.log.Error("create session", "err", err)
		s.redirectConsole(w, r, map[string]string{"login_error": "server_error"})
		return
	}

	params := map[string]string{}
	if welcome != "" {
		params["welcome"] = welcome
	}
	s.redirectConsole(w, r, params)
}

// startSession mints an opaque session token, stores its hash, and sets the
// jcloud_session cookie (httpOnly, SameSite=Lax, Path=/).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	token, err := auth.GenerateRunToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sess := &domain.Session{
		ID:        domain.NewID(),
		UserID:    userID,
		TokenHash: auth.HashToken(token),
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL()),
	}
	if err := s.st.CreateSession(r.Context(), sess); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestScheme(r) == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.sessionTTL().Seconds()),
	})
	return nil
}

// handleAuthLogout revokes the current session (if any) and clears the cookie.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p != nil && p.sessionToken != "" {
		if err := s.st.RevokeSession(r.Context(), auth.HashToken(p.sessionToken)); err != nil {
			s.log.Warn("revoke session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- helpers ----------------------------------------------------------------

func (s *Server) encryptTokens(tok *provider.OAuthToken) (accessEnc, refreshEnc []byte, err error) {
	accessEnc, err = s.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		return nil, nil, err
	}
	if tok.RefreshToken != "" {
		refreshEnc, err = s.cipher.EncryptString(tok.RefreshToken)
		if err != nil {
			return nil, nil, err
		}
	}
	return accessEnc, refreshEnc, nil
}

func (s *Server) sessionTTL() time.Duration {
	if s.cfg.SessionTTL > 0 {
		return s.cfg.SessionTTL
	}
	return 30 * 24 * time.Hour
}

// callbackRedirectURI reconstructs the OAuth redirect URI from the request so it
// matches at both authorize and token-exchange time (OAuth requires the two to
// be identical). It is the browser-facing origin, e.g.
// http://localhost:8080/auth/callback/gitea.
func (s *Server) callbackRedirectURI(r *http.Request, providerID string) string {
	return requestScheme(r) + "://" + r.Host + "/auth/callback/" + providerID
}

func (s *Server) clearStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth",
		HttpOnly: true,
		Secure:   requestScheme(r) == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// redirectConsole 302s to CONSOLE_URL, merging the given query params onto it.
func (s *Server) redirectConsole(w http.ResponseWriter, r *http.Request, params map[string]string) {
	s.redirectConsoleTo(w, r, "", params)
}

// redirectConsoleTo redirects to the configured Console origin. returnTo may
// choose only a verified relative path within that origin; it never controls the
// host, scheme, or port. This preserves a Service Automation location across an
// OAuth round trip without introducing an open redirect.
func (s *Server) redirectConsoleTo(w http.ResponseWriter, r *http.Request, returnTo string, params map[string]string) {
	base := s.cfg.ConsoleURL
	if base == "" {
		base = "http://localhost:5173"
	}
	u, err := url.Parse(base)
	if err != nil {
		http.Redirect(w, r, base, http.StatusFound)
		return
	}
	if target := safeOAuthReturnTo(returnTo); target != "" {
		if targetURL, err := url.Parse(target); err == nil {
			u.Path = targetURL.Path
			u.RawPath = targetURL.RawPath
			u.RawQuery = targetURL.RawQuery
			u.Fragment = "" // OAuth state needs no fragment and fragments are never sent to servers.
		}
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// safeOAuthReturnTo accepts a browser-local path only. Rejecting slash-slash,
// backslashes, and an absolute URL closes the common URL-parser differences that
// otherwise turn an apparently-relative value into a cross-origin redirect.
func safeOAuthReturnTo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return ""
	}
	return raw
}

func requestScheme(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		return xf
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
