package api

import (
	"net/http"

	"github.com/cnjack/jcloud/internal/domain"
)

// meUser is the user block of GET /api/v1/me.
type meUser struct {
	ID             string `json:"id,omitempty"`
	DisplayName    string `json:"display_name"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	IsClusterAdmin bool   `json:"is_cluster_admin"`
}

// meIdentity is one linked identity in GET /api/v1/me.
type meIdentity struct {
	Provider string `json:"provider"`
	Username string `json:"username"`
}

// meResponse is the GET /api/v1/me body. The console (M4) depends on this shape:
// every principal kind returns 200; only an unauthenticated request 401s.
type meResponse struct {
	User      meUser `json:"user"`
	IsService bool   `json:"is_service,omitempty"`
	// Kind names the principal kind so the console can tell an api_key subject
	// apart from a human user or the service token: "user", "service", or
	// "api_key" (F12 / D24). Optional (omitempty) — a client predating this
	// field simply ignores it.
	Kind string `json:"kind,omitempty"`
	// ScopedProjectID / Role are populated ONLY for an api_key principal (F12):
	// the single project it is bound to and its effective role there (member).
	// Empty for a user / service principal.
	ScopedProjectID string       `json:"scoped_project_id,omitempty"`
	Role            string       `json:"role,omitempty"`
	Identities      []meIdentity `json:"identities"`
}

// handleMe returns the current principal. For the CONSOLE_TOKEN service
// principal it returns a synthetic cluster-admin user with is_service=true and no
// identities. For a project-scoped API key (F12 / D24) it returns a minimal,
// honest identity (kind=api_key, its bound project + member role) WITHOUT
// dereferencing the nil p.user. For a real user it returns the user plus its
// linked identities.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.service {
		writeJSON(w, http.StatusOK, meResponse{
			User:       meUser{DisplayName: "console token", IsClusterAdmin: true},
			IsService:  true,
			Kind:       "service",
			Identities: []meIdentity{},
		})
		return
	}
	// Project-scoped API key: a scoped principal has NO user record (p.user is
	// nil — dereferencing it would panic → 500). Return the minimal honest form
	// naming the kind + bound project + effective role, not a fabricated user.
	if p.isAPIKey() {
		writeJSON(w, http.StatusOK, meResponse{
			User:            meUser{DisplayName: "API key"},
			Kind:            "api_key",
			ScopedProjectID: p.scopedProjectID,
			Role:            string(domain.RoleMember),
			Identities:      []meIdentity{},
		})
		return
	}

	u := p.user
	ids, err := s.st.ListIdentities(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load identities")
		return
	}
	identities := make([]meIdentity, 0, len(ids))
	for _, id := range ids {
		identities = append(identities, meIdentity{Provider: string(id.Provider), Username: id.Username})
	}
	writeJSON(w, http.StatusOK, meResponse{
		User: meUser{
			ID:             u.ID,
			DisplayName:    u.DisplayName,
			AvatarURL:      u.AvatarURL,
			IsClusterAdmin: u.IsClusterAdmin,
		},
		Kind:       "user",
		Identities: identities,
	})
}

// handleSearchUsers backs the add-member picker: GET /api/v1/users?q= (any
// logged-in HUMAN/service principal). A project-scoped API key (F12 / D24) is
// NOT permitted — the user directory is cross-project (it would let a scoped
// key enumerate every deployment user and spot cluster admins), so a scoped
// principal is a 403, consistent with the cluster-admin-surface exclusion.
func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	if principalFrom(r.Context()).isAPIKey() {
		writeError(w, http.StatusForbidden, "forbidden",
			"project-scoped API keys cannot enumerate users")
		return
	}
	q := r.URL.Query().Get("q")
	users, err := s.st.SearchUsers(r.Context(), q, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not search users")
		return
	}
	out := make([]meUser, 0, len(users))
	for _, u := range users {
		out = append(out, meUser{ID: u.ID, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL, IsClusterAdmin: u.IsClusterAdmin})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}
