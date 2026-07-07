package api

import (
	"context"
	"net/http"

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
// true: a human `user` (OAuth login/session) or a `service` principal (the
// CONSOLE_TOKEN, treated as a virtual cluster admin with user_id=null). A nil
// principal means unauthenticated.
type principal struct {
	user         *domain.User // nil for the service principal
	service      bool         // authenticated via CONSOLE_TOKEN
	sessionToken string       // plaintext session token (for logout); "" for service
}

// isClusterAdmin reports whether the principal has cluster-admin authority. The
// service principal is always cluster-admin (script compatibility); a user is
// admin iff IsClusterAdmin is set.
func (p *principal) isClusterAdmin() bool {
	if p == nil {
		return false
	}
	return p.service || (p.user != nil && p.user.IsClusterAdmin)
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
		// Otherwise treat it as a session token.
		if u, err := s.st.GetUserBySessionToken(ctx, auth.HashToken(tok)); err == nil {
			return &principal{user: u, sessionToken: tok}, true
		}
	}
	return nil, false
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
