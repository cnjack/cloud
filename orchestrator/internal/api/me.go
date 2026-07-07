package api

import (
	"net/http"
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
// all three principal kinds return 200; only an unauthenticated request 401s.
type meResponse struct {
	User       meUser       `json:"user"`
	IsService  bool         `json:"is_service,omitempty"`
	Identities []meIdentity `json:"identities"`
}

// handleMe returns the current principal. For the CONSOLE_TOKEN service
// principal it returns a synthetic cluster-admin user with is_service=true and no
// identities. For a real user it returns the user plus its linked identities.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.service {
		writeJSON(w, http.StatusOK, meResponse{
			User:       meUser{DisplayName: "console token", IsClusterAdmin: true},
			IsService:  true,
			Identities: []meIdentity{},
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
		Identities: identities,
	})
}

// handleSearchUsers backs the add-member picker: GET /api/v1/users?q= (any
// logged-in principal). Returns up to 20 users matching display_name or an
// identity username.
func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
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
