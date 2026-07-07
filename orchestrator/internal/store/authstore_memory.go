package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func memberKey(projectID, userID string) string { return projectID + "|" + userID }

// --- users & identities ------------------------------------------------------

func (m *MemStore) CreateUserWithIdentity(_ context.Context, u *domain.User, id *domain.UserIdentity) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The mutex already serialises this decision (mirrors the PG advisory lock).
	first := len(m.users) == 0
	u.IsClusterAdmin = first
	m.users[u.ID] = *u
	id.UserID = u.ID
	m.identities[id.ID] = *id
	return first, nil
}

func (m *MemStore) GetUser(_ context.Context, id string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := u
	return &cp, nil
}

func (m *MemStore) GetIdentity(_ context.Context, provider domain.GitProvider, providerUID string) (*domain.UserIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.identities {
		if id.Provider == provider && id.ProviderUID == providerUID {
			cp := id
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListIdentities(_ context.Context, userID string) ([]domain.UserIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.UserIdentity
	for _, id := range m.identities {
		if id.UserID == userID {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateIdentityToken(_ context.Context, identityID string, accessEnc, refreshEnc []byte, expiresAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.identities[identityID]
	if !ok {
		return ErrNotFound
	}
	id.AccessTokenEnc = accessEnc
	id.RefreshTokenEnc = refreshEnc
	id.TokenExpiresAt = expiresAt
	m.identities[identityID] = id
	return nil
}

func (m *MemStore) AttachIdentity(_ context.Context, userID string, id *domain.UserIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for existingID, ex := range m.identities {
		if ex.Provider == id.Provider && ex.ProviderUID == id.ProviderUID {
			if ex.UserID != userID {
				return ErrIdentityTaken
			}
			ex.Username = id.Username
			ex.AccessTokenEnc = id.AccessTokenEnc
			ex.RefreshTokenEnc = id.RefreshTokenEnc
			ex.TokenExpiresAt = id.TokenExpiresAt
			m.identities[existingID] = ex
			id.UserID = userID
			return nil
		}
	}
	id.UserID = userID
	m.identities[id.ID] = *id
	return nil
}

func (m *MemStore) CountUsers(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *MemStore) SearchUsers(_ context.Context, q string, limit int) ([]domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	ql := strings.ToLower(strings.TrimSpace(q))
	// Precompute usernames per user for the identity-username match.
	usernames := map[string][]string{}
	for _, id := range m.identities {
		usernames[id.UserID] = append(usernames[id.UserID], strings.ToLower(id.Username))
	}
	var out []domain.User
	for _, u := range m.users {
		if ql == "" || strings.Contains(strings.ToLower(u.DisplayName), ql) || anyContains(usernames[u.ID], ql) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func anyContains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

func (m *MemStore) GetUserByProviderUsername(_ context.Context, provider domain.GitProvider, username string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.identities {
		if id.Provider == provider && id.Username == username {
			if u, ok := m.users[id.UserID]; ok {
				cp := u
				return &cp, nil
			}
		}
	}
	return nil, ErrNotFound
}

// --- sessions ----------------------------------------------------------------

func (m *MemStore) CreateSession(_ context.Context, s *domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = *s
	return nil
}

func (m *MemStore) GetUserBySessionToken(_ context.Context, tokenHash string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	now := time.Now()
	for _, s := range m.sessions {
		if s.TokenHash == tokenHash && s.Valid(now) {
			if u, ok := m.users[s.UserID]; ok {
				cp := u
				return &cp, nil
			}
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) RevokeSession(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.TokenHash == tokenHash && s.RevokedAt == nil {
			t := time.Now().UTC()
			s.RevokedAt = &t
			m.sessions[k] = s
		}
	}
	return nil
}

// --- members -----------------------------------------------------------------

func (m *MemStore) ListMembers(_ context.Context, projectID string) ([]domain.ProjectMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ProjectMember
	for _, mem := range m.members {
		if mem.ProjectID == projectID {
			out = append(out, mem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) GetMember(_ context.Context, projectID, userID string) (*domain.ProjectMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[memberKey(projectID, userID)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := mem
	return &cp, nil
}

func (m *MemStore) UpsertMember(_ context.Context, mem *domain.ProjectMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = time.Now().UTC()
	}
	k := memberKey(mem.ProjectID, mem.UserID)
	if existing, ok := m.members[k]; ok {
		// Preserve original created_at on a role update.
		mem.CreatedAt = existing.CreatedAt
	}
	m.members[k] = *mem
	return nil
}

func (m *MemStore) RemoveMember(_ context.Context, projectID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey(projectID, userID)
	if _, ok := m.members[k]; !ok {
		return ErrNotFound
	}
	delete(m.members, k)
	return nil
}

func (m *MemStore) CountProjectOwners(_ context.Context, projectID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, mem := range m.members {
		if mem.ProjectID == projectID && mem.Role == domain.RoleOwner {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) ListProjectsForUser(_ context.Context, userID string) ([]domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Project
	for _, mem := range m.members {
		if mem.UserID == userID {
			if p, ok := m.projects[mem.ProjectID]; ok {
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
