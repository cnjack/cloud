package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Cookie names for the browser auth flow.
const (
	// sessionCookieName holds the opaque session token (httpOnly, SameSite=Lax).
	sessionCookieName = "jcloud_session"
	// stateCookieName holds the OAuth CSRF nonce during a login/link round trip.
	stateCookieName = "jcloud_oauth_state"
)

// principal is the authenticated subject of a request. Exactly one of these is
// true: a human `user` (OAuth login/session), a `service` principal (the
// CONSOLE_TOKEN, treated as a virtual cluster admin with user_id=null), a
// project-scoped API key (F12 / D24 — scopedProjectID non-empty), or a device
// (docs/17 — deviceID non-empty). A nil principal means unauthenticated.
type principal struct {
	user         *domain.User // nil for the service principal / an API key / a device
	service      bool         // authenticated via CONSOLE_TOKEN
	sessionToken string       // plaintext session token (for logout); "" for service/API key
	// scopedProjectID, when non-empty, means this principal authenticated with a
	// project-scoped API key (F12 / D24): a Bearer token with the "jck_" prefix
	// resolved (by SHA-256) to a revocable, project-bound credential. Its
	// authority is capped at RoleMember on EXACTLY this one project — see
	// effectiveRole — and it can never reach a cluster-admin surface or manage
	// API keys itself (no self-renewal privilege escalation). This is the
	// security core of F12: a leaked project key cannot reach another project.
	scopedProjectID string
	// deviceID, when non-empty, means this principal authenticated with a device
	// token (docs/17 §3.2): a "jcd_"-prefixed Bearer token resolved (by SHA-256)
	// to a revocable, device-bound credential. A device is NOT a user — user is
	// nil so it is never cluster-admin and holds no project role; only the
	// /internal/v1/device/* endpoints accept it (they assert isDevice).
	// deviceUserID is the device's owning user, carried for attribution.
	deviceID     string
	deviceUserID string
}

// isClusterAdmin reports whether the principal has cluster-admin authority. The
// service principal is always cluster-admin (script compatibility); a user is
// admin iff IsClusterAdmin is set. A project-scoped API key is NEVER
// cluster-admin (isAPIKey below), regardless of who created it.
func (p *principal) isClusterAdmin() bool {
	if p == nil {
		return false
	}
	return p.service || (p.user != nil && p.user.IsClusterAdmin)
}

// isAPIKey reports whether the principal authenticated with a project-scoped
// API key (F12 / D24). Its authority is capped at RoleMember on exactly
// scopedProjectID (see effectiveRole) and it is excluded from cluster-admin
// surfaces regardless of that cap (see handleGetSystem; the model/kanban admin
// surfaces are already gated by requireClusterAdmin, which isClusterAdmin
// above reports false for).
func (p *principal) isAPIKey() bool {
	return p != nil && p.scopedProjectID != ""
}

// isDevice reports whether the principal authenticated with a device token
// (docs/17 §3.2). Device principals are accepted ONLY by the
// /internal/v1/device/* endpoints; everywhere else a device is just "not a
// user" (no project role, never cluster-admin).
func (p *principal) isDevice() bool {
	return p != nil && p.deviceID != ""
}

// userID returns the human user's id, or "" for the service principal.
func (p *principal) userID() string {
	if p != nil && p.user != nil {
		return p.user.ID
	}
	return ""
}

// userIDPtr returns a *string for persistence (nil for the service principal, so
// runs.triggered_by_user_id / projects.owner_user_id land as NULL).
func (p *principal) userIDPtr() *string {
	if id := p.userID(); id != "" {
		return &id
	}
	return nil
}

// principalCtxKey is the context key for the resolved principal.
type principalCtxKey struct{}

func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// principalFrom returns the principal placed in the context by the auth
// middleware (nil if none — only on unauthenticated paths).
func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(principalCtxKey{}).(*principal)
	return p
}

