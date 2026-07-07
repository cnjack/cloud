package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

// firstUserLockKey is an arbitrary constant for pg_advisory_xact_lock so all
// concurrent "create the first user" transactions serialise on the same lock —
// this is what makes the is_cluster_admin decision race-free (blueprint §2).
const firstUserLockKey = int64(0x6a636c6f75643031) // "jcloud01"

const userCols = `id, display_name, avatar_url, is_cluster_admin, created_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.DisplayName, &u.AvatarURL, &u.IsClusterAdmin, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return &u, nil
}

const identityCols = `id, user_id, provider, provider_uid, username,
	access_token_enc, refresh_token_enc, token_expires_at, created_at`

func scanIdentity(row pgx.Row) (*domain.UserIdentity, error) {
	var id domain.UserIdentity
	err := row.Scan(&id.ID, &id.UserID, &id.Provider, &id.ProviderUID, &id.Username,
		&id.AccessTokenEnc, &id.RefreshTokenEnc, &id.TokenExpiresAt, &id.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan identity: %w", err)
	}
	return &id, nil
}

func (s *PGStore) CreateUserWithIdentity(ctx context.Context, u *domain.User, id *domain.UserIdentity) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin create user: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialise the first-user decision so two concurrent first logins cannot both
	// read count==0 and both become cluster admin. The lock is released at commit.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, firstUserLockKey); err != nil {
		return false, fmt.Errorf("advisory lock: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	first := count == 0
	u.IsClusterAdmin = first

	if _, err := tx.Exec(ctx,
		`INSERT INTO users (`+userCols+`) VALUES ($1,$2,$3,$4,$5)`,
		u.ID, u.DisplayName, u.AvatarURL, u.IsClusterAdmin, u.CreatedAt); err != nil {
		return false, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_identities (`+identityCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id.ID, u.ID, string(id.Provider), id.ProviderUID, id.Username,
		id.AccessTokenEnc, id.RefreshTokenEnc, nullTime(id.TokenExpiresAt), id.CreatedAt); err != nil {
		return false, fmt.Errorf("insert identity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit create user: %w", err)
	}
	id.UserID = u.ID
	return first, nil
}

