package domain

import "time"

// Role is a user's role inside a single project (multitenant blueprint §2 RBAC
// matrix). Ordered owner > member > viewer.
type Role string

const (
	// RoleOwner may change project settings, manage members, create/edit
	// services, delete the project, and do everything a member can.
	RoleOwner Role = "owner"
	// RoleMember may trigger/retry/cancel runs and request reviews, plus
	// everything a viewer can.
	RoleMember Role = "member"
	// RoleViewer may only view the project, its runs, diffs and PRs.
	RoleViewer Role = "viewer"
)

// ValidRole reports whether r is a recognised project role.
func ValidRole(r Role) bool {
	switch r {
	case RoleOwner, RoleMember, RoleViewer:
		return true
	}
	return false
}

// rank orders roles so authorization can compare "at least" thresholds.
func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether r grants at least the permissions of min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() }

// User is a human principal. The first user to log in becomes the cluster admin
// (blueprint §2). A user may have several linked identities across providers.
type User struct {
	ID             string    `json:"id"`
	DisplayName    string    `json:"display_name"`
	AvatarURL      string    `json:"avatar_url"`
	IsClusterAdmin bool      `json:"is_cluster_admin"`
	CreatedAt      time.Time `json:"created_at"`
}

// UserIdentity is a provider account linked to a User. The access/refresh tokens
// are stored AES-256-GCM encrypted (blueprint §1) and are NEVER serialised to an
// API client — the byte slices carry ciphertext and are json:"-".
type UserIdentity struct {
	ID              string      `json:"id"`
	UserID          string      `json:"user_id"`
	Provider        GitProvider `json:"provider"`
	ProviderUID     string      `json:"provider_uid"`
	Username        string      `json:"username"`
	AccessTokenEnc  []byte      `json:"-"`
	RefreshTokenEnc []byte      `json:"-"`
	TokenExpiresAt  *time.Time  `json:"token_expires_at,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
}

// Session is an opaque browser session. Only TokenHash (sha256 of the opaque
// token) is persisted; the plaintext lives only in the jcloud_session cookie.
type Session struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	TokenHash string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Valid reports whether the session is currently usable (not revoked, not
// expired) at time now.
func (s *Session) Valid(now time.Time) bool {
	return s.RevokedAt == nil && s.ExpiresAt.After(now)
}

// ProjectMember binds a user to a project with a role (blueprint §1/§2).
type ProjectMember struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}
