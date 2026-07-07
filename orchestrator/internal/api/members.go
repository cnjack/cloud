package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// memberView is one row of GET /api/v1/projects/{id}/members, enriched with the
// user's display fields (and best-effort primary username) so the console can
// render the members list without a second lookup per member.
type memberView struct {
	UserID         string `json:"user_id"`
	Role           string `json:"role"`
	DisplayName    string `json:"display_name"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	Username       string `json:"username,omitempty"`
	IsClusterAdmin bool   `json:"is_cluster_admin"`
}

func (s *Server) memberViewOf(r *http.Request, m domain.ProjectMember) memberView {
	mv := memberView{UserID: m.UserID, Role: string(m.Role)}
	if u, err := s.st.GetUser(r.Context(), m.UserID); err == nil {
		mv.DisplayName = u.DisplayName
		mv.AvatarURL = u.AvatarURL
		mv.IsClusterAdmin = u.IsClusterAdmin
	}
	if ids, err := s.st.ListIdentities(r.Context(), m.UserID); err == nil && len(ids) > 0 {
		mv.Username = ids[0].Username
	}
	return mv
}

// handleListMembers lists a project's members. Readable by any member (viewer+).
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	members, err := s.st.ListMembers(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list members")
		return
	}
	out := make([]memberView, 0, len(members))
	for _, m := range members {
		out = append(out, s.memberViewOf(r, m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

type addMemberReq struct {
	UserID   string `json:"user_id"`
	Provider string `json:"provider"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// handleAddMember adds or updates a member. Owner/cluster-admin only. The target
// is identified by user_id OR by {provider, username}.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	var req addMemberReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	role := domain.Role(strings.TrimSpace(req.Role))
	if !domain.ValidRole(role) {
		writeError(w, http.StatusBadRequest, "bad_request", "role must be owner, member or viewer")
		return
	}

	// Resolve the target user by id, or by (provider, username).
	var target *domain.User
	var err error
	switch {
	case strings.TrimSpace(req.UserID) != "":
		target, err = s.st.GetUser(r.Context(), strings.TrimSpace(req.UserID))
	case strings.TrimSpace(req.Provider) != "" && strings.TrimSpace(req.Username) != "":
		prov := domain.GitProvider(strings.TrimSpace(req.Provider))
		if !domain.ValidProvider(prov) {
			writeError(w, http.StatusBadRequest, "bad_request", "provider must be gitea, github or gitlab")
			return
		}
		target, err = s.st.GetUserByProviderUsername(r.Context(), prov, strings.TrimSpace(req.Username))
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "provide either user_id or {provider, username}")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve user")
		return
	}

	m := &domain.ProjectMember{ProjectID: projectID, UserID: target.ID, Role: role, CreatedAt: time.Now().UTC()}
	if err := s.st.UpsertMember(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not add member")
		return
	}
	writeJSON(w, http.StatusOK, s.memberViewOf(r, *m))
}

// handleRemoveMember removes a member. Owner/cluster-admin only. It refuses to
// remove the project's last owner (there must always be at least one owner).
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	userID := r.PathValue("userID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	existing, err := s.st.GetMember(r.Context(), projectID, userID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load member")
		return
	}
	if existing.Role == domain.RoleOwner {
		owners, err := s.st.CountProjectOwners(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not count owners")
			return
		}
		if owners <= 1 {
			writeError(w, http.StatusConflict, "conflict", "cannot remove the last owner of a project")
			return
		}
	}
	if err := s.st.RemoveMember(r.Context(), projectID, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not remove member")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectExists writes a 404 and returns false when the project is missing.
func (s *Server) projectExists(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return false
	}
	return true
}
