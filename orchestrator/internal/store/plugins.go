package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

const providerConfigCols = `provider,base_url,login_enabled,plugin_enabled,client_id,client_secret_enc,app_id,app_slug,app_private_key_enc,webhook_secret_enc,capability_version,capabilities,config_revision,last_health_error,last_capability_check,updated_by,updated_at`

func scanProviderConfig(row pgx.Row) (*domain.ProviderConfig, error) {
	var cfg domain.ProviderConfig
	var checkedAt *time.Time
	var updatedBy *string
	err := row.Scan(&cfg.Provider, &cfg.BaseURL, &cfg.LoginEnabled, &cfg.PluginEnabled, &cfg.ClientID,
		&cfg.ClientSecretEnc, &cfg.AppID, &cfg.AppSlug, &cfg.AppPrivateKeyEnc, &cfg.WebhookSecretEnc,
		&cfg.CapabilityVersion, &cfg.Capabilities, &cfg.ConfigRevision, &cfg.LastHealthError,
		&checkedAt, &updatedBy, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan provider config: %w", err)
	}
	cfg.LastCapabilityCheck = checkedAt
	if updatedBy != nil {
		cfg.UpdatedBy = *updatedBy
	}
	return &cfg, nil
}

func (s *PGStore) GetClusterSettings(ctx context.Context) (*domain.ClusterSettings, error) {
	var settings domain.ClusterSettings
	var updatedBy *string
	err := s.pool.QueryRow(ctx, `SELECT public_url,setup_complete,updated_by,updated_at FROM cluster_settings WHERE id=1`).Scan(&settings.PublicURL, &settings.SetupComplete, &updatedBy, &settings.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cluster settings: %w", err)
	}
	if updatedBy != nil {
		settings.UpdatedBy = *updatedBy
	}
	return &settings, nil
}

