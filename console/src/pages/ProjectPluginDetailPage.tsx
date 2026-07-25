import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, Trash, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { ErrorBlock, LoadingBlock } from '../components/States';
import {
  usePluginBoards,
  usePluginRepositories,
  usePluginWorkspaces,
  useProject,
  useProjectAutomations,
  useProjectPluginImpact,
  useProjectPluginAudit,
  useProjectPlugins,
  useSetProjectPluginEnabled,
  useSetProjectPluginWorkspace,
  useUninstallProjectPlugin,
} from '../api/queries';
import type { ProjectPlugin, ProviderKind } from '../api/types';
import { PluginConsentModal, ProviderMark, providerName } from './ProjectPluginsPanel';
import styles from './ProjectPluginDetailPage.module.css';

function isProviderKind(value: string | undefined): value is ProviderKind {
  return value === 'github' || value === 'gitlab' || value === 'gitea' || value === 'jtype';
}

function disconnected(provider: ProviderKind): ProjectPlugin {
  return { provider, status: 'not_connected', scopes: [] };
}

export function ProjectPluginDetailPage() {
  const { t } = useTranslation();
  const { projectId = '', provider: rawProvider } = useParams();
  const provider = isProviderKind(rawProvider) ? rawProvider : null;
  const project = useProject(projectId);
  const plugins = useProjectPlugins(projectId, !!provider);
  const automations = useProjectAutomations(projectId, !!provider);
  const item = provider
    ? (plugins.data ?? []).find((plugin) => plugin.provider === provider) ?? disconnected(provider)
    : null;
  const installationId = item?.id ?? '';
  const connected = !!item && item.status !== 'not_connected';
  const repositories = usePluginRepositories(projectId, installationId, '', connected && provider !== 'jtype');
  const workspaces = usePluginWorkspaces(projectId, installationId, connected && provider === 'jtype');
  const workspaceId = item?.workspace_id ?? workspaces.data?.[0]?.id ?? '';
  const boards = usePluginBoards(projectId, installationId, workspaceId, connected && provider === 'jtype');
  const toggle = useSetProjectPluginEnabled(projectId);
  const setWorkspace = useSetProjectPluginWorkspace(projectId);
  const uninstall = useUninstallProjectPlugin(projectId);
  const [consentOpen, setConsentOpen] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [acknowledgement, setAcknowledgement] = useState('');
  const [forceUninstall, setForceUninstall] = useState(false);
  const impact = useProjectPluginImpact(projectId, installationId, confirmOpen);
  const audit = useProjectPluginAudit(projectId, installationId, connected);
  const canManage = (project.data?.role ?? 'owner') === 'owner';
  const services = (project.data?.services ?? []).filter((service) => service.provider === provider);
  const serviceIds = new Set(services.map((service) => service.id));
  const relatedAutomations = (automations.data ?? []).filter((spec) =>
    serviceIds.has(spec.automation.service_id) || spec.automation.installation_id === installationId);

  if (!provider) return <ErrorBlock error={new Error(t('plugins.invalidProvider'))} />;
  if (project.isLoading || plugins.isLoading || automations.isLoading) {
    return <LoadingBlock label={t('plugins.loading')} />;
  }
  if (project.isError || plugins.isError || automations.isError) {
    return (
      <ErrorBlock
        error={project.error ?? plugins.error ?? automations.error}
        onRetry={() => void plugins.refetch()}
        title={t('plugins.loadError')}
      />
    );
  }
  if (!item) return null;
  const healthError = item.last_health_error ?? item.last_error;
  const canToggle = (item.status === 'enabled' || item.status === 'disabled') &&
    (provider !== 'jtype' || !!item.workspace_id);

  return (
    <main className={styles.page} data-testid="plugin-detail-page">
      <Link className={styles.back} to={`/projects/${encodeURIComponent(projectId)}?view=project-settings&settings=plugins`}>
        <ArrowLeft size={16} aria-hidden />
        {t('plugins.back')}
      </Link>
      <header className={styles.head}>
        <span className={styles.mark}><ProviderMark provider={provider} size={28} /></span>
        <div>
          <p>{t('plugins.projectPlugin')}</p>
          <h1>{providerName(provider)}</h1>
          <span data-status={item.status}>{t(`plugins.status.${item.status}`)}</span>
        </div>
        {canManage && !connected && (
          <Button type="button" size="sm" onClick={() => setConsentOpen(true)}>{t('plugins.connect')}</Button>
        )}
        {canManage && connected && installationId && canToggle && (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            loading={toggle.isPending}
            onClick={() => toggle.mutate({ installationId, enabled: item.status !== 'enabled' })}
          >
            {item.status === 'enabled' ? t('plugins.disable') : t('plugins.enable')}
          </Button>
        )}
        {canManage && connected && installationId && (
          <Button type="button" variant="secondary" size="sm" onClick={() => setConsentOpen(true)}>
            Reconnect
          </Button>
        )}
      </header>
      {healthError && <div className={styles.error} role="alert"><Warning size={17} aria-hidden />{healthError}</div>}

      <section className={styles.section}>
        <h2>{t('plugins.identity')}</h2>
        <p>{item.external_account ?? item.workspace_id ?? t('plugins.notConnected')}</p>
        {item.external_account_id && <small>Stable ID: {item.external_account_id}</small>}
        {item.consented_at && <small>Consent {item.consent_version ?? 'unknown'} · {new Date(item.consented_at).toLocaleString()}</small>}
      </section>

      <section className={styles.section}>
        <h2>{t('plugins.scopes')}</h2>
        {item.scopes.length
          ? <ul className={styles.pills}>{item.scopes.map((scope) => <li key={scope}>{scope}</li>)}</ul>
          : <p>{t('plugins.noScopes')}</p>}
      </section>

      <section className={styles.section}>
        <h2>{t('plugins.resources')}</h2>
        {(repositories.isError || workspaces.isError || boards.isError) && (
          <ErrorBlock error={repositories.error ?? workspaces.error ?? boards.error} title="Provider resources could not be listed" />
        )}
        {provider === 'jtype' ? (
          <>
            {canManage && connected && installationId && (
              <label className={styles.workspacePicker}>
                <span>Bound workspace</span>
                <select
                  value={item.workspace_id ?? ''}
                  disabled={workspaces.isLoading || setWorkspace.isPending}
                  onChange={(event) => {
                    const nextWorkspace = event.target.value;
                    if (nextWorkspace) {
                      setWorkspace.mutate({ installationId, workspaceId: nextWorkspace });
                    }
                  }}
                >
                  <option value="">Select a workspace…</option>
                  {(workspaces.data ?? []).map((workspace) => (
                    <option key={workspace.id} value={workspace.id}>{workspace.name}</option>
                  ))}
                </select>
                {!item.workspace_id && <small>Selecting a workspace finishes the JType Plugin setup and enables it.</small>}
                {setWorkspace.error && <small className={styles.fieldError}>{(setWorkspace.error as Error).message}</small>}
              </label>
            )}
            {(workspaces.data ?? []).map((workspace) => <p key={workspace.id}><strong>{workspace.name}</strong> · workspace</p>)}
            <ul className={styles.resources}>
              {(boards.data ?? []).map((board) => <li key={board.id}><strong>{board.title}</strong><small>{board.ref} · {board.columns.length} columns</small></li>)}
            </ul>
            {!workspaces.isLoading && !boards.isLoading && !(workspaces.data?.length || boards.data?.length) && <p>{t('plugins.noResources')}</p>}
          </>
        ) : (
          <>
            <ul className={styles.resources}>
              {(repositories.data ?? []).map((repository) => (
                <li key={repository.id}>
                  <strong>{repository.full_name}</strong>
                  <small>{repository.private ? 'private' : 'public'} · {repository.default_branch ?? 'default branch unknown'}</small>
                </li>
              ))}
            </ul>
            {!repositories.isLoading && !repositories.data?.length && <p>{t('plugins.noResources')}</p>}
          </>
        )}
      </section>

      <section className={styles.section}>
        <h2>Services</h2>
        {services.length ? (
          <ul className={styles.resources}>
            {services.map((service) => <li key={service.id}><strong>{service.name}</strong><small>{service.repo_owner_name ?? service.default_branch}</small></li>)}
          </ul>
        ) : <p>No Services are bound to this Plugin.</p>}
      </section>

      <section className={styles.section}>
        <h2>{t('plugins.automationSummary')}</h2>
        {relatedAutomations.length ? (
          <ul className={styles.resources}>
            {relatedAutomations.map((spec) => <li key={spec.automation.id}><strong>{spec.automation.name}</strong><small>{spec.automation.trigger_kind}</small></li>)}
          </ul>
        ) : <p>No Automations depend on this Plugin.</p>}
        <Link to={`/projects/${encodeURIComponent(projectId)}?tab=automations`}>{t('plugins.openAutomations')}</Link>
      </section>

      <section className={styles.section}>
        <h2>{t('plugins.audit')}</h2>
        {audit.isError && <ErrorBlock error={audit.error} title="Plugin audit history could not be loaded" />}
        {audit.isLoading && <p role="status">Loading audit history…</p>}
        {audit.data?.length ? (
          <ul className={styles.resources}>
            {audit.data.map((event) => (
              <li key={event.id}>
                <strong>{event.event_type.replaceAll('_', ' ')}</strong>
                <small>{new Date(event.created_at).toLocaleString()}{event.detail ? ` · ${event.detail}` : ''}</small>
              </li>
            ))}
          </ul>
        ) : (!audit.isLoading && !audit.isError && <p>No audit events have been recorded.</p>)}
      </section>

      {canManage && connected && installationId && (
        <section className={[styles.section, styles.danger].join(' ')}>
          <h2>{t('plugins.uninstall')}</h2>
          <p>{t('plugins.uninstallHint')}</p>
          <Button type="button" variant="secondary" size="sm" onClick={() => setConfirmOpen(true)}>
            <Trash size={15} aria-hidden />
            {t('plugins.uninstall')}
          </Button>
        </section>
      )}

      {consentOpen && (
        <PluginConsentModal projectId={projectId} provider={provider} onClose={() => setConsentOpen(false)} />
      )}
      <Modal
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        title={t('plugins.uninstallTitle', { provider: providerName(provider) })}
        footer={(
          <>
            <Button type="button" variant="ghost" onClick={() => setConfirmOpen(false)}>{t('common.cancel')}</Button>
            <Button
              type="button"
              variant="secondary"
              loading={uninstall.isPending}
              disabled={acknowledgement !== 'UNINSTALL' || impact.isLoading}
              onClick={() => uninstall.mutate({ installationId, force: forceUninstall }, {
                onSuccess: () => {
                  setConfirmOpen(false);
                  setAcknowledgement('');
                  setForceUninstall(false);
                },
              })}
            >
              {t('plugins.uninstall')}
            </Button>
          </>
        )}
      >
        <div className={styles.confirm}>
          <p>{t('plugins.uninstallConfirm')}</p>
          {impact.isLoading && <p>Loading impact…</p>}
          {impact.isError && <ErrorBlock error={impact.error} title="Impact could not be loaded" />}
          {impact.data && <p><strong>{impact.data.services}</strong> Services and <strong>{impact.data.automations}</strong> Automations will be permanently deleted.</p>}
          <label>
            {t('plugins.uninstallType')}
            <input value={acknowledgement} onChange={(event) => setAcknowledgement(event.target.value)} autoComplete="off" />
          </label>
          {uninstall.isError && (
            <label>
              <input type="checkbox" checked={forceUninstall} onChange={(event) => setForceUninstall(event.target.checked)} />
              Force local removal if only Provider webhook cleanup is failing. This may leave an external hook behind.
            </label>
          )}
          {uninstall.error && <p className={styles.error} role="alert">{(uninstall.error as Error).message}</p>}
        </div>
      </Modal>
    </main>
  );
}