// resolvePrincipal identifies the request subject following the blueprint §2
// order: jcloud_session cookie → Bearer session token → Bearer CONSOLE_TOKEN.
// allowQuery additionally accepts a ?access_token= param (SSE/download, where a
// browser EventSource cannot set a header). Returns (nil,false) if unresolved.
func (s *Server) resolvePrincipal(r *http.Request, allowQuery bool) (*principal, bool) {
	ctx := r.Context()

	// 1. Session cookie.
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if u, err := s.st.GetUserBySessionToken(ctx, auth.HashToken(c.Value)); err == nil {
			return &principal{user: u, sessionToken: c.Value}, true
		}
	}

	// 2/3. Bearer header, or ?access_token= for stream/download endpoints.
	tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok && allowQuery {
		tok = r.URL.Query().Get("access_token")
	}
	if tok != "" {
		// CONSOLE_TOKEN → virtual cluster-admin service principal.
		if s.cfg.ConsoleToken != "" && auth.ConstantTimeEqual(tok, s.cfg.ConsoleToken) {
			return &principal{service: true}, true
		}
		// Project-scoped API key (F12 / D24): a "jck_"-prefixed token resolves by
		// its SHA-256 to a revocable, project-bound credential (store excludes
		// revoked rows, so a revoked key falls straight through to "unresolved" —
		// same 401 as an unknown token; revocation is effective on the very next
		// lookup, no cache to invalidate). A "jck_" token is never a session
		// token, so it does NOT fall through to the session lookup below.
		if strings.HasPrefix(tok, auth.APIKeyTokenPrefix) {
			if k, err := s.st.GetAPIKeyByHash(ctx, auth.HashToken(tok)); err == nil {
				s.touchAPIKeyLastUsed(ctx, k)
				return &principal{scopedProjectID: k.ProjectID}, true
			}
			return nil, false
		}
		// Device token (docs/17 §3.2): a "jcd_"-prefixed token resolves by its
		// SHA-256 to a revocable, device-bound credential. The store excludes
		// revoked tokens and revoked devices, so a revoked credential falls
		// straight through to "unresolved" — same 401 as an unknown token,
		// effective on the very next lookup. A "jcd_" token is never a session
		// token, so it does NOT fall through to the session lookup below.
		if strings.HasPrefix(tok, auth.DeviceTokenPrefix) {
			if dt, err := s.st.GetDeviceTokenByHash(ctx, auth.HashToken(tok)); err == nil {
				return &principal{deviceID: dt.DeviceID, deviceUserID: dt.UserID}, true
			}
			return nil, false
		}
		// Otherwise treat it as a session token.
		if u, err := s.st.GetUserBySessionToken(ctx, auth.HashToken(tok)); err == nil {
			return &principal{user: u, sessionToken: tok}, true
		}
	}
	return nil, false
}

// apiKeyLastUsedThrottle caps how often a scoped principal's last_used_at is
// refreshed. A hot key firing many requests a minute must not turn into a
// synchronous DB write on every single one — see touchAPIKeyLastUsed.
const apiKeyLastUsedThrottle = time.Minute

// touchAPIKeyLastUsed best-effort refreshes an API key's last_used_at, but
// only when it is stale (nil, or older than apiKeyLastUsedThrottle) — the
// throttle that keeps a hammered key's audit stamp off the hot path. Errors
// are logged, never surfaced: a failed audit stamp must not turn an otherwise
// valid credential into a failed request.
func (s *Server) touchAPIKeyLastUsed(ctx context.Context, k *domain.APIKey) {
	now := time.Now().UTC()
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < apiKeyLastUsedThrottle {
		return
	}
	if err := s.st.UpdateAPIKeyLastUsed(ctx, k.ID, now); err != nil {
		s.log.Error("update api key last_used_at", "key", k.ID, "err", err)
	}
}

// effectiveRole returns the principal's role on a project and whether it has any
// access at all. A cluster-admin/service principal is treated as owner on every
// project (full authority; the "owner" role name is the strongest, so responses
// report role="owner" for admins — documented on projectView.Role). A non-member
// gets (RoleViewer, false).
func (s *Server) effectiveRole(ctx context.Context, p *principal, projectID string) (domain.Role, bool, error) {
	if p.isClusterAdmin() {
		return domain.RoleOwner, true, nil
	}
	// Project-scoped API key (F12 / D24) — the security core of the
	// scoped-principal boundary: RoleMember on EXACTLY p.scopedProjectID, no
	// access to any other project. Because RoleMember never satisfies a
	// RoleOwner gate (authorizeProject below), this single branch also denies
	// every owner-level action on the key's OWN project (settings, members,
	// integrations, kanban, schedules, service create/delete, and managing API
	// keys itself) without needing a per-endpoint special case.
	if p.isAPIKey() {
		if projectID == p.scopedProjectID {
			return domain.RoleMember, true, nil
		}
		return domain.RoleViewer, false, nil
	}
	uid := p.userID()
	if uid == "" {
		return domain.RoleViewer, false, nil
	}
	m, err := s.st.GetMember(ctx, projectID, uid)
	if err == store.ErrNotFound {
		return domain.RoleViewer, false, nil
	}
	if err != nil {
		return domain.RoleViewer, false, err
	}
	return m.Role, true, nil
}

// authorizeProject enforces that the request principal holds at least `min` on
// the project. On denial it writes the error response and returns false; the
// caller must stop. A non-member is a 403 (not 404) — existence is not leaked
// beyond a fixed forbidden response, which is acceptable for this internal tool.
func (s *Server) authorizeProject(ctx context.Context, w http.ResponseWriter, p *principal, projectID string, min domain.Role) bool {
	role, hasAccess, err := s.effectiveRole(ctx, p, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve project access")
		return false
	}
	if !hasAccess {
		writeError(w, http.StatusForbidden, "forbidden", "you are not a member of this project")
		return false
	}
	if !role.AtLeast(min) {
		writeError(w, http.StatusForbidden, "forbidden", "this action requires the "+string(min)+" role")
		return false
	}
	return true
}