func (s *PGStore) UpsertClusterSettings(ctx context.Context, settings *domain.ClusterSettings) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cluster_settings(id,public_url,setup_complete,updated_by,updated_at) VALUES(1,$1,$2,$3,now()) ON CONFLICT(id) DO UPDATE SET public_url=EXCLUDED.public_url,setup_complete=EXCLUDED.setup_complete,updated_by=EXCLUDED.updated_by,updated_at=now()`, settings.PublicURL, settings.SetupComplete, nullStr(settings.UpdatedBy))
	if err != nil {
		return fmt.Errorf("upsert cluster settings: %w", err)
	}
	return nil
}

func (s *PGStore) GetProviderConfig(ctx context.Context, provider domain.ProviderKind) (*domain.ProviderConfig, error) {
	return scanProviderConfig(s.pool.QueryRow(ctx, `SELECT `+providerConfigCols+` FROM provider_configs WHERE provider=$1`, provider))
}

func (s *PGStore) ListProviderConfigs(ctx context.Context) ([]domain.ProviderConfig, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+providerConfigCols+` FROM provider_configs ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list provider configs: %w", err)
	}
	defer rows.Close()
	out := []domain.ProviderConfig{}
	for rows.Next() {
		cfg, err := scanProviderConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

func (s *PGStore) UpsertProviderConfig(ctx context.Context, cfg *domain.ProviderConfig) error {
	return s.UpsertProviderConfigAndInvalidate(ctx, cfg, false, "")
}

func (s *PGStore) UpsertProviderConfigAndInvalidate(ctx context.Context, cfg *domain.ProviderConfig, invalidate bool, reason string) error {
	if cfg == nil || !domain.ValidProviderKind(cfg.Provider) {
		return fmt.Errorf("upsert provider config: invalid provider")
	}
	// PostgreSQL arrays distinguish NULL from an empty array. Provider
	// capability discovery is optional during bootstrap, so callers commonly
	// supply a nil slice; persist that state as the schema's NOT NULL empty
	// array instead of failing the entire setup transaction.
	if cfg.Capabilities == nil {
		cfg.Capabilities = []string{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("upsert provider config: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var revision int64
	err = tx.QueryRow(ctx, `INSERT INTO provider_configs (`+providerConfigCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,1,$13,$14,$15,now()) ON CONFLICT(provider) DO UPDATE SET base_url=EXCLUDED.base_url,login_enabled=EXCLUDED.login_enabled,plugin_enabled=EXCLUDED.plugin_enabled,client_id=EXCLUDED.client_id,client_secret_enc=EXCLUDED.client_secret_enc,app_id=EXCLUDED.app_id,app_slug=EXCLUDED.app_slug,app_private_key_enc=EXCLUDED.app_private_key_enc,webhook_secret_enc=EXCLUDED.webhook_secret_enc,capability_version=EXCLUDED.capability_version,capabilities=EXCLUDED.capabilities,last_health_error=EXCLUDED.last_health_error,last_capability_check=EXCLUDED.last_capability_check,updated_by=EXCLUDED.updated_by,updated_at=now(),config_revision=provider_configs.config_revision+1 RETURNING config_revision`, cfg.Provider, cfg.BaseURL, cfg.LoginEnabled, cfg.PluginEnabled, cfg.ClientID, cfg.ClientSecretEnc, cfg.AppID, cfg.AppSlug, cfg.AppPrivateKeyEnc, cfg.WebhookSecretEnc, cfg.CapabilityVersion, cfg.Capabilities, cfg.LastHealthError, cfg.LastCapabilityCheck, nullStr(cfg.UpdatedBy)).Scan(&revision)
	if err != nil {
		return fmt.Errorf("upsert provider config: %w", err)
	}
	// Keep the issuer material immutable by revision. Snapshot rows reference
	// this history instead of copying client secrets or App keys per run.
	if _, err = tx.Exec(ctx, `INSERT INTO provider_config_versions(provider,config_revision,base_url,client_id,client_secret_enc,app_id,app_private_key_enc) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(provider,config_revision) DO NOTHING`, cfg.Provider, revision, cfg.BaseURL, cfg.ClientID, cfg.ClientSecretEnc, cfg.AppID, cfg.AppPrivateKeyEnc); err != nil {
		return fmt.Errorf("append provider config version: %w", err)
	}
	if invalidate {
		if _, err = tx.Exec(ctx, `UPDATE plugin_installations SET status='action_required',last_health_error=$2,updated_at=now() WHERE provider=$1`, cfg.Provider, reason); err != nil {
			return fmt.Errorf("invalidate provider installations: %w", err)
		}
	} else if _, err = tx.Exec(ctx, `UPDATE plugin_installations SET config_revision=$2,updated_at=now() WHERE provider=$1`, cfg.Provider, revision); err != nil {
		return fmt.Errorf("sync provider installation revisions: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("upsert provider config: commit: %w", err)
	}
	cfg.ConfigRevision = revision
	return nil
}

func (s *PGStore) RecordProviderCapabilities(ctx context.Context, provider domain.ProviderKind, version string, capabilities []string, checkedAt time.Time) error {
	if !domain.ValidProviderKind(provider) {
		return fmt.Errorf("record provider capabilities: invalid provider")
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	result, err := s.pool.Exec(ctx, `UPDATE provider_configs SET capability_version=$2,capabilities=$3,last_health_error='',last_capability_check=$4,updated_at=now() WHERE provider=$1`, provider, version, capabilities, checkedAt.UTC())
	if err != nil {
		return fmt.Errorf("record provider capabilities: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) RecordProviderAppSlug(ctx context.Context, provider domain.ProviderKind, appSlug string) error {
	if provider != domain.PluginGitHub {
		return fmt.Errorf("record provider App slug: provider must be github")
	}
	result, err := s.pool.Exec(ctx, `UPDATE provider_configs SET app_slug=$2,updated_at=now() WHERE provider=$1`, provider, strings.TrimSpace(appSlug))
	if err != nil {
		return fmt.Errorf("record provider App slug: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) RecordProviderHealthError(ctx context.Context, provider domain.ProviderKind, message string, checkedAt time.Time) error {
	if !domain.ValidProviderKind(provider) {
		return fmt.Errorf("record provider health error: invalid provider")
	}
	result, err := s.pool.Exec(ctx, `UPDATE provider_configs SET last_health_error=$2,last_capability_check=$3,updated_at=now() WHERE provider=$1`, provider, message, checkedAt.UTC())
	if err != nil {
		return fmt.Errorf("record provider health error: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) CountProviderConfigImpact(ctx context.Context, provider domain.ProviderKind) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_installations WHERE provider=$1`, provider).Scan(&count); err != nil {
		return 0, fmt.Errorf("count provider config impact: %w", err)
	}
	return count, nil
}

const pluginInstallationCols = `id,project_id,provider,status,external_account_id,external_account,github_installation_id,workspace_id,scopes,access_token_enc,refresh_token_enc,token_expires_at,credential_version_id,consent_version,consented_by,consented_at,config_revision,last_health_error,last_healthy_at,created_at,updated_at`

func scanPluginInstallation(row pgx.Row) (*domain.PluginInstallation, error) {
	var in domain.PluginInstallation
	var consentedBy *string
	err := row.Scan(&in.ID, &in.ProjectID, &in.Provider, &in.Status, &in.ExternalAccountID, &in.ExternalAccount, &in.GitHubInstallID, &in.WorkspaceID, &in.Scopes, &in.AccessTokenEnc, &in.RefreshTokenEnc, &in.TokenExpiresAt, &in.CredentialVersionID, &in.ConsentVersion, &consentedBy, &in.ConsentedAt, &in.ConfigRevision, &in.LastHealthError, &in.LastHealthyAt, &in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan plugin installation: %w", err)
	}
	if consentedBy != nil {
		in.ConsentedBy = *consentedBy
	}
	return &in, nil
}

func (s *PGStore) CreatePluginInstallation(ctx context.Context, in *domain.PluginInstallation) error {
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create plugin installation: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err = appendPluginCredentialVersion(ctx, tx, in); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO plugin_installations (`+pluginInstallationCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,'epoch'::timestamptz),$17,$18,$19,$20,now())`, in.ID, in.ProjectID, in.Provider, in.Status, in.ExternalAccountID, in.ExternalAccount, in.GitHubInstallID, in.WorkspaceID, in.Scopes, in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt, in.CredentialVersionID, in.ConsentVersion, nullStr(in.ConsentedBy), in.ConsentedAt, in.ConfigRevision, in.LastHealthError, in.LastHealthyAt, in.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create plugin installation: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("create plugin installation: commit: %w", err)
	}
	return nil
}

func (s *PGStore) GetPluginInstallation(ctx context.Context, id string) (*domain.PluginInstallation, error) {
	return scanPluginInstallation(s.pool.QueryRow(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations WHERE id=$1`, id))
}
func (s *PGStore) GetPluginInstallationForProject(ctx context.Context, projectID string, provider domain.ProviderKind) (*domain.PluginInstallation, error) {
	return scanPluginInstallation(s.pool.QueryRow(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations WHERE project_id=$1 AND provider=$2`, projectID, provider))
}
func (s *PGStore) ListPluginInstallationsByProject(ctx context.Context, projectID string) ([]domain.PluginInstallation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations WHERE project_id=$1 ORDER BY provider`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list plugin installations: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginInstallation{}
	for rows.Next() {
		in, err := scanPluginInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}
func (s *PGStore) UpdatePluginInstallation(ctx context.Context, in *domain.PluginInstallation) error {
	if in == nil || in.ID == "" {
		return fmt.Errorf("update plugin installation: invalid installation")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update plugin installation: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := scanPluginInstallation(tx.QueryRow(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations WHERE id=$1 FOR UPDATE`, in.ID))
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("update plugin installation: lock current: %w", err)
	}
	if pluginCredentialMaterialChanged(*current, *in) {
		if err = appendPluginCredentialVersion(ctx, tx, in); err != nil {
			return err
		}
	} else {
		// Status, health and consent changes do not mint another encrypted grant
		// row. Keep the live installation pinned to its existing immutable grant.
		in.CredentialVersionID = current.CredentialVersionID
	}
	tag, err := tx.Exec(ctx, `UPDATE plugin_installations SET status=$2,external_account_id=$3,external_account=$4,github_installation_id=$5,workspace_id=$6,scopes=$7,access_token_enc=$8,refresh_token_enc=$9,token_expires_at=$10,credential_version_id=$11,consent_version=$12,consented_by=$13,consented_at=$14,config_revision=$15,last_health_error=$16,last_healthy_at=$17,updated_at=now() WHERE id=$1`, in.ID, in.Status, in.ExternalAccountID, in.ExternalAccount, in.GitHubInstallID, in.WorkspaceID, in.Scopes, in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt, in.CredentialVersionID, in.ConsentVersion, nullStr(in.ConsentedBy), in.ConsentedAt, in.ConfigRevision, in.LastHealthError, in.LastHealthyAt)
	if err != nil {
		return fmt.Errorf("update plugin installation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("update plugin installation: commit: %w", err)
	}
	return nil
}

func pluginCredentialMaterialChanged(current, next domain.PluginInstallation) bool {
	return current.GitHubInstallID != next.GitHubInstallID ||
		!bytes.Equal(current.AccessTokenEnc, next.AccessTokenEnc) ||
		!bytes.Equal(current.RefreshTokenEnc, next.RefreshTokenEnc) ||
		!sameOptionalTime(current.TokenExpiresAt, next.TokenExpiresAt)
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// RotatePluginCredentialVersion keeps a durable run on its own launch-time
// grant after an OAuth refresh-token rotation. The guarded second UPDATE is
// important: once a reconnect creates a new version, an old run may rotate
// only its historical row and can never clobber the new Installation tokens.
func (s *PGStore) RotatePluginCredentialVersion(ctx context.Context, version *domain.PluginCredentialVersion) error {
	if version == nil || version.ID == "" || version.InstallationID == "" || !domain.ValidProviderKind(version.Provider) {
		return fmt.Errorf("rotate plugin credential version: invalid version")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rotate plugin credential version: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE plugin_credential_versions SET access_token_enc=$4,refresh_token_enc=$5,token_expires_at=$6 WHERE id=$1 AND installation_id=$2 AND provider=$3`, version.ID, version.InstallationID, version.Provider, version.AccessTokenEnc, version.RefreshTokenEnc, version.TokenExpiresAt)
	if err != nil {
		return fmt.Errorf("rotate plugin credential version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE plugin_installations SET access_token_enc=$3,refresh_token_enc=$4,token_expires_at=$5,last_health_error='',last_healthy_at=now(),updated_at=now() WHERE id=$1 AND credential_version_id=$2`, version.InstallationID, version.ID, version.AccessTokenEnc, version.RefreshTokenEnc, version.TokenExpiresAt); err != nil {
		return fmt.Errorf("sync rotated plugin credential version: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("rotate plugin credential version: commit: %w", err)
	}
	return nil
}

func appendPluginCredentialVersion(ctx context.Context, tx pgx.Tx, in *domain.PluginInstallation) error {
	if in == nil || in.ID == "" || !domain.ValidProviderKind(in.Provider) {
		return fmt.Errorf("append plugin credential version: invalid installation")
	}
	in.CredentialVersionID = domain.NewID()
	_, err := tx.Exec(ctx, `INSERT INTO plugin_credential_versions(id,installation_id,provider,github_installation_id,access_token_enc,refresh_token_enc,token_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.CredentialVersionID, in.ID, in.Provider, in.GitHubInstallID, in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt)
	if err != nil {
		return fmt.Errorf("append plugin credential version: %w", err)
	}
	return nil
}
func (s *PGStore) CountPluginInstallationImpact(ctx context.Context, installationID string) (services, automations int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM service_repository_bindings WHERE installation_id=$1`, installationID).Scan(&services)
	if err != nil {
		return 0, 0, fmt.Errorf("count plugin services: %w", err)
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM automations_v2 a
		LEFT JOIN service_repository_bindings b ON b.service_id=a.service_id
		WHERE a.installation_id=$1 OR b.installation_id=$1`, installationID).Scan(&automations)
	if err != nil {
		return 0, 0, fmt.Errorf("count plugin automations: %w", err)
	}
	return services, automations, nil
}
func (s *PGStore) UninstallPlugin(ctx context.Context, installationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin plugin uninstall: %w", err)
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_installations WHERE id=$1 FOR UPDATE)`, installationID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT service_id FROM service_repository_bindings WHERE installation_id=$1`, installationID)
	if err != nil {
		return err
	}
	var serviceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		serviceIDs = append(serviceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(serviceIDs) > 0 {
		// A dispatch claim holds the installation row lock until its run is
		// durably moved to scheduling.  Once committed, never delete that run
		// from under the reconciler before it has created its Job: let uninstall
		// retry after the in-flight run reaches a terminal state instead.
		var live bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM runs
			WHERE service_id=ANY($1)
			  AND status IN ('scheduling','running','awaiting_input')
		)`, serviceIDs).Scan(&live); err != nil {
			return fmt.Errorf("check in-flight plugin runs: %w", err)
		}
		if live {
			return ErrConflict
		}
		if _, err = tx.Exec(ctx, `DELETE FROM runs WHERE service_id=ANY($1)`, serviceIDs); err != nil {
			return fmt.Errorf("delete plugin runs: %w", err)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM services WHERE id=ANY($1)`, serviceIDs); err != nil {
			return fmt.Errorf("delete plugin services: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM automations_v2 WHERE installation_id=$1`, installationID); err != nil {
		return fmt.Errorf("delete plugin automations: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id=$1`, installationID); err != nil {
		return fmt.Errorf("delete plugin installation: %w", err)
	}
	// The Installation's live credential reference has now been removed. Reclaim
	// only history that is not pinned by another Installation or a non-terminal
	// run snapshot; audit events and terminal snapshot identifiers are retained.
	if _, _, err = deleteUnreferencedPluginSecretVersionsTx(ctx, tx, pluginSecretVersionDeleteBatchSize); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit plugin uninstall: %w", err)
	}
	return nil
}

const repositoryBindingCols = `service_id,installation_id,provider_repo_id,repository_path,clone_url,default_branch,created_at,updated_at`

func scanRepositoryBinding(row pgx.Row) (*domain.ServiceRepositoryBinding, error) {
	var b domain.ServiceRepositoryBinding
	err := row.Scan(&b.ServiceID, &b.InstallationID, &b.ProviderRepoID, &b.RepositoryPath, &b.CloneURL, &b.DefaultBranch, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan repository binding: %w", err)
	}
	return &b, nil
}
func (s *PGStore) GetServiceRepositoryBinding(ctx context.Context, serviceID string) (*domain.ServiceRepositoryBinding, error) {
	return scanRepositoryBinding(s.pool.QueryRow(ctx, `SELECT `+repositoryBindingCols+` FROM service_repository_bindings WHERE service_id=$1`, serviceID))
}
func (s *PGStore) UpsertServiceRepositoryBinding(ctx context.Context, b *domain.ServiceRepositoryBinding) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO service_repository_bindings(`+repositoryBindingCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,now()) ON CONFLICT(service_id) DO UPDATE SET installation_id=EXCLUDED.installation_id,provider_repo_id=EXCLUDED.provider_repo_id,repository_path=EXCLUDED.repository_path,clone_url=EXCLUDED.clone_url,default_branch=EXCLUDED.default_branch,updated_at=now()`, b.ServiceID, b.InstallationID, b.ProviderRepoID, b.RepositoryPath, b.CloneURL, b.DefaultBranch, b.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("upsert repository binding: %w", err)
	}
	return nil
}
func (s *PGStore) DeleteServiceRepositoryBinding(ctx context.Context, serviceID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM service_repository_bindings WHERE service_id=$1`, serviceID)
	if err != nil {
		return fmt.Errorf("delete repository binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const pluginAutomationCols = `id,service_id,installation_id,name,trigger_kind,run_kind,prompt_template,model_id,model_effort,enabled,ignore_jcode,last_triggered_at,last_run_id,last_error,created_by,created_at,updated_at`

func qualifiedPluginAutomationCols(alias string) string {
	columns := strings.Split(pluginAutomationCols, ",")
	for i := range columns {
		columns[i] = alias + "." + strings.TrimSpace(columns[i])
	}
	return strings.Join(columns, ",")
}

func scanPluginAutomation(row pgx.Row) (*domain.PluginAutomation, error) {
	var a domain.PluginAutomation
	var createdBy *string
	var installationID *string
	var modelID *string
	var lastRunID *string
	err := row.Scan(&a.ID, &a.ServiceID, &installationID, &a.Name, &a.TriggerKind, &a.RunKind, &a.PromptTemplate, &modelID, &a.ModelEffort, &a.Enabled, &a.IgnoreJCode, &a.LastTriggeredAt, &lastRunID, &a.LastError, &createdBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan plugin automation: %w", err)
	}
	if createdBy != nil {
		a.CreatedBy = *createdBy
	}
	if installationID != nil {
		a.InstallationID = *installationID
	}
	if modelID != nil {
		a.ModelID = *modelID
	}
	if lastRunID != nil {
		a.LastRunID = *lastRunID
	}
	return &a, nil
}
func (s *PGStore) CreatePluginAutomation(ctx context.Context, a *domain.PluginAutomation, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger, cron *domain.CronTrigger) error {
	if err := validatePluginAutomationAggregate(a, scm, actions, kanban, cron); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO automations_v2(id,service_id,installation_id,name,trigger_kind,run_kind,prompt_template,model_id,model_effort,enabled,ignore_jcode,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now())`, a.ID, a.ServiceID, nullStr(a.InstallationID), a.Name, a.TriggerKind, a.RunKind, a.PromptTemplate, nullStr(a.ModelID), a.ModelEffort, a.Enabled, a.IgnoreJCode, nullStr(a.CreatedBy), a.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create plugin automation: %w", err)
	}
	if scm != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_scm_triggers(automation_id,branch,path_pattern,conclusion,include_drafts)VALUES($1,$2,$3,$4,$5)`, a.ID, scm.Branch, scm.PathPattern, scm.Conclusion, scm.IncludeDrafts); err != nil {
			return err
		}
		for _, action := range actions {
			if _, err = tx.Exec(ctx, `INSERT INTO automation_scm_actions(automation_id,service_id,event_family,action)VALUES($1,$2,$3,$4)`, a.ID, action.ServiceID, action.EventFamily, action.Action); err != nil {
				if isUniqueViolation(err) {
					return ErrAlreadyExists
				}
				return err
			}
		}
	}
	if kanban != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_kanban_triggers(automation_id,installation_id,board_ref,trigger_column,work_column,done_column,trigger_label,work_label,done_label)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, a.ID, kanban.InstallationID, kanban.BoardRef, kanban.TriggerColumn, kanban.WorkColumn, kanban.DoneColumn, kanban.TriggerLabel, kanban.WorkLabel, kanban.DoneLabel); err != nil {
			if isUniqueViolation(err) {
				return ErrAlreadyExists
			}
			return err
		}
	}
	if cron != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_cron_triggers(automation_id,cron_expr,output_mode)VALUES($1,$2,$3)`, a.ID, cron.CronExpr, cron.OutputMode); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *PGStore) GetPluginAutomation(ctx context.Context, id string) (*domain.PluginAutomation, error) {
	return scanPluginAutomation(s.pool.QueryRow(ctx, `SELECT `+pluginAutomationCols+` FROM automations_v2 WHERE id=$1`, id))
}
func (s *PGStore) GetPluginAutomationSpec(ctx context.Context, id string) (*domain.PluginAutomationSpec, error) {
	a, err := s.GetPluginAutomation(ctx, id)
	if err != nil {
		return nil, err
	}
	spec := &domain.PluginAutomationSpec{Automation: *a}
	switch a.TriggerKind {
	case "scm":
		var scm domain.SCMTrigger
		if err := s.pool.QueryRow(ctx, `SELECT automation_id,branch,path_pattern,conclusion,include_drafts FROM automation_scm_triggers WHERE automation_id=$1`, id).Scan(&scm.AutomationID, &scm.Branch, &scm.PathPattern, &scm.Conclusion, &scm.IncludeDrafts); err != nil {
			return nil, fmt.Errorf("get scm trigger: %w", err)
		}
		spec.SCM = &scm
		rows, err := s.pool.Query(ctx, `SELECT automation_id,service_id,event_family,action FROM automation_scm_actions WHERE automation_id=$1 ORDER BY event_family,action`, id)
		if err != nil {
			return nil, fmt.Errorf("get scm actions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var v domain.SCMAction
			if err := rows.Scan(&v.AutomationID, &v.ServiceID, &v.EventFamily, &v.Action); err != nil {
				return nil, err
			}
			spec.Actions = append(spec.Actions, v)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	case "kanban":
		var v domain.KanbanTrigger
		if err := s.pool.QueryRow(ctx, `SELECT automation_id,installation_id,board_ref,trigger_column,work_column,done_column,trigger_label,work_label,done_label,event_cursor,bootstrapped_at FROM automation_kanban_triggers WHERE automation_id=$1`, id).Scan(&v.AutomationID, &v.InstallationID, &v.BoardRef, &v.TriggerColumn, &v.WorkColumn, &v.DoneColumn, &v.TriggerLabel, &v.WorkLabel, &v.DoneLabel, &v.EventCursor, &v.BootstrappedAt); err != nil {
			return nil, fmt.Errorf("get kanban trigger: %w", err)
		}
		spec.Kanban = &v
	case "cron":
		var v domain.CronTrigger
		if err := s.pool.QueryRow(ctx, `SELECT automation_id,cron_expr,output_mode,last_fired_at,last_error FROM automation_cron_triggers WHERE automation_id=$1`, id).Scan(&v.AutomationID, &v.CronExpr, &v.OutputMode, &v.LastFiredAt, &v.LastError); err != nil {
			return nil, fmt.Errorf("get cron trigger: %w", err)
		}
		spec.Cron = &v
	}
	return spec, nil
}

func (s *PGStore) ListEnabledCronAutomations(ctx context.Context) ([]domain.PluginAutomationSpec, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM automations_v2 WHERE enabled=TRUE AND trigger_kind='cron' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list enabled cron Automations: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.PluginAutomationSpec, 0, len(ids))
	for _, id := range ids {
		spec, err := s.GetPluginAutomationSpec(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *spec)
	}
	return out, nil
}

func (s *PGStore) AdvancePluginCronAutomation(ctx context.Context, id string, previous, firedAt *time.Time, lastError string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_cron_triggers
		SET last_fired_at=$3,last_error=$4
		WHERE automation_id=$1 AND last_fired_at IS NOT DISTINCT FROM $2`,
		id, previous, firedAt, lastError)
	if err != nil {
		return false, fmt.Errorf("advance cron Automation: %w", err)
	}
	if tag.RowsAffected() == 1 {
		_, _ = s.pool.Exec(ctx, `UPDATE automations_v2 SET last_error=$2,updated_at=now() WHERE id=$1`, id, lastError)
		return true, nil
	}
	return false, nil
}

func (s *PGStore) ListEnabledKanbanAutomations(ctx context.Context) ([]domain.PluginAutomationSpec, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM automations_v2 WHERE enabled=TRUE AND trigger_kind='kanban' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list enabled Kanban Automations: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := make([]domain.PluginAutomationSpec, 0, len(ids))
	for _, id := range ids {
		spec, err := s.GetPluginAutomationSpec(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *spec)
	}
	return out, rows.Err()
}

func (s *PGStore) EnsurePluginKanbanClaim(ctx context.Context, automationID, documentID, documentPath, workspaceID, doneColumn string) (*domain.PluginKanbanClaim, error) {
	var installationID string
	if err := s.pool.QueryRow(ctx, `SELECT installation_id FROM automation_kanban_triggers WHERE automation_id=$1`, automationID).Scan(&installationID); err != nil {
		return nil, fmt.Errorf("load Kanban Automation installation: %w", err)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO automation_kanban_claims(automation_id,installation_id,document_id,document_path,workspace_id,done_column) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, automationID, installationID, documentID, documentPath, workspaceID, doneColumn)
	if err != nil {
		return nil, fmt.Errorf("ensure Kanban Automation claim: %w", err)
	}
	claim, err := scanPluginKanbanClaim(s.pool.QueryRow(ctx,
		`SELECT `+pluginKanbanClaimCols+` FROM automation_kanban_claims WHERE automation_id=$1 AND document_id=$2`,
		automationID, documentID))
	if err != nil {
		return nil, fmt.Errorf("load Kanban Automation claim: %w", err)
	}
	return claim, nil
}

func (s *PGStore) SetPluginKanbanClaimRun(ctx context.Context, automationID, documentID, runID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set Kanban Automation claim run: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	claim, err := scanPluginKanbanClaim(tx.QueryRow(ctx,
		`SELECT `+pluginKanbanClaimCols+` FROM automation_kanban_claims WHERE automation_id=$1 AND document_id=$2 FOR UPDATE`,
		automationID, documentID))
	if err != nil {
		return err
	}
	if claim.RunID != "" {
		return ErrAlreadyExists
	}
	occurrenceID := claim.LatestOccurrenceID
	if occurrenceID == "" {
		var serviceID, triggerColumn string
		if err = tx.QueryRow(ctx, `SELECT a.service_id,t.trigger_column
			FROM automations_v2 a JOIN automation_kanban_triggers t ON t.automation_id=a.id
			WHERE a.id=$1`, automationID).Scan(&serviceID, &triggerColumn); err != nil {
			return fmt.Errorf("set Kanban Automation claim run: load trigger: %w", err)
		}
		occurrenceID = domain.NewID()
		now := time.Now().UTC()
		if _, err = tx.Exec(ctx, `INSERT INTO automation_kanban_occurrences(
			id,automation_id,service_id,installation_id,workspace_id,document_id,document_path,
			done_column,event_key,entry_column,state,run_id,writeback_state,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued',$11,'pending',$12,$12)`,
			occurrenceID, automationID, serviceID, claim.InstallationID, claim.WorkspaceID,
			documentID, claim.DocumentPath, claim.DoneColumn, "legacy:"+documentID+":"+runID,
			triggerColumn, runID, now); err != nil {
			return fmt.Errorf("set Kanban Automation claim run: create occurrence: %w", err)
		}
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE automation_kanban_occurrences
			SET run_id=$2,state='queued',updated_at=now() WHERE id=$1 AND run_id IS NULL`,
			occurrenceID, runID)
		if updateErr != nil {
			return fmt.Errorf("set Kanban Automation claim run: update occurrence: %w", updateErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrAlreadyExists
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE automation_kanban_claims
		SET run_id=$3,latest_occurrence_id=$4,updated_at=now()
		WHERE automation_id=$1 AND document_id=$2 AND run_id IS NULL`,
		automationID, documentID, runID, occurrenceID)
	if err != nil {
		return fmt.Errorf("set Kanban Automation claim run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyExists
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("set Kanban Automation claim run: commit: %w", err)
	}
	return nil
}

type PluginKanbanWriteback struct {
	Claim      domain.PluginKanbanClaim
	Occurrence *domain.PluginKanbanOccurrence
	Run        domain.Run
}

func (s *PGStore) ListPluginKanbanRunsAwaitingWriteback(ctx context.Context) ([]PluginKanbanWriteback, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+qualifiedPluginKanbanOccurrenceCols+`
		FROM automation_kanban_occurrences o
		JOIN automation_kanban_claims c
		  ON c.automation_id=o.automation_id AND c.document_id=o.document_id
		 AND c.latest_occurrence_id=o.id AND c.writeback_at IS NULL
		JOIN runs r ON r.id=o.run_id
		WHERE o.writeback_state='pending' AND r.status IN ('succeeded','failed','canceled')
		ORDER BY r.finished_at NULLS LAST,r.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list Kanban Automation writebacks: %w", err)
	}
	defer rows.Close()
	var occurrences []domain.PluginKanbanOccurrence
	for rows.Next() {
		occurrence, err := scanPluginKanbanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		occurrences = append(occurrences, *occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	out := make([]PluginKanbanWriteback, 0, len(occurrences))
	for i := range occurrences {
		occurrence := occurrences[i]
		run, err := s.GetRun(ctx, occurrence.RunID)
		if err != nil {
			return nil, fmt.Errorf("load Kanban Automation writeback run: %w", err)
		}
		claim, err := scanPluginKanbanClaim(s.pool.QueryRow(ctx,
			`SELECT `+pluginKanbanClaimCols+` FROM automation_kanban_claims
			 WHERE automation_id=$1 AND document_id=$2 AND latest_occurrence_id=$3`,
			occurrence.AutomationID, occurrence.DocumentID, occurrence.ID))
		if err != nil {
			return nil, fmt.Errorf("load Kanban Automation writeback claim: %w", err)
		}
		out = append(out, PluginKanbanWriteback{Claim: *claim, Occurrence: &occurrence, Run: *run})
	}
	return out, nil
}

func (s *PGStore) MarkPluginKanbanWriteback(
	ctx context.Context,
	automationID, documentID, occurrenceID string,
	outcome domain.RunStatus,
	terminalAt *time.Time,
	at time.Time,
) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if occurrenceID != "" {
		finishedAt := at
		if terminalAt != nil {
			finishedAt = *terminalAt
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE automation_kanban_occurrences o SET
			state='terminal',outcome=$4,receipt_phase='terminal',
			receipt_written_at=$6,writeback_state='complete',writeback_error='',
			terminal_at=$5,updated_at=$6
			FROM automation_kanban_claims c
			WHERE c.automation_id=$1 AND c.document_id=$2
			  AND c.latest_occurrence_id=$3 AND c.writeback_at IS NULL
			  AND o.id=$3 AND o.writeback_state='pending'`,
			automationID, documentID, occurrenceID, outcome, finishedAt, at)
		if updateErr != nil {
			return false, fmt.Errorf("mark Kanban Automation occurrence writeback: %w", updateErr)
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE automation_kanban_claims
		SET writeback_at=$4,updated_at=$4
		WHERE automation_id=$1 AND document_id=$2
		  AND COALESCE(latest_occurrence_id,'')=$3
		  AND writeback_at IS NULL`,
		automationID, documentID, occurrenceID, at)
	if err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, fmt.Errorf("mark Kanban Automation writeback: claim changed during occurrence update")
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback: commit: %w", err)
	}
	return true, nil
}

func (s *PGStore) MarkPluginKanbanWritebackUnavailable(
	ctx context.Context,
	automationID, documentID, occurrenceID string,
	outcome domain.RunStatus,
	terminalAt *time.Time,
	message string,
	at time.Time,
) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback unavailable: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if occurrenceID != "" {
		finishedAt := at
		if terminalAt != nil {
			finishedAt = *terminalAt
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE automation_kanban_occurrences o SET
			state='terminal',outcome=$4,receipt_phase='terminal',
			writeback_state='unavailable',writeback_error=$6,
			terminal_at=$5,updated_at=$7
			FROM automation_kanban_claims c
			WHERE c.automation_id=$1 AND c.document_id=$2
			  AND c.latest_occurrence_id=$3 AND c.writeback_at IS NULL
			  AND o.id=$3 AND o.writeback_state='pending'`,
			automationID, documentID, occurrenceID, outcome, finishedAt, message, at)
		if updateErr != nil {
			return false, fmt.Errorf("mark Kanban Automation occurrence writeback unavailable: %w", updateErr)
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE automation_kanban_claims
		SET writeback_at=$4,external_ref_available=FALSE,last_observed_column='',
		    outside_trigger_at=$4,updated_at=$4
		WHERE automation_id=$1 AND document_id=$2
		  AND COALESCE(latest_occurrence_id,'')=$3
		  AND writeback_at IS NULL`,
		automationID, documentID, occurrenceID, at)
	if err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback unavailable: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, fmt.Errorf("mark Kanban Automation writeback unavailable: claim changed during occurrence update")
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("mark Kanban Automation writeback unavailable: commit: %w", err)
	}
	return true, nil
}
func (s *PGStore) ListPluginAutomationsByProject(ctx context.Context, projectID string) ([]domain.PluginAutomation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+qualifiedPluginAutomationCols("a")+` FROM automations_v2 a JOIN services s ON s.id=a.service_id WHERE s.project_id=$1 ORDER BY a.created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list plugin automations: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginAutomation{}
	for rows.Next() {
		a, err := scanPluginAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListPluginAutomationsForEvent intentionally matches stable provider repository
// ids, not a mutable URL/path supplied by a webhook.
func (s *PGStore) ListPluginAutomationsForEvent(ctx context.Context, provider domain.ProviderKind, repositoryID string, family, action string) ([]domain.PluginAutomation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+qualifiedPluginAutomationCols("a")+` FROM automations_v2 a JOIN automation_scm_actions x ON x.automation_id=a.id JOIN service_repository_bindings b ON b.service_id=a.service_id JOIN plugin_installations i ON i.id=b.installation_id WHERE i.provider=$1 AND i.status='enabled' AND b.provider_repo_id=$2 AND a.enabled=TRUE AND a.trigger_kind='scm' AND x.event_family=$3 AND x.action=$4 ORDER BY a.created_at`, provider, repositoryID, family, action)
	if err != nil {
		return nil, fmt.Errorf("list plugin automations for event: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginAutomation{}
	for rows.Next() {
		a, err := scanPluginAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}
func (s *PGStore) ListPluginReviewAutomationsForRepository(ctx context.Context, provider domain.ProviderKind, repositoryID string) ([]domain.PluginAutomation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+qualifiedPluginAutomationCols("a")+` FROM automations_v2 a JOIN service_repository_bindings b ON b.service_id=a.service_id JOIN plugin_installations i ON i.id=b.installation_id WHERE i.provider=$1 AND i.status='enabled' AND b.provider_repo_id=$2 AND a.enabled=TRUE AND a.trigger_kind='scm' AND a.run_kind='review' ORDER BY a.created_at`, provider, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("list review automations for repository: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginAutomation{}
	for rows.Next() {
		a, err := scanPluginAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}
func (s *PGStore) UpdatePluginAutomation(ctx context.Context, a *domain.PluginAutomation) error {
	tag, err := s.pool.Exec(ctx, `UPDATE automations_v2 SET name=$2,run_kind=$3,prompt_template=$4,model_id=$5,model_effort=$6,enabled=$7,ignore_jcode=$8,last_triggered_at=$9,last_run_id=$10,last_error=$11,updated_at=now() WHERE id=$1`, a.ID, a.Name, a.RunKind, a.PromptTemplate, nullStr(a.ModelID), a.ModelEffort, a.Enabled, a.IgnoreJCode, a.LastTriggeredAt, a.LastRunID, a.LastError)
	if err != nil {
		return fmt.Errorf("update plugin automation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *PGStore) ReplacePluginAutomationSpec(ctx context.Context, a *domain.PluginAutomation, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger, cron *domain.CronTrigger) error {
	if err := validatePluginAutomationAggregate(a, scm, actions, kanban, cron); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Child action rows are intentionally deleted before inserting replacements:
	// the service/family/action uniqueness constraint otherwise sees the prior
	// version of this same Automation as an overlap.
	if _, err = tx.Exec(ctx, `DELETE FROM automation_scm_triggers WHERE automation_id=$1`, a.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM automation_kanban_triggers WHERE automation_id=$1`, a.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM automation_cron_triggers WHERE automation_id=$1`, a.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM automation_scm_actions WHERE automation_id=$1`, a.ID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE automations_v2 SET installation_id=$2,name=$3,trigger_kind=$4,run_kind=$5,prompt_template=$6,model_id=$7,model_effort=$8,enabled=$9,ignore_jcode=$10,last_error=$11,updated_at=now() WHERE id=$1`, a.ID, nullStr(a.InstallationID), a.Name, a.TriggerKind, a.RunKind, a.PromptTemplate, nullStr(a.ModelID), a.ModelEffort, a.Enabled, a.IgnoreJCode, a.LastError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if scm != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_scm_triggers(automation_id,branch,path_pattern,conclusion,include_drafts)VALUES($1,$2,$3,$4,$5)`, a.ID, scm.Branch, scm.PathPattern, scm.Conclusion, scm.IncludeDrafts); err != nil {
			return err
		}
		for _, x := range actions {
			if _, err = tx.Exec(ctx, `INSERT INTO automation_scm_actions(automation_id,service_id,event_family,action)VALUES($1,$2,$3,$4)`, a.ID, x.ServiceID, x.EventFamily, x.Action); err != nil {
				if isUniqueViolation(err) {
					return ErrAlreadyExists
				}
				return err
			}
		}
	}
	if kanban != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_kanban_triggers(automation_id,installation_id,board_ref,trigger_column,work_column,done_column,trigger_label,work_label,done_label)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, a.ID, kanban.InstallationID, kanban.BoardRef, kanban.TriggerColumn, kanban.WorkColumn, kanban.DoneColumn, kanban.TriggerLabel, kanban.WorkLabel, kanban.DoneLabel); err != nil {
			if isUniqueViolation(err) {
				return ErrAlreadyExists
			}
			return err
		}
	}
	if cron != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO automation_cron_triggers(automation_id,cron_expr,output_mode)VALUES($1,$2,$3)`, a.ID, cron.CronExpr, cron.OutputMode); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *PGStore) DeletePluginAutomation(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete plugin automation: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM automation_kanban_claims c
		WHERE c.automation_id=$1 AND c.run_id IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM automation_kanban_occurrences o
			WHERE o.automation_id=c.automation_id AND o.document_id=c.document_id
		  )`, id); err != nil {
		return fmt.Errorf("delete plugin automation observations: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM automations_v2 WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete plugin automation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *PGStore) ClaimWebhookReceipt(ctx context.Context, r *domain.WebhookReceipt) (bool, error) {
	claimedAt := r.ReceivedAt.UTC()
	if r.ClaimedAt != nil {
		claimedAt = r.ClaimedAt.UTC()
	}
	err := s.pool.QueryRow(ctx, `UPDATE webhook_receipts SET
		delivery_id=$2,payload_digest=$3,installation_id=$4,event_family=$5,action=$6,
		external_actor_id=$7,external_actor=$8,object_ref=$9,status=$10,
		matched_automation_id=$11,error=$12,received_at=$13::timestamptz,
		expires_at=$13::timestamptz + interval '30 days',claim_token=$14,claimed_at=$15,
		ingress_sequence=nextval('webhook_receipt_ingress_sequence_seq')
		WHERE provider=$1 AND (status='error' OR (
			status='received' AND COALESCE(claimed_at,received_at) <= $16
		))
		  AND (delivery_id=$2 OR ($3<>'' AND payload_digest=$3))
		RETURNING ingress_sequence`,
		r.Provider, r.DeliveryID, r.PayloadDigest, nullStr(r.InstallationID),
		r.EventFamily, r.Action, r.ExternalActorID, r.ExternalActor, r.ObjectRef,
		r.Status, nullStr(r.MatchedAutomationID), r.Error, r.ReceivedAt,
		r.ClaimToken, claimedAt, r.ReclaimBefore.UTC()).Scan(&r.IngressSequence)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("reclaim webhook receipt: %w", err)
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO webhook_receipts(
		id,provider,delivery_id,payload_digest,installation_id,event_family,action,
		external_actor_id,external_actor,object_ref,status,matched_automation_id,error,
		received_at,claim_token,claimed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT DO NOTHING RETURNING ingress_sequence`,
		r.ID, r.Provider, r.DeliveryID, r.PayloadDigest, nullStr(r.InstallationID),
		r.EventFamily, r.Action, r.ExternalActorID, r.ExternalActor, r.ObjectRef,
		r.Status, nullStr(r.MatchedAutomationID), r.Error, r.ReceivedAt,
		r.ClaimToken, claimedAt).Scan(&r.IngressSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim webhook receipt: %w", err)
	}
	return true, nil
}
func (s *PGStore) CompleteWebhookReceipt(ctx context.Context, r *domain.WebhookReceipt) error {
	tag, err := s.pool.Exec(ctx, `UPDATE webhook_receipts SET status=$3,matched_automation_id=$4,error=$5 WHERE provider=$1 AND (delivery_id=$2 OR ($7<>'' AND payload_digest=$7)) AND claim_token=$6`, r.Provider, r.DeliveryID, r.Status, nullStr(r.MatchedAutomationID), r.Error, r.ClaimToken, r.PayloadDigest)
	if err != nil {
		return fmt.Errorf("complete webhook receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

const webhookReceiptDeleteBatchSize = 1000
const pluginSecretVersionDeleteBatchSize = 1000

func (s *PGStore) DeleteExpiredWebhookReceipts(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH expired AS (
			SELECT id
			FROM webhook_receipts
			WHERE expires_at <= $1
			ORDER BY expires_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM webhook_receipts r
		USING expired e
		WHERE r.id = e.id`, before.UTC(), webhookReceiptDeleteBatchSize)
	if err != nil {
		return 0, fmt.Errorf("delete expired webhook receipts: %w", err)
	}
	return tag.RowsAffected(), nil
}
func (s *PGStore) CreateRunPluginSnapshots(ctx context.Context, snapshots []domain.RunPluginSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// A failed Job creation may leave snapshots on a still-queued run. Reconcile
	// those candidates against current Plugin state before retrying launch; only
	// already-scheduled/running tasks retain immutable snapshots after disable.
	runIDs := map[string]struct{}{}
	for _, snap := range snapshots {
		runIDs[snap.RunID] = struct{}{}
	}
	for runID := range runIDs {
		if _, err = tx.Exec(ctx, `
			DELETE FROM run_plugin_snapshots s
			USING runs r
			WHERE s.run_id=$1 AND r.id=s.run_id
			  AND r.status='queued'
			  AND NOT EXISTS (
			    SELECT 1
			    FROM plugin_installations pi
			    JOIN provider_configs pc ON pc.provider=pi.provider
			    WHERE pi.id=s.installation_id AND pi.project_id=r.project_id
			      AND pi.status='enabled' AND pi.last_health_error=''
			      AND pc.plugin_enabled=TRUE AND pc.config_revision=pi.config_revision
			      AND pi.credential_version_id<>''
			      AND ((pi.provider='github' AND pi.github_installation_id<>'')
			        OR (pi.provider<>'github' AND pi.access_token_enc IS NOT NULL))
			  )`, runID); err != nil {
			return fmt.Errorf("revalidate run plugin snapshots: %w", err)
		}
	}
	for _, snap := range snapshots {
		// Re-check eligibility and project ownership in the same statement that
		// creates the immutable snapshot. This closes the gap between the
		// reconciler's list and insert when an owner disables/revokes a Plugin.
		if _, err = tx.Exec(ctx, `
			INSERT INTO run_plugin_snapshots(run_id,installation_id,provider,provider_config_revision,credential_version_id,created_at,
				repository_id,repository_path,clone_url,default_branch,acting_principal_kind,acting_principal_id)
			SELECT r.id,pi.id,pi.provider,pi.config_revision,pi.credential_version_id,$3,
				COALESCE(rb.provider_repo_id,''),COALESCE(rb.repository_path,''),COALESCE(rb.clone_url,''),COALESCE(rb.default_branch,''),
				CASE WHEN rb.installation_id IS NULL THEN '' ELSE 'provider_bot' END,
				CASE WHEN rb.installation_id IS NULL THEN '' ELSE pi.id END
			FROM runs r
			JOIN plugin_installations pi ON pi.project_id=r.project_id
			LEFT JOIN service_repository_bindings rb ON rb.service_id=r.service_id AND rb.installation_id=pi.id
			JOIN provider_configs pc ON pc.provider=pi.provider AND pc.config_revision=pi.config_revision
			JOIN provider_config_versions pv ON pv.provider=pi.provider AND pv.config_revision=pi.config_revision
			JOIN plugin_credential_versions cv ON cv.id=pi.credential_version_id AND cv.installation_id=pi.id AND cv.provider=pi.provider
			WHERE r.id=$1 AND pi.id=$2
			  AND pi.status='enabled' AND pi.last_health_error=''
			  AND pc.plugin_enabled=TRUE
			  AND (
			    (pi.provider='github' AND pi.github_installation_id<>'')
			    OR (pi.provider<>'github' AND pi.access_token_enc IS NOT NULL)
			  )
			ON CONFLICT DO NOTHING`, snap.RunID, snap.InstallationID, snap.CreatedAt); err != nil {
			return fmt.Errorf("create run plugin snapshot: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (s *PGStore) ClearQueuedRunPluginSnapshots(ctx context.Context, runID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM run_plugin_snapshots s
		USING runs r
		WHERE s.run_id=$1 AND r.id=s.run_id AND r.status='queued'`, runID)
	if err != nil {
		return fmt.Errorf("clear queued run plugin snapshots: %w", err)
	}
	_ = tag // an empty provisional set is successful and idempotent.
	return nil
}

func (s *PGStore) ListRunPluginSnapshots(ctx context.Context, runID string) ([]domain.RunPluginSnapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT
		s.run_id,s.installation_id,s.provider,s.provider_config_revision,s.credential_version_id,
		s.repository_id,s.repository_path,s.clone_url,s.default_branch,s.acting_principal_kind,s.acting_principal_id,
		COALESCE(pv.base_url,''),COALESCE(pv.client_id,''),pv.client_secret_enc,
		COALESCE(pv.app_id,''),pv.app_private_key_enc,
		COALESCE(cv.github_installation_id,''),cv.access_token_enc,cv.refresh_token_enc,cv.token_expires_at,
		s.created_at
		FROM run_plugin_snapshots s
		LEFT JOIN provider_config_versions pv
		  ON pv.provider=s.provider AND pv.config_revision=s.provider_config_revision
		LEFT JOIN plugin_credential_versions cv
		  ON cv.id=s.credential_version_id AND cv.installation_id=s.installation_id AND cv.provider=s.provider
		WHERE s.run_id=$1 ORDER BY s.installation_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run plugin snapshots: %w", err)
	}
	defer rows.Close()
	out := []domain.RunPluginSnapshot{}
	for rows.Next() {
		var snap domain.RunPluginSnapshot
		if err := rows.Scan(
			&snap.RunID, &snap.InstallationID, &snap.Provider, &snap.ProviderConfigRevision, &snap.CredentialVersionID,
			&snap.RepositoryID, &snap.RepositoryPath, &snap.CloneURL, &snap.DefaultBranch, &snap.ActingPrincipalKind, &snap.ActingPrincipalID,
			&snap.ProviderBaseURL, &snap.ProviderClientID, &snap.ProviderClientSecretEnc,
			&snap.ProviderAppID, &snap.ProviderAppPrivateKeyEnc,
			&snap.GitHubInstallID, &snap.AccessTokenEnc, &snap.RefreshTokenEnc, &snap.TokenExpiresAt,
			&snap.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// DeleteUnreferencedPluginSecretVersions retains exactly the encrypted history
// needed to launch/continue a Run and to finish native review, Kanban, or review
// status Provider projections. Converged terminal snapshots retain immutable
// audit identifiers after their historical ciphertext is gone.
func (s *PGStore) DeleteUnreferencedPluginSecretVersions(ctx context.Context, limit int) (credentialVersions, providerVersions int64, err error) {
	if limit <= 0 {
		return 0, 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("delete unreferenced plugin secret versions: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	credentialVersions, providerVersions, err = deleteUnreferencedPluginSecretVersionsTx(ctx, tx, limit)
	if err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("delete unreferenced plugin secret versions: commit: %w", err)
	}
	return credentialVersions, providerVersions, nil
}

func deleteUnreferencedPluginSecretVersionsTx(ctx context.Context, tx pgx.Tx, limit int) (credentialVersions, providerVersions int64, err error) {
	credentialTag, err := tx.Exec(ctx, `
		WITH candidates AS (
			SELECT cv.id
			FROM plugin_credential_versions cv
			WHERE NOT EXISTS (
				SELECT 1 FROM plugin_installations pi
				WHERE pi.credential_version_id=cv.id
			)
			AND NOT EXISTS (
				SELECT 1
				FROM run_plugin_snapshots s
				JOIN runs r ON r.id=s.run_id
				WHERE s.credential_version_id=cv.id
				  AND (
					r.status NOT IN ('succeeded','failed','canceled')
					OR (r.kind='review' AND r.status='succeeded' AND (r.review_output<>'' OR r.review_result IS NOT NULL) AND r.delivery_status IN ('not_required','pending'))
					OR EXISTS (
						SELECT 1
						FROM automation_kanban_claims c
						WHERE c.run_id=r.id AND c.writeback_at IS NULL
					)
					OR EXISTS (
						SELECT 1
						FROM review_status_comments c
						WHERE c.current_run_id=r.id AND NOT (
							c.comment_id<>'' AND c.desired_state=c.applied_state
							AND c.desired_body_hash=c.applied_body_hash
							AND c.applied_state IN ('completed','failed','canceled','superseded')
							AND c.observed_run_status=r.status
							AND c.observed_run_phase=r.phase
							AND c.observed_failure_reason=r.failure_reason
							AND c.observed_delivery_status=r.delivery_status
							AND c.observed_review_posted=(r.review_posted_at IS NOT NULL)
							AND c.observed_review_plan_hash=COALESCE(r.review_plan->>'plan_hash','')
						)
					)
				  )
			)
			ORDER BY cv.created_at,cv.id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM plugin_credential_versions cv
		USING candidates c
		WHERE cv.id=c.id`, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("delete unreferenced plugin credential versions: %w", err)
	}

	providerTag, err := tx.Exec(ctx, `
		WITH candidates AS (
			SELECT pv.provider,pv.config_revision
			FROM provider_config_versions pv
			WHERE NOT EXISTS (
				SELECT 1 FROM provider_configs pc
				WHERE pc.provider=pv.provider AND pc.config_revision=pv.config_revision
			)
			AND NOT EXISTS (
				SELECT 1
				FROM run_plugin_snapshots s
				JOIN runs r ON r.id=s.run_id
				WHERE s.provider=pv.provider
				  AND s.provider_config_revision=pv.config_revision
				  AND (
					r.status NOT IN ('succeeded','failed','canceled')
					OR (r.kind='review' AND r.status='succeeded' AND (r.review_output<>'' OR r.review_result IS NOT NULL) AND r.delivery_status IN ('not_required','pending'))
					OR EXISTS (
						SELECT 1
						FROM automation_kanban_claims c
						WHERE c.run_id=r.id AND c.writeback_at IS NULL
					)
					OR EXISTS (
						SELECT 1
						FROM review_status_comments c
						WHERE c.current_run_id=r.id AND NOT (
							c.comment_id<>'' AND c.desired_state=c.applied_state
							AND c.desired_body_hash=c.applied_body_hash
							AND c.applied_state IN ('completed','failed','canceled','superseded')
							AND c.observed_run_status=r.status
							AND c.observed_run_phase=r.phase
							AND c.observed_failure_reason=r.failure_reason
							AND c.observed_delivery_status=r.delivery_status
							AND c.observed_review_posted=(r.review_posted_at IS NOT NULL)
							AND c.observed_review_plan_hash=COALESCE(r.review_plan->>'plan_hash','')
						)
					)
				  )
			)
			ORDER BY pv.provider,pv.config_revision
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM provider_config_versions pv
		USING candidates c
		WHERE pv.provider=c.provider AND pv.config_revision=c.config_revision`, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("delete unreferenced provider config versions: %w", err)
	}
	return credentialTag.RowsAffected(), providerTag.RowsAffected(), nil
}

func (s *PGStore) CreatePluginAuditEvent(ctx context.Context, event *domain.PluginAuditEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO plugin_audit_events(id,project_id,installation_id,actor_user_id,event_type,detail,created_at)VALUES($1,$2,$3,$4,$5,$6,$7)`, event.ID, event.ProjectID, nullStr(event.InstallationID), nullStr(event.ActorUserID), event.EventType, event.Detail, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("create plugin audit event: %w", err)
	}
	return nil
}
func (s *PGStore) ListPluginAuditEvents(ctx context.Context, projectID string, limit int) ([]domain.PluginAuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id,project_id,installation_id,actor_user_id,event_type,detail,created_at FROM plugin_audit_events WHERE project_id=$1 ORDER BY created_at DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list plugin audit events: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginAuditEvent{}
	for rows.Next() {
		var e domain.PluginAuditEvent
		var inst, actor *string
		if err := rows.Scan(&e.ID, &e.ProjectID, &inst, &actor, &e.EventType, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if inst != nil {
			e.InstallationID = *inst
		}
		if actor != nil {
			e.ActorUserID = *actor
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PGStore) ListPluginInstallationAuditEvents(ctx context.Context, projectID, installationID string, limit int) ([]domain.PluginAuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id,project_id,installation_id,actor_user_id,event_type,detail,created_at FROM plugin_audit_events WHERE project_id=$1 AND installation_id=$2 ORDER BY created_at DESC LIMIT $3`, projectID, installationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list plugin installation audit events: %w", err)
	}
	defer rows.Close()
	out := []domain.PluginAuditEvent{}
	for rows.Next() {
		var e domain.PluginAuditEvent
		var inst, actor *string
		if err := rows.Scan(&e.ID, &e.ProjectID, &inst, &actor, &e.EventType, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if inst != nil {
			e.InstallationID = *inst
		}
		if actor != nil {
			e.ActorUserID = *actor
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
