import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { GithubLogo, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
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
import { ApiError } from '../api/client';
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
              {(plugin.last_health_error || plugin.last_error) && <p className={styles.error}><Warning size={15} aria-hidden /> {plugin.last_health_error ?? plugin.last_error}</p>}
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
        setError('The Provider returned an incomplete connection response.');
      },
      onError: (reason) => setError(reason instanceof ApiError ? reason.message : t('plugins.connectError')),
    });
  };
  const previewInstallation = (installationId: string) => {
    setError('');
    previewGitHub.mutate(installationId, {
      onSuccess: (preview) => {
        setGitHubPreview(preview);
        setAcknowledged(false);
      },
      onError: (reason) => setError(reason instanceof ApiError ? reason.message : t('plugins.connectError')),
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
      onError: (reason) => setError(reason instanceof ApiError ? reason.message : t('plugins.connectError')),
    });
  };
  const canContinue = acknowledged && !deviceStart && provider !== 'github';
  return (
    <Modal open onClose={onClose} title={t('plugins.consentTitle', { provider: providerName(provider) })} data-testid="plugin-consent-modal"
      footer={<>
        <Button variant="ghost" type="button" onClick={onClose}>{deviceStart ? 'Close' : t('common.cancel')}</Button>
        {provider !== 'github' && !deviceStart && <Button type="button" onClick={submit} disabled={!canContinue} loading={install.isPending}>{t('plugins.continueToProvider')}</Button>}
      </>}>
      <div className={styles.consent}>
        <p>{t('plugins.consentIntro', { provider: providerName(provider) })}</p>
        <p><strong>Provider instance:</strong> {providerCapabilities.data?.instance_url || (provider === 'github' ? 'https://github.com' : 'Configured cluster instance')}</p>
        <div>
          <strong>Requested scopes</strong>
          <ul>
            {(providerCapabilities.data?.oauth_scopes ?? providerScopes(provider)).map((scope) => <li key={scope}><code>{scope}</code></li>)}
          </ul>
        </div>
        {coarseScopeDisclosure(provider) && (
          <p className={styles.riskDisclosure} role="alert">
            <Warning size={18} aria-hidden />
            {coarseScopeDisclosure(provider)}
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
            <strong>Choose a GitHub App Installation</strong>
            <p>Select an Installation to inspect its actual permissions before consenting. The selection is explicit for this Project and is never reused automatically.</p>
            {githubInstallations.isLoading && <p>Loading GitHub Installations…</p>}
            {githubInstallations.isError && <ErrorBlock error={githubInstallations.error} title="GitHub Installations could not be listed" />}
            {(githubInstallations.data ?? []).map((item) => (
              <button key={item.id} type="button" onClick={() => previewInstallation(item.id)} disabled={previewGitHub.isPending || selectGitHub.isPending}>
                <ProviderMark provider="github" size={18} />
                <span><strong>{item.account}</strong><small>{item.target_type} · {item.repository_selection} repositories</small></span>
              </button>
            ))}
            {githubInstallations.isSuccess && !githubInstallations.data.length && <p>No manageable GitHub App Installations were found. Install or adjust the GitHub App on GitHub, then retry.</p>}
            {githubPreview && (
              <div>
                <strong>Actual permissions for {githubPreview.account}</strong>
                <ul>{githubPreview.scopes.map((scope) => <li key={scope}><code>{scope}</code></li>)}</ul>
                <label className={styles.checkbox}><input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />I reviewed these actual GitHub App permissions and accept the Project-wide credential risks above.</label>
                <Button type="button" onClick={selectInstallation} disabled={!acknowledged} loading={selectGitHub.isPending}>Connect this Installation</Button>
              </div>
            )}
          </div>
        )}
        {provider === 'jtype' && deviceStart && (
          <div className={styles.deviceFlow}>
            <strong>Approve in JType</strong>
            <p>Use code <code>{deviceStart.user_code}</code>. This dialog will keep checking until JType confirms the grant.</p>
            <a href={deviceStart.verification_uri_complete ?? deviceStart.verification_uri} target="_blank" rel="noopener noreferrer">Open JType consent page</a>
            <p role="status">Status: {jtypeStatus.data?.status ?? (jtypeStatus.isError ? 'could not check status' : 'pending')}</p>
            {jtypeStatus.isError && <ErrorBlock error={jtypeStatus.error} title="JType connection status could not be checked" />}
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

function coarseScopeDisclosure(provider: ProviderKind): string {
  if (provider === 'gitlab') {
    return 'GitLab’s coarse api scope can authorize broader read/write API operations than this Plugin normally needs, including repository settings allowed to your external role. jcode has no command allowlist that technically prevents every destructive API call.';
  }
  if (provider === 'gitea') {
    return 'Gitea’s coarse write:repository scope may authorize repository settings and destructive operations beyond normal task workflows, depending on the instance version and your external role. jcode has no command allowlist.';
  }
  if (provider === 'jtype') {
    return 'JType full scope authorizes every supported read/write operation in the selected cloud workspace, not only Kanban triggers.';
  }
  return '';
}