func (s *PGStore) GetUser(ctx context.Context, id string) (*domain.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (s *PGStore) GetIdentity(ctx context.Context, provider domain.GitProvider, providerUID string) (*domain.UserIdentity, error) {
	return scanIdentity(s.pool.QueryRow(ctx,
		`SELECT `+identityCols+` FROM user_identities WHERE provider=$1 AND provider_uid=$2`,
		string(provider), providerUID))
}

// GetIdentityForUser returns a user's identity on a specific provider (the token
// the M3 draft-PR / review passes push and review with). ErrNotFound if the user
// has not linked that provider.
func (s *PGStore) GetIdentityForUser(ctx context.Context, userID string, provider domain.GitProvider) (*domain.UserIdentity, error) {
	return scanIdentity(s.pool.QueryRow(ctx,
		`SELECT `+identityCols+` FROM user_identities WHERE user_id=$1 AND provider=$2
		 ORDER BY created_at ASC LIMIT 1`, userID, string(provider)))
}

func (s *PGStore) ListIdentities(ctx context.Context, userID string) ([]domain.UserIdentity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+identityCols+` FROM user_identities WHERE user_id=$1 ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	var out []domain.UserIdentity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *id)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateIdentityToken(ctx context.Context, identityID string, accessEnc, refreshEnc []byte, expiresAt *time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_identities SET access_token_enc=$2, refresh_token_enc=$3, token_expires_at=$4 WHERE id=$1`,
		identityID, accessEnc, refreshEnc, nullTime(expiresAt))
	if err != nil {
		return fmt.Errorf("update identity token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) AttachIdentity(ctx context.Context, userID string, id *domain.UserIdentity) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attach identity: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var existingUser, existingID string
	err = tx.QueryRow(ctx,
		`SELECT id, user_id FROM user_identities WHERE provider=$1 AND provider_uid=$2 FOR UPDATE`,
		string(id.Provider), id.ProviderUID).Scan(&existingID, &existingUser)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// New identity for this user.
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_identities (`+identityCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id.ID, userID, string(id.Provider), id.ProviderUID, id.Username,
			id.AccessTokenEnc, id.RefreshTokenEnc, nullTime(id.TokenExpiresAt), id.CreatedAt); err != nil {
			return fmt.Errorf("insert linked identity: %w", err)
		}
	case err != nil:
		return fmt.Errorf("lookup identity: %w", err)
	case existingUser != userID:
		return ErrIdentityTaken
	default:
		// Already ours: refresh tokens + username.
		if _, err := tx.Exec(ctx,
			`UPDATE user_identities SET username=$2, access_token_enc=$3, refresh_token_enc=$4, token_expires_at=$5 WHERE id=$1`,
			existingID, id.Username, id.AccessTokenEnc, id.RefreshTokenEnc, nullTime(id.TokenExpiresAt)); err != nil {
			return fmt.Errorf("refresh linked identity: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attach identity: %w", err)
	}
	id.UserID = userID
	return nil
}

func (s *PGStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

func (s *PGStore) SearchUsers(ctx context.Context, q string, limit int) ([]domain.User, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Match display_name or any linked identity username, case-insensitively.
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT `+prefixCols("u", userCols)+`
		 FROM users u
		 LEFT JOIN user_identities i ON i.user_id = u.id
		 WHERE ($1 = '' OR u.display_name ILIKE '%'||$1||'%' OR i.username ILIKE '%'||$1||'%')
		 ORDER BY u.display_name ASC
		 LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *PGStore) GetUserByProviderUsername(ctx context.Context, provider domain.GitProvider, username string) (*domain.User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`SELECT `+prefixCols("u", userCols)+`
		 FROM users u JOIN user_identities i ON i.user_id = u.id
		 WHERE i.provider=$1 AND i.username=$2
		 ORDER BY i.created_at ASC LIMIT 1`, string(provider), username))
}

// --- sessions ---------------------------------------------------------------

func (s *PGStore) CreateSession(ctx context.Context, sess *domain.Session) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.CreatedAt, sess.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *PGStore) GetUserBySessionToken(ctx context.Context, tokenHash string) (*domain.User, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.pool.QueryRow(ctx,
		`SELECT `+prefixCols("u", userCols)+`
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at > now()`, tokenHash))
}

func (s *PGStore) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, tokenHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// --- members ----------------------------------------------------------------

func scanMember(row pgx.Row) (*domain.ProjectMember, error) {
	var m domain.ProjectMember
	err := row.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan member: %w", err)
	}
	return &m, nil
}

func (s *PGStore) ListMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT project_id, user_id, role, created_at FROM project_members WHERE project_id=$1 ORDER BY created_at ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var out []domain.ProjectMember
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *PGStore) GetMember(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error) {
	return scanMember(s.pool.QueryRow(ctx,
		`SELECT project_id, user_id, role, created_at FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, userID))
}

func (s *PGStore) UpsertMember(ctx context.Context, m *domain.ProjectMember) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role, created_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (project_id, user_id) DO UPDATE SET role=EXCLUDED.role`,
		m.ProjectID, m.UserID, string(m.Role), m.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert member: %w", err)
	}
	return nil
}

func (s *PGStore) RemoveMember(ctx context.Context, projectID, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) CountProjectOwners(ctx context.Context, projectID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM project_members WHERE project_id=$1 AND role='owner'`, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count owners: %w", err)
	}
	return n, nil
}

func (s *PGStore) ListProjectsForUser(ctx context.Context, userID string) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+prefixCols("p", projectCols)+`
		 FROM projects p JOIN project_members m ON m.project_id = p.id
		 WHERE m.user_id=$1 ORDER BY p.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects for user: %w", err)
	}
	defer rows.Close()
	var out []domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
