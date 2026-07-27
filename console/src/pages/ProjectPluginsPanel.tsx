import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { GithubLogo, Warning } from '@phosphor-icons/react';
import { Trans, useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { Modal } from '../components/Modal';
import {
  useGitHubAppInstallations,
  useJTypePluginConnectStatus,
  usePreviewGitHubAppInstallationConsent,
  useProjectPlugins,
  useProviderCapabilities,
  useSelectGitHubAppInstallation,
  useStartPluginInstall,
} from '../api/queries';
import type { GitHubInstallationConsentPreview, PluginInstallStart, PluginStatus, Project, ProjectPlugin, ProviderKind } from '../api/types';
import { apiErrorCode } from '../api/client';
import styles from './ProjectPluginsPanel.module.css';

const PROVIDERS: readonly ProviderKind[] = ['github', 'gitlab', 'gitea', 'jtype'];

export function providerName(provider: ProviderKind): string {
  return { github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', jtype: 'JType Kanban' }[provider];
}

/** Provider marks are canonical where the current icon bundle supplies them.
 * Gitea/JType retain their established product-shaped marks until their owned
 * SVG assets are exposed by the shared design package. */
export function ProviderMark({ provider, size = 24 }: { provider: ProviderKind; size?: number }) {
  if (provider === 'github') return <GithubLogo size={size} weight="fill" aria-hidden />;
  return (
    <img
      src={`/provider-${provider}.svg`}
      width={size}
      height={size}
      alt=""
      aria-hidden
    />
  );
}

function statusLabel(status: PluginStatus, t: (key: string) => string) {
  return t(`plugins.status.${status}`);
}

export function pluginHealthErrorText(message: string, t: (key: string) => string): string {
  if (message === 'JType device authorization could not be started') return t('plugins.healthError.jtypeStartFailed');
  if (message === 'Provider webhook cleanup failed; retry uninstall or explicitly force local removal') return t('plugins.healthError.webhookCleanupFailed');
  if (message === 'Runtime cleanup failed; retry uninstall after the cluster is healthy') return t('plugins.healthError.runtimeCleanupFailed');
  if (message === 'OAuth access expired and refresh failed; reconnect this Plugin') return t('plugins.healthError.oauthRefreshFailed');
  if (message.includes('Cluster Provider identity, URL, credentials, or Plugin availability changed')) return t('plugins.healthError.providerConfigChanged');
  return message;
}

export function pluginOperationErrorText(error: unknown, t: (key: string) => string): string {
  const code = apiErrorCode(error);
  switch (code) {
  case 'github_app_not_configured': return t('plugins.apiError.githubAppNotConfigured');
  case 'github_identity_required': return t('plugins.apiError.githubIdentityRequired');
  case 'github_installation_forbidden': return t('plugins.apiError.githubInstallationForbidden');
  case 'github_app_error': return t('plugins.apiError.githubAppError');
  case 'consent_scope_changed': return t('plugins.apiError.consentScopeChanged');
  case 'provider_not_configured': return t('plugins.apiError.providerNotConfigured');
  case 'plugin_credential_unavailable': return t('plugins.apiError.credentialUnavailable');
  case 'plugin_reconnect_required':
  case 'reconnect_required': return t('plugins.apiError.reconnectRequired');
  case 'connect_expired': return t('plugins.apiError.connectExpired');
  case 'workspace_not_available': return t('plugins.apiError.workspaceUnavailable');
  case 'workspace_required': return t('plugins.apiError.workspaceRequired');
  case 'webhook_cleanup_failed': return t('plugins.apiError.webhookCleanupFailed');
  case 'cleanup_failed': return t('plugins.apiError.runtimeCleanupFailed');
  case 'plugin_connect_not_implemented': return t('plugins.apiError.connectUnsupported');
  case 'cipher_not_configured': return t('plugins.apiError.encryptionUnavailable');
  case 'consent_required': return t('plugins.apiError.consentRequired');
  case 'provider_error':
  case 'provider_unavailable': return t('plugins.apiError.providerUnavailable');
  default: return t('plugins.apiError.generic');
  }
}

export function ProjectPluginsPanel({ project }: { project: Project }) {
  const { t } = useTranslation();
  const query = useProjectPlugins(project.id);
  const [consentProvider, setConsentProvider] = useState<ProviderKind | null>(null);
  const byProvider = new Map((query.data ?? []).map((plugin) => [plugin.provider, plugin]));
  const canManage = (project.role ?? 'owner') === 'owner';

  if (query.isLoading) return <LoadingBlock label={t('plugins.loading')} />;
  if (query.isError) return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('plugins.loadError')} />;

  return (
    <section className={styles.surface} data-testid="project-plugins-panel">
      <div className={styles.intro}>
        <p>{t('plugins.intro')}</p>
        {!canManage && <p className={styles.readOnly}>{t('plugins.readOnly')}</p>}
      </div>
      <div className={styles.grid}>
        {PROVIDERS.map((provider) => {
          const plugin = byProvider.get(provider) ?? disconnectedPlugin(provider);
          return (
            <article className={styles.card} key={provider} data-testid={`plugin-card-${provider}`}>
              <Link className={styles.cardLink} to={`/projects/${encodeURIComponent(project.id)}/plugins/${provider}`}>
                <span className={styles.mark}><ProviderMark provider={provider} /></span>
                <span className={styles.copy}>
                  <strong>{providerName(provider)}</strong>
                  <small>{plugin.external_account ?? plugin.account_name ?? plugin.workspace_id ?? t('plugins.notConnected')}</small>
                </span>
                <span className={styles.status} data-status={plugin.status}>{statusLabel(plugin.status, t)}</span>
                <span className={styles.summary}>{t('plugins.summary', { services: plugin.service_count ?? 0, automations: plugin.automation_count ?? 0 })}</span>
              </Link>
              {canManage && plugin.status === 'not_connected' && (
                <Button type="button" size="sm" variant="secondary" onClick={() => setConsentProvider(provider)}>
                  {t('plugins.connect')}
                </Button>
              )}
              {(plugin.last_health_error || plugin.last_error) && <p className={styles.error}><Warning size={15} aria-hidden /> {pluginHealthErrorText(plugin.last_health_error ?? plugin.last_error ?? '', t)}</p>}
            </article>
          );
        })}
      </div>
      {consentProvider && (
        <PluginConsentModal
          projectId={project.id}
          provider={consentProvider}
          onClose={() => setConsentProvider(null)}
        />
      )}
    </section>
  );
}

export function PluginConsentModal({ projectId, provider, onClose }: { projectId: string; provider: ProviderKind; onClose: () => void }) {
  const { t } = useTranslation();
  const install = useStartPluginInstall(projectId);
  const plugins = useProjectPlugins(projectId);
  const githubInstallations = useGitHubAppInstallations(projectId, provider === 'github');
  const providerCapabilities = useProviderCapabilities(provider, true);
  const selectGitHub = useSelectGitHubAppInstallation(projectId);
  const previewGitHub = usePreviewGitHubAppInstallationConsent(projectId);
  const [acknowledged, setAcknowledged] = useState(false);
  const [githubPreview, setGitHubPreview] = useState<GitHubInstallationConsentPreview | null>(null);
  const [deviceStart, setDeviceStart] = useState<PluginInstallStart | null>(null);
  const [error, setError] = useState('');
  const jtypeInstallation = (plugins.data ?? []).find((item) =>
    item.provider === 'jtype' && item.id && item.status !== 'not_connected');
  const jtypeStatus = useJTypePluginConnectStatus(
    projectId,
    jtypeInstallation?.id ?? '',
    deviceStart?.connect_id ?? '',
    provider === 'jtype' && !!deviceStart,
  );
  const refetchPlugins = plugins.refetch;
  useEffect(() => {
    if (jtypeStatus.data?.status === 'complete') void refetchPlugins();
  }, [jtypeStatus.data?.status, refetchPlugins]);

  const submit = () => {
    setError('');
    install.mutate({ provider, input: {
      consent_version: 'plugin-platform-v2-coarse-scope',
      consent_accepted: true,
      scopes: providerScopes(provider),
    } }, {
      onSuccess: (result) => {
        if (result.authorize_url) {
          window.location.assign(result.authorize_url);
          return;
        }
        if (result.connect_id && result.verification_uri) {
          setDeviceStart(result);
          return;
        }
        setError(t('plugins.incompleteResponse'));
      },
      onError: (reason) => setError(pluginOperationErrorText(reason, t)),
    });
  };
  const previewInstallation = (installationId: string) => {
    setError('');
    previewGitHub.mutate(installationId, {
      onSuccess: (preview) => {
        setGitHubPreview(preview);
        setAcknowledged(false);
      },
      onError: (reason) => setError(pluginOperationErrorText(reason, t)),
    });
  };
  const selectInstallation = () => {
    if (!githubPreview) return;
    setError('');
    selectGitHub.mutate({
      installationId: githubPreview.installation_id,
      input: {
        consent_version: 'plugin-platform-v2-coarse-scope',
        consent_accepted: true,
        scopes: githubPreview.scopes,
        scope_digest: githubPreview.scope_digest,
      },
    }, {
      onSuccess: onClose,
      onError: (reason) => setError(pluginOperationErrorText(reason, t)),
    });
  };
  const canContinue = acknowledged && !deviceStart && provider !== 'github';
  return (
    <Modal open onClose={onClose} title={t('plugins.consentTitle', { provider: providerName(provider) })} data-testid="plugin-consent-modal"
      footer={<>
        <Button variant="ghost" type="button" onClick={onClose}>{deviceStart ? t('common.close') : t('common.cancel')}</Button>
        {provider !== 'github' && !deviceStart && <Button type="button" onClick={submit} disabled={!canContinue} loading={install.isPending}>{t('plugins.continueToProvider')}</Button>}
      </>}>
      <div className={styles.consent}>
        <p>{t('plugins.consentIntro', { provider: providerName(provider) })}</p>
        <p><strong>{t('plugins.providerInstance')}:</strong> {providerCapabilities.data?.instance_url || (provider === 'github' ? 'https://github.com' : t('plugins.configuredClusterInstance'))}</p>
        <div>
          <strong>{t('plugins.requestedScopes')}</strong>
          <ul>
            {(providerCapabilities.data?.oauth_scopes ?? providerScopes(provider)).map((scope) => <li key={scope}><code>{scope}</code></li>)}
          </ul>
        </div>
        {coarseScopeDisclosure(provider, t) && (
          <p className={styles.riskDisclosure} role="alert">
            <Warning size={18} aria-hidden />
            {coarseScopeDisclosure(provider, t)}
          </p>
        )}
        <ul>
          <li>{t('plugins.consentScope')}</li>
          <li>{t('plugins.consentShared')}</li>
          <li>{t('plugins.consentTasks')}</li>
          <li>{t('plugins.consentPersistence')}</li>
          <li>{t('plugins.consentPublic')}</li>
        </ul>
        {provider !== 'github' && <label className={styles.checkbox}><input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />{t('plugins.consentAcknowledge')}</label>}
        {provider === 'github' && (
          <div className={styles.installations}>
            <strong>{t('plugins.githubChooseInstallation')}</strong>
            <p>{t('plugins.githubChooseInstallationHint')}</p>
            {githubInstallations.isLoading && <p>{t('plugins.githubInstallationsLoading')}</p>}
            {githubInstallations.isError && <ErrorBlock error={new Error(pluginOperationErrorText(githubInstallations.error, t))} title={t('plugins.githubInstallationsLoadError')} />}
            {(githubInstallations.data ?? []).map((item) => (
              <button key={item.id} type="button" onClick={() => previewInstallation(item.id)} disabled={previewGitHub.isPending || selectGitHub.isPending}>
                <ProviderMark provider="github" size={18} />
                <span><strong>{item.account}</strong><small>{t('plugins.githubInstallationSummary', {
                  target: item.target_type === 'Organization'
                    ? t('plugins.githubTarget.organization')
                    : item.target_type === 'User' ? t('plugins.githubTarget.user') : item.target_type,
                  selection: item.repository_selection === 'selected'
                    ? t('plugins.githubSelection.selected')
                    : item.repository_selection === 'all' ? t('plugins.githubSelection.all') : item.repository_selection,
                })}</small></span>
              </button>
            ))}
            {githubInstallations.isSuccess && !githubInstallations.data.length && <p>{t('plugins.githubInstallationsEmpty')}</p>}
            {githubPreview && (
              <div>
                <strong>{t('plugins.githubActualPermissions', { account: githubPreview.account })}</strong>
                <ul>{githubPreview.scopes.map((scope) => <li key={scope}><code>{scope}</code></li>)}</ul>
                <label className={styles.checkbox}><input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />{t('plugins.githubPermissionsAcknowledge')}</label>
                <Button type="button" onClick={selectInstallation} disabled={!acknowledged} loading={selectGitHub.isPending}>{t('plugins.githubConnectInstallation')}</Button>
              </div>
            )}
          </div>
        )}
        {provider === 'jtype' && deviceStart && (
          <div className={styles.deviceFlow}>
            <strong>{t('plugins.jtypeApprove')}</strong>
            <p><Trans i18nKey="plugins.jtypeApproveHint" values={{ code: deviceStart.user_code }} components={{ code: <code /> }} /></p>
            <a href={deviceStart.verification_uri_complete ?? deviceStart.verification_uri} target="_blank" rel="noopener noreferrer">{t('plugins.jtypeOpenConsent')}</a>
            <p role="status">{t('plugins.connectionStatusLabel', {
              status: jtypeStatus.isError
                ? t('plugins.connectionStatus.checkFailed')
                : t(`plugins.connectionStatus.${jtypeStatus.data?.status ?? 'pending'}`),
            })}</p>
            {jtypeStatus.isError && <ErrorBlock error={new Error(pluginOperationErrorText(jtypeStatus.error, t))} title={t('plugins.jtypeStatusLoadError')} />}
          </div>
        )}
        {error && <p className={styles.error} role="alert">{error}</p>}
      </div>
    </Modal>
  );
}

function disconnectedPlugin(provider: ProviderKind): ProjectPlugin {
  return { provider, status: 'not_connected', scopes: [], service_count: 0, automation_count: 0 };
}

export function providerScopes(provider: ProviderKind): string[] {
  if (provider === 'github') {
    return ['contents:write', 'pull_requests:write', 'issues:write', 'checks:write', 'actions:write', 'metadata:read'];
  }
  if (provider === 'gitlab') return ['read_user', 'api'];
  if (provider === 'gitea') return ['read:user', 'write:repository'];
  return ['full'];
}

function coarseScopeDisclosure(provider: ProviderKind, t: (key: string) => string): string {
  if (provider === 'gitlab') {
    return t('plugins.coarseScope.gitlab');
  }
  if (provider === 'gitea') {
    return t('plugins.coarseScope.gitea');
  }
  if (provider === 'jtype') {
    return t('plugins.coarseScope.jtype');
  }
  return '';
}
