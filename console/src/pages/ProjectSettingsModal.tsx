/*
 * ProjectSettingsModal — owner/cluster-admin project settings (blueprint §2/§5).
 * Sections:
 *   - General: project rename, the guardrails editor (owner only — concurrency
 *     cap, run timeout, injected env), and a Delete-project action behind a
 *     confirm step. Repo config (branch / git mode) lives on each repository on
 *     the project page — a project is a pure container.
 *   - Members: roster with role management + add-by-search (MembersPanel).
 *   - Bot integrations (D19 / F5): git host bindings for unattended service
 *     execution. They are intentionally distinct from a member's OAuth-based
 *     provider webhook setup in the Service Automation area.
 *   - Kanban: jtype board→service bindings (owner).
 *   - Model access: models granted to this project (members can view).
 *   - API keys (F12 / D24): project-scoped, revocable automation credentials
 *     (owner) — replaces borrowing CONSOLE_TOKEN for external/CI use.
 */
import { useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash } from '@phosphor-icons/react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { SelectField, TextField } from '../components/Field';
import { MembersPanel } from './MembersPanel';
import { IntegrationsPanel } from './IntegrationsPanel';
import { ProjectModelsPanel } from './models/ProjectModelsPanel';
import {
  useUpdateProject,
  useDeleteProject,
  useSystem,
  useProjectKanbanLinks,
  useProjectBoardLinks,
  useCreateProjectKanbanLink,
  useUpdateProjectKanbanLinkToken,
  useDeleteProjectKanbanLink,
  useJtypeWorkspaces,
  useJtypeBoards,
  useStartLinkConnect,
  useLinkConnectStatus,
  useStartProjectConnect,
  useProjectConnectStatus,
  useApiKeys,
  useCreateApiKey,
  useRevokeApiKey,
} from '../api/queries';
import { useToast } from '../components/Toast';
import { KanbanConnectFlow, expiryLabel } from '../components/KanbanConnect';
import { KanbanBoardModal } from './KanbanBoardModal';
import { ApiError, apiErrorCode } from '../api/client';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';
import { timeAgo } from '../lib/format';
import { PageHeader, SurfaceInner, pageLayoutStyles } from '../components/PageLayout';
import type {
  ApiKey,
  CreateApiKeyResponse,
  KanbanLink,
  Project,
  UpdateProjectInput,
} from '../api/types';
import styles from './ProjectSettingsModal.module.css';

type Tab = 'general' | 'members' | 'integrations' | 'kanban' | 'apikeys';

interface EnvRow {
  key: string;
  value: string;
}

/** Parse a guardrail number field: empty/≤0/NaN => null ("inherit"). */
function parseGuardrail(s: string): number | null {
  const t = s.trim();
  if (t === '') return null;
  const n = Number(t);
  if (!Number.isFinite(n) || !Number.isInteger(n) || n <= 0) return null;
  return n;
}

function rowsToEnv(rows: EnvRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value;
  }
  return out;
}

function envToRows(env: Record<string, string> | undefined): EnvRow[] {
  return Object.entries(env ?? {}).map(([key, value]) => ({ key, value }));
}

function sameEnv(a: Record<string, string>, b: Record<string, string>): boolean {
  const ak = Object.keys(a);
  const bk = Object.keys(b);
  if (ak.length !== bk.length) return false;
  return ak.every((k) => b[k] === a[k]);
}

export type ProjectSettingsSectionId = 'general' | 'members' | 'integrations' | 'kanban' | 'models' | 'apikeys';

const SETTINGS_NAV: ReadonlyArray<{
  id: ProjectSettingsSectionId;
  labelKey: string;
  titleKey: string;
  descriptionKey: string;
  ownerOnly?: boolean;
}> = [
  {
    id: 'general',
    labelKey: 'projectSettings.navGeneral',
    titleKey: 'projectSettings.title',
    descriptionKey: 'projectSettings.subtitle',
  },
  {
    id: 'members',
    labelKey: 'projectSettings.navMembers',
    titleKey: 'projectSettings.membersTitle',
    descriptionKey: 'projectSettings.membersDesc',
  },
  {
    id: 'integrations',
    labelKey: 'projectSettings.navIntegrations',
    titleKey: 'projectSettings.navIntegrations',
    descriptionKey: 'projectSettings.integrationsDesc',
    ownerOnly: true,
  },
  {
    id: 'kanban',
    labelKey: 'projectSettings.navKanban',
    titleKey: 'projectSettings.kanbanTitle',
    descriptionKey: 'projectSettings.kanbanDesc',
    ownerOnly: true,
  },
  {
    id: 'models',
    labelKey: 'projectSettings.navModels',
    titleKey: 'projectSettings.navModels',
    descriptionKey: 'projectSettings.modelsDesc',
  },
  {
    id: 'apikeys',
    labelKey: 'projectSettings.navApiKeys',
    titleKey: 'projectSettings.apiKeysTitle',
    descriptionKey: 'projectSettings.apiKeysDesc',
    ownerOnly: true,
  },
];

function visibleSettingsSections(canManage: boolean) {
  return SETTINGS_NAV.filter(({ ownerOnly }) => canManage || !ownerOnly);
}

export function resolveProjectSettingsSection(
  value: string | null,
  canManage: boolean,
): ProjectSettingsSectionId {
  return visibleSettingsSections(canManage).some(({ id }) => id === value)
    ? value as ProjectSettingsSectionId
    : 'general';
}

export function ProjectSettingsSubnav({
  canManage,
  activeSection,
  onSelect,
}: {
  canManage: boolean;
  activeSection: ProjectSettingsSectionId;
  onSelect: (section: ProjectSettingsSectionId) => void;
}) {
  const { t } = useTranslation();
  return (
    <nav
      className={pageLayoutStyles.subnav}
      aria-label={t('projectSettings.navAria')}
      data-testid="project-settings-subnav"
    >
      {visibleSettingsSections(canManage).map(({ id, labelKey }) => (
        <button
          key={id}
          type="button"
          data-testid={`tab-${id}`}
          aria-current={activeSection === id ? 'page' : undefined}
          data-active={activeSection === id || undefined}
          onClick={() => onSelect(id)}
        >
          {t(labelKey)}
        </button>
      ))}
    </nav>
  );
}

/**
 * Route-owned Project administration. Like the Cluster routes, each settings
 * section owns one page while the shell owns the horizontal section navigation.
 * Repository/service controls intentionally remain in the Service settings tab.
 */
export function ProjectSettingsPage({
  project,
  onDeleted,
  activeSection = 'general',
}: {
  project: Project;
  onDeleted: () => void;
  activeSection?: ProjectSettingsSectionId;
}) {
  const { t } = useTranslation();
  const update = useUpdateProject();
  const del = useDeleteProject();
  const toast = useToast();
  const canManage = (project.role ?? 'owner') === 'owner';
  const section = resolveProjectSettingsSection(activeSection, canManage);
  const sectionMeta = SETTINGS_NAV.find(({ id }) => id === section) ?? SETTINGS_NAV[0]!;
  const [name, setName] = useState(project.name);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [maxConcurrent, setMaxConcurrent] = useState(
    project.max_concurrent_runs != null ? String(project.max_concurrent_runs) : '',
  );
  const [runTimeout, setRunTimeout] = useState(
    project.run_timeout_secs != null ? String(project.run_timeout_secs) : '',
  );
  const [envRows, setEnvRows] = useState<EnvRow[]>(envToRows(project.injected_env));
  const busy = update.isPending || del.isPending;

  const envError = (() => {
    for (const row of envRows) {
      const key = row.key.trim();
      if (!key) continue;
      if (!isValidEnvKey(key)) return t('projectSettings.envInvalidName', { key });
      if (isReservedEnvKey(key)) return t('projectSettings.envReserved', { key });
    }
    return '';
  })();

  const save = (event: React.FormEvent) => {
    event.preventDefault();
    if (envError) return;
    const input: UpdateProjectInput = {};
    const nextName = name.trim();
    if (nextName && nextName !== project.name) input.name = nextName;
    if (canManage) {
      const nextMax = parseGuardrail(maxConcurrent);
      const nextTimeout = parseGuardrail(runTimeout);
      const nextEnv = rowsToEnv(envRows);
      if (nextMax !== (project.max_concurrent_runs ?? null)) input.max_concurrent_runs = nextMax;
      if (nextTimeout !== (project.run_timeout_secs ?? null)) input.run_timeout_secs = nextTimeout;
      if (!sameEnv(nextEnv, project.injected_env ?? {})) input.injected_env = nextEnv;
    }
    if (Object.keys(input).length === 0) return;
    update.mutate(
      { id: project.id, input },
      {
        onSuccess: (updated) =>
          toast.push({ kind: 'success', message: t('projectSettings.projectUpdated', { name: updated.name }) }),
        onError: (error) =>
          toast.push({
            kind: 'error',
            message: error instanceof ApiError ? error.message : t('projectSettings.updateFailed'),
          }),
      },
    );
  };

  const remove = () => {
    del.mutate(project.id, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: t('projectSettings.projectDeleted', { name: project.name }) });
        onDeleted();
      },
      onError: (error) =>
        toast.push({
          kind: 'error',
          message: error instanceof ApiError ? error.message : t('projectSettings.deleteFailed'),
        }),
    });
  };

  return (
    <SurfaceInner className={styles.settingsPage}>
      <div data-testid="project-settings-page">
        <PageHeader
          eyebrow={t('projectSettings.eyebrow')}
          title={t(sectionMeta.titleKey)}
          description={t(sectionMeta.descriptionKey)}
        />
        <div className={styles.settingsDocument}>
          {section === 'general' && (
            <>
              <form id="project-settings-form" onSubmit={save} noValidate>
                <div className={styles.body}>
                  <TextField
                    label={t('projectSettings.projectName')}
                    placeholder="demo"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    hint={t('projectSettings.projectNameHintPage')}
                    data-testid="settings-name-input"
                    autoComplete="off"
                  />
                  {canManage && (
                    <section className={styles.guardrails} data-testid="guardrails">
                      <div className={styles.guardrailHead}>
                        <span className={styles.guardrailTitle}>
                          {t('projectSettings.executionGuardrails')}
                        </span>
                        <span className={styles.guardrailHint}>
                          {t('projectSettings.guardrailBlankHint')}
                        </span>
                      </div>
                      <div className={styles.guardrailGrid}>
                        <TextField
                          label={t('projectSettings.maxConcurrent')}
                          type="number"
                          min={1}
                          inputMode="numeric"
                          placeholder={t('projectSettings.clusterDefaultPlaceholder')}
                          value={maxConcurrent}
                          onChange={(event) => setMaxConcurrent(event.target.value)}
                          data-testid="settings-max-concurrent"
                          autoComplete="off"
                        />
                        <TextField
                          label={t('projectSettings.runTimeout')}
                          type="number"
                          min={1}
                          inputMode="numeric"
                          placeholder={t('projectSettings.clusterDefaultPlaceholder')}
                          value={runTimeout}
                          onChange={(event) => setRunTimeout(event.target.value)}
                          data-testid="settings-run-timeout"
                          autoComplete="off"
                        />
                      </div>
                      <div className={styles.envBlock} data-testid="settings-injected-env">
                        <div className={styles.guardrailHead}>
                          <span className={styles.guardrailTitle}>
                            {t('projectSettings.injectedEnv')}
                          </span>
                          <span className={styles.guardrailHint}>
                            {t('projectSettings.injectedEnvHintPage')}
                          </span>
                        </div>
                        {envRows.length > 0 && (
                          <div className={styles.envRows}>
                            {envRows.map((row, index) => {
                              const key = row.key.trim();
                              const invalid = key !== ''
                                && (!isValidEnvKey(key) || isReservedEnvKey(key));
                              return (
                                <div key={index} className={styles.envRow} data-testid="env-row">
                                  <input
                                    className={[styles.envInput, invalid && styles.envInvalid]
                                      .filter(Boolean)
                                      .join(' ')}
                                    placeholder="KEY"
                                    value={row.key}
                                    aria-invalid={invalid || undefined}
                                    onChange={(event) => setEnvRows((rows) => rows.map(
                                      (item, itemIndex) => itemIndex === index
                                        ? { ...item, key: event.target.value }
                                        : item,
                                    ))}
                                    data-testid={`env-key-${index}`}
                                    autoComplete="off"
                                  />
                                  <span className={styles.envEq}>=</span>
                                  <input
                                    className={styles.envInput}
                                    placeholder="value"
                                    value={row.value}
                                    onChange={(event) => setEnvRows((rows) => rows.map(
                                      (item, itemIndex) => itemIndex === index
                                        ? { ...item, value: event.target.value }
                                        : item,
                                    ))}
                                    data-testid={`env-value-${index}`}
                                    autoComplete="off"
                                  />
                                  <button
                                    type="button"
                                    className={styles.envRemove}
                                    onClick={() => setEnvRows((rows) => rows.filter(
                                      (_, itemIndex) => itemIndex !== index,
                                    ))}
                                    data-testid={`env-remove-${index}`}
                                    aria-label={t('projectSettings.removeVariable')}
                                  >
                                    <Trash size={15} weight="regular" aria-hidden="true" />
                                  </button>
                                </div>
                              );
                            })}
                          </div>
                        )}
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => setEnvRows((rows) => [...rows, { key: '', value: '' }])}
                          data-testid="env-add"
                        >
                          <Plus size={15} weight="regular" aria-hidden="true" />
                          <span>{t('projectSettings.addVariable')}</span>
                        </Button>
                        {envError && (
                          <span className={styles.envError} data-testid="env-error">{envError}</span>
                        )}
                      </div>
                    </section>
                  )}
                </div>
              </form>

              {canManage && (
                <SettingsSection
                  id="danger"
                  title={t('projectSettings.dangerTitle')}
                  description={t('projectSettings.dangerDesc')}
                >
                  <section className={styles.danger} data-testid="danger-zone">
                    <div className={styles.dangerText}>
                      <span className={styles.dangerTitle}>{t('projectSettings.deleteProject')}</span>
                      <span className={styles.dangerHint}>{t('projectSettings.deleteHintPage')}</span>
                    </div>
                    {confirmDelete ? (
                      <div className={styles.confirmRow} data-testid="delete-confirm">
                        <span className={styles.confirmLabel}>{t('projectSettings.deleteForGood')}</span>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => setConfirmDelete(false)}
                          disabled={del.isPending}
                        >
                          {t('projectSettings.keep')}
                        </Button>
                        <Button
                          type="button"
                          variant="danger"
                          size="sm"
                          loading={del.isPending}
                          onClick={remove}
                          data-testid="project-delete-confirm"
                        >
                          {t('projectSettings.deleteProject')}
                        </Button>
                      </div>
                    ) : (
                      <Button
                        type="button"
                        variant="danger"
                        size="sm"
                        onClick={() => setConfirmDelete(true)}
                        disabled={busy}
                        data-testid="project-delete"
                      >
                        {t('projectSettings.deleteProject')}
                      </Button>
                    )}
                  </section>
                </SettingsSection>
              )}

              <div className={styles.settingsSavebar}>
                <span>{envError || t('projectSettings.savebarHint')}</span>
                <div>
                  <Button
                    variant="primary"
                    type="submit"
                    form="project-settings-form"
                    loading={update.isPending}
                    disabled={!!envError}
                    data-testid="project-settings-save"
                  >
                    {t('projectSettings.saveChanges')}
                  </Button>
                </div>
              </div>
            </>
          )}

          {section === 'members' && <MembersPanel projectId={project.id} canManage={canManage} />}
          {section === 'integrations' && canManage && <IntegrationsPanel project={project} />}
          {section === 'kanban' && canManage && <KanbanPanel project={project} />}
          {section === 'models' && <ProjectModelsPanel projectId={project.id} canManage={canManage} />}
          {section === 'apikeys' && canManage && <ApiKeysPanel project={project} />}
        </div>
      </div>
    </SurfaceInner>
  );
}

function SettingsSection({
  id,
  title,
  description,
  children,
}: {
  id: string;
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section id={`project-settings-${id}`} className={styles.settingsSection}>
      <header>
        <h2>{title}</h2>
        <p>{description}</p>
      </header>
      <div className={styles.settingsSectionBody}>{children}</div>
    </section>
  );
}

export function ProjectSettingsModal({
  open,
  project,
  onClose,
  onDeleted,
}: {
  open: boolean;
  project: Project;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { t } = useTranslation();
  const update = useUpdateProject();
  const del = useDeleteProject();
  const toast = useToast();

  // Absent role (demo / legacy) is treated as owner (full affordances).
  const canManage = (project.role ?? 'owner') === 'owner';

  const [tab, setTab] = useState<Tab>('general');
  const [name, setName] = useState(project.name);
  const [confirmDelete, setConfirmDelete] = useState(false);

  // Guardrail form state (owner only). Numbers are kept as strings so empty means
  // "inherit the cluster default".
  const [maxConcurrent, setMaxConcurrent] = useState(
    project.max_concurrent_runs != null ? String(project.max_concurrent_runs) : '',
  );
  const [runTimeout, setRunTimeout] = useState(
    project.run_timeout_secs != null ? String(project.run_timeout_secs) : '',
  );
  const [envRows, setEnvRows] = useState<EnvRow[]>(envToRows(project.injected_env));

  const busy = update.isPending || del.isPending;

  // Front-end injected_env validation (mirrors the server's typed 400). The first
  // offending non-empty key wins; a truthy value blocks Save.
  const envError = (() => {
    for (const r of envRows) {
      const k = r.key.trim();
      if (!k) continue;
      if (!isValidEnvKey(k)) return t('projectSettings.envInvalidName', { key: k });
      if (isReservedEnvKey(k)) return t('projectSettings.envReserved', { key: k });
    }
    return '';
  })();

  const reset = () => {
    setName(project.name);
    setMaxConcurrent(project.max_concurrent_runs != null ? String(project.max_concurrent_runs) : '');
    setRunTimeout(project.run_timeout_secs != null ? String(project.run_timeout_secs) : '');
    setEnvRows(envToRows(project.injected_env));
    setConfirmDelete(false);
    setTab('general');
  };

  const close = () => {
    if (busy) return;
    reset();
    onClose();
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    if (envError) return; // blocked — the inline error explains why

    // Build a minimal PATCH: only include a field that actually changed, so a
    // rename-only save sends { name } and never disturbs the guardrails.
    const input: UpdateProjectInput = {};

    const nextName = name.trim();
    if (nextName && nextName !== project.name) input.name = nextName;

    if (canManage) {
      const nextMax = parseGuardrail(maxConcurrent);
      if (nextMax !== (project.max_concurrent_runs ?? null)) input.max_concurrent_runs = nextMax;

      const nextTimeout = parseGuardrail(runTimeout);
      if (nextTimeout !== (project.run_timeout_secs ?? null)) input.run_timeout_secs = nextTimeout;

      const nextEnv = rowsToEnv(envRows);
      if (!sameEnv(nextEnv, project.injected_env ?? {})) input.injected_env = nextEnv;
    }

    if (Object.keys(input).length === 0) {
      onClose();
      return;
    }

    update.mutate(
      { id: project.id, input },
      {
        onSuccess: (updated) => {
          toast.push({ kind: 'success', message: t('projectSettings.projectUpdated', { name: updated.name }) });
          onClose();
        },
        onError: (err) => {
          // The server's typed 400 (e.g. reserved_env_key) message is shown verbatim.
          const msg = err instanceof ApiError ? err.message : t('projectSettings.updateFailed');
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  const remove = () => {
    del.mutate(project.id, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: t('projectSettings.projectDeleted', { name: project.name }) });
        onDeleted();
      },
      onError: (err) => {
        const msg = err instanceof ApiError ? err.message : t('projectSettings.deleteFailed');
        toast.push({ kind: 'error', message: msg });
      },
    });
  };

  const footer =
    tab === 'general' ? (
      <>
        <Button variant="ghost" onClick={close} type="button">
          {t('common.cancel')}
        </Button>
        <Button
          variant="primary"
          type="submit"
          form="project-settings-form"
          loading={update.isPending}
          disabled={!!envError}
          data-testid="project-settings-save"
        >
          {t('projectSettings.saveChanges')}
        </Button>
      </>
    ) : (
      <Button variant="secondary" onClick={close} type="button" data-testid="settings-done">
        {t('common.done')}
      </Button>
    );

  return (
    <Modal
      open={open}
      onClose={close}
      title={t('projectSettings.modalTitle')}
      data-testid="project-settings-modal"
      footer={footer}
    >
      <div className={styles.tabs} role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'general'}
          className={styles.tab}
          data-active={tab === 'general' || undefined}
          onClick={() => setTab('general')}
          data-testid="tab-general"
        >
          {t('projectSettings.navGeneral')}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'members'}
          className={styles.tab}
          data-active={tab === 'members' || undefined}
          onClick={() => setTab('members')}
          data-testid="tab-members"
        >
          {t('projectSettings.navMembers')}
        </button>
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'integrations'}
            className={styles.tab}
            data-active={tab === 'integrations' || undefined}
            onClick={() => setTab('integrations')}
            data-testid="tab-integrations"
          >
            {t('projectSettings.tabBotIntegrations')}
          </button>
        )}
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'kanban'}
            className={styles.tab}
            data-active={tab === 'kanban' || undefined}
            onClick={() => setTab('kanban')}
            data-testid="tab-kanban"
          >
            {t('projectSettings.navKanban')}
          </button>
        )}
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'apikeys'}
            className={styles.tab}
            data-active={tab === 'apikeys' || undefined}
            onClick={() => setTab('apikeys')}
            data-testid="tab-apikeys"
          >
            {t('projectSettings.navApiKeys')}
          </button>
        )}
      </div>

      {tab === 'general' ? (
        <form id="project-settings-form" onSubmit={save} noValidate>
          <div className={styles.body}>
            <TextField
              label={t('projectSettings.name')}
              placeholder="demo"
              value={name}
              onChange={(e) => setName(e.target.value)}
              hint={t('projectSettings.nameHintModal')}
              data-testid="settings-name-input"
              autoComplete="off"
            />

            {canManage && (
              <section className={styles.guardrails} data-testid="guardrails">
                <div className={styles.guardrailHead}>
                  <span className={styles.guardrailTitle}>{t('projectSettings.guardrails')}</span>
                  <span className={styles.guardrailHint}>
                    {t('projectSettings.guardrailBlankHint')}
                  </span>
                </div>

                <div className={styles.guardrailGrid}>
                  <TextField
                    label={t('projectSettings.maxConcurrent')}
                    type="number"
                    min={1}
                    inputMode="numeric"
                    placeholder={t('projectSettings.clusterDefaultPlaceholder')}
                    value={maxConcurrent}
                    onChange={(e) => setMaxConcurrent(e.target.value)}
                    data-testid="settings-max-concurrent"
                    autoComplete="off"
                  />
                  <TextField
                    label={t('projectSettings.runTimeout')}
                    type="number"
                    min={1}
                    inputMode="numeric"
                    placeholder={t('projectSettings.clusterDefaultPlaceholder')}
                    value={runTimeout}
                    onChange={(e) => setRunTimeout(e.target.value)}
                    data-testid="settings-run-timeout"
                    autoComplete="off"
                  />
                </div>

                <div className={styles.envBlock} data-testid="settings-injected-env">
                  <div className={styles.guardrailHead}>
                    <span className={styles.guardrailTitle}>{t('projectSettings.injectedEnv')}</span>
                    <span className={styles.guardrailHint}>
                      {t('projectSettings.injectedEnvHintModal')}
                    </span>
                  </div>
                  {envRows.length > 0 && (
                    <div className={styles.envRows}>
                      {envRows.map((row, i) => {
                        const k = row.key.trim();
                        const rowError =
                          k !== '' && (!isValidEnvKey(k) || isReservedEnvKey(k));
                        return (
                          <div key={i} className={styles.envRow} data-testid="env-row">
                            <input
                              className={[styles.envInput, rowError && styles.envInvalid]
                                .filter(Boolean)
                                .join(' ')}
                              placeholder="KEY"
                              value={row.key}
                              aria-invalid={rowError || undefined}
                              onChange={(e) =>
                                setEnvRows((rows) =>
                                  rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)),
                                )
                              }
                              data-testid={`env-key-${i}`}
                              autoComplete="off"
                            />
                            <span className={styles.envEq}>=</span>
                            <input
                              className={styles.envInput}
                              placeholder="value"
                              value={row.value}
                              onChange={(e) =>
                                setEnvRows((rows) =>
                                  rows.map((r, j) =>
                                    j === i ? { ...r, value: e.target.value } : r,
                                  ),
                                )
                              }
                              data-testid={`env-value-${i}`}
                              autoComplete="off"
                            />
                            <button
                              type="button"
                              className={styles.envRemove}
                              onClick={() => setEnvRows((rows) => rows.filter((_, j) => j !== i))}
                              data-testid={`env-remove-${i}`}
                              aria-label={t('projectSettings.removeVariable')}
                            >
                              <Trash size={15} weight="regular" aria-hidden="true" />
                            </button>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => setEnvRows((rows) => [...rows, { key: '', value: '' }])}
                    data-testid="env-add"
                  >
                    <Plus size={15} weight="regular" aria-hidden="true" />
                    <span>{t('projectSettings.addVariable')}</span>
                  </Button>
                  {envError && (
                    <span className={styles.envError} data-testid="env-error">
                      {envError}
                    </span>
                  )}
                </div>
              </section>
            )}

            <section className={styles.danger} data-testid="danger-zone">
              <div className={styles.dangerText}>
                <span className={styles.dangerTitle}>{t('projectSettings.deleteProject')}</span>
                <span className={styles.dangerHint}>
                  {t('projectSettings.deleteHintModal')}
                </span>
              </div>
              {confirmDelete ? (
                <div className={styles.confirmRow} data-testid="delete-confirm">
                  <span className={styles.confirmLabel}>{t('projectSettings.deleteForGood')}</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => setConfirmDelete(false)}
                    disabled={del.isPending}
                  >
                    {t('projectSettings.keep')}
                  </Button>
                  <Button
                    type="button"
                    variant="danger"
                    size="sm"
                    loading={del.isPending}
                    onClick={remove}
                    data-testid="project-delete-confirm"
                  >
                    {t('projectSettings.deleteProject')}
                  </Button>
                </div>
              ) : (
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  onClick={() => setConfirmDelete(true)}
                  disabled={busy}
                  data-testid="project-delete"
                >
                  {t('projectSettings.deleteProject')}
                </Button>
              )}
            </section>
          </div>
        </form>
      ) : tab === 'members' ? (
        <MembersPanel projectId={project.id} canManage={canManage} />
      ) : tab === 'integrations' ? (
        <IntegrationsPanel project={project} />
      ) : tab === 'kanban' ? (
        <KanbanPanel project={project} />
      ) : (
        <ApiKeysPanel project={project} />
      )}
    </Modal>
  );
}

/**
 * KanbanPanel — the project owner's jtype Kanban integration (D37 + approved
 * kanban-link-flow design). ONE card with three quiet sections:
 *
 *   1. 集成登录 — the jtype credential. Connected (a link carries a per-link
 *      token, or a project-surface connect just minted a sealed blob held in
 *      memory), not connected (the device-flow panel; paste-a-PAT stays as the
 *      manual fallback), or expired (90-day device token lapsed → reconnect).
 *   2. 看板设置 — visible only while the project has NO link (product model:
 *      one board per project). Service comes from the project; workspace /
 *      board / columns are ALL listed by jtype through the server proxy and
 *      picked, never typed (manual entry stays as the escape hatch).
 *   3. 当前看板 — the project's existing link(s) with a fail-visible status
 *      (生效中 / 无凭据 / 已过期) and actions: open the embedded board,
 *      connect/reconnect, remove.
 */
function KanbanPanel({ project }: { project: Project }) {
  const { t } = useTranslation();
  const toast = useToast();
  const system = useSystem();
  const links = useProjectKanbanLinks(project.id);
  const boardLinks = useProjectBoardLinks(project.id, (links.data ?? []).length > 0);
  const create = useCreateProjectKanbanLink(project.id);
  const del = useDeleteProjectKanbanLink(project.id);
  const updateToken = useUpdateProjectKanbanLinkToken(project.id);
  const services = project.services ?? [];
  // Strictly false (an absent kanban block ⇒ don't block, we can't tell).
  const kanbanOff = system.data?.kanban?.enabled === false;

  const [serviceId, setServiceId] = useState('');
  const [workspaceId, setWorkspaceId] = useState('');
  const [boardRef, setBoardRef] = useState('');
  const [triggerCol, setTriggerCol] = useState('');
  const [doneCol, setDoneCol] = useState('');
  const [token, setToken] = useState('');
  // Paste-a-PAT is the manual credential fallback inside the login section.
  const [pasteMode, setPasteMode] = useState(false);
  // Manual entry is the escape hatch for an unlistable workspace (D29): the
  // server resolves a typed board ref exactly like a picked one.
  const [manual, setManual] = useState(false);
  const [discoveryError, setDiscoveryError] = useState('');
  const [confirmRemove, setConfirmRemove] = useState<string | undefined>();
  const [boardOpen, setBoardOpen] = useState(false);

  // ---- connection state -----------------------------------------------------
  const linkList = links.data ?? [];
  const tokenedLink = linkList.find((l) => l.token_set);
  const expiredLink = linkList.find(
    (l) => l.token_expires_at && new Date(l.token_expires_at).getTime() < Date.now(),
  );

  // D37: a project-surface "Connect with jtype" flow mints a token WITHOUT an
  // existing link and returns the SEALED blob — held in memory only (never
  // plaintext), passed to discovery as X-Jtype-Token-Enc and submitted back
  // with the save. Dropped from memory once the board settings are saved.
  const [pendingTokenEnc, setPendingTokenEnc] = useState<string | undefined>();
  const [pendingTokenExpiry, setPendingTokenExpiry] = useState<string | undefined>();
  const startConnect = useStartProjectConnect(project.id);
  const [connectId, setConnectId] = useState<string | undefined>();
  const connectStatus = useProjectConnectStatus(project.id, connectId, !!connectId);
  const connectComplete = connectStatus.data?.status === 'complete';
  useEffect(() => {
    if (!connectComplete) return;
    const enc = connectStatus.data?.token_enc;
    if (enc) {
      setPendingTokenEnc(enc);
      setPendingTokenExpiry(connectStatus.data?.token_expires_at);
      setConnectId(undefined);
      setDiscoveryError('');
      setPasteMode(false);
    }
  }, [connectComplete, connectStatus.data]);

  const hasCredential = !!tokenedLink || !!pendingTokenEnc;
  const connected = hasCredential;

  // Card-head status: fail-visible, never a bare "off".
  const headStatus = kanbanOff
    ? { tone: 'warning' as const, label: t('projectSettings.kanbanNotEnabled') }
    : expiredLink && !tokenedLink
      ? { tone: 'danger' as const, label: t('projectSettings.expiredReconnect') }
      : tokenedLink
        ? {
            tone: 'success' as const,
            label:
              t('projectSettings.connectedLabel') +
              (expiryLabel(tokenedLink.token_expires_at, t('projectSettings.expiredReconnect'))
                ? ` · ${expiryLabel(tokenedLink.token_expires_at, t('projectSettings.expiredReconnect'))}`
                : ''),
          }
        : pendingTokenEnc
          ? { tone: 'success' as const, label: t('projectSettings.connectedPendingSave') }
          : { tone: 'warning' as const, label: t('projectSettings.notConnectedYet') };

  // ---- discovery pickers ----------------------------------------------------
  const pickerActive = !kanbanOff && !manual && hasCredential && linkList.length === 0;
  const workspaces = useJtypeWorkspaces(project.id, pickerActive, pendingTokenEnc);
  const boards = useJtypeBoards(project.id, workspaceId, pickerActive && !!workspaceId, pendingTokenEnc);
  const boardList = boards.data ?? [];
  const selectedBoard = boardList.find((b) => b.ref === boardRef);
  const columnOptions = (selectedBoard?.columns ?? []).map((c) => ({ value: c.key, label: c.name }));

  // Fail-visible fallback: data-shape/discovery failures may use manual entry,
  // but an invalid credential cannot be fixed by typing IDs. Keep the pickers
  // visible and ask the owner to reconnect instead of presenting unusable
  // manual fields.
  useEffect(() => {
    if (manual) return;
    const wsErr = workspaces.isError && !workspaces.isFetching;
    const boardErr = boards.isError && !boards.isFetching;
    if (!wsErr && !boardErr) return;
    const err = wsErr ? workspaces.error : boards.error;
    const code = err instanceof ApiError ? apiErrorCode(err) : undefined;
    if (code !== 'jtype_unauthorized' && code !== 'bad_token_enc') {
      setManual(true);
    }
    setDiscoveryError(
      err instanceof ApiError
        ? err.message
        : t('projectSettings.jtypeDiscoveryFailed'),
    );
  }, [
    manual,
    workspaces.isError,
    workspaces.isFetching,
    workspaces.error,
    boards.isError,
    boards.isFetching,
    boards.error,
    t,
  ]);

  const pickWorkspace = (id: string) => {
    setWorkspaceId(id);
    setBoardRef('');
    setTriggerCol('');
    setDoneCol('');
  };
  const pickBoard = (ref: string) => {
    setBoardRef(ref);
    setTriggerCol('');
    setDoneCol('');
  };

  const incomplete =
    !serviceId || !workspaceId.trim() || !boardRef.trim() || !triggerCol.trim();

  // ---- save board settings (creates THE one link) ---------------------------
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const typedToken = token.trim();
    create.mutate(
      {
        workspace_id: workspaceId.trim(),
        board_ref: boardRef.trim(),
        service_id: serviceId,
        trigger_column: triggerCol.trim(),
        done_column: doneCol.trim() || undefined,
        // Exactly one credential source: a pasted PAT wins; otherwise the
        // sealed blob from the project-surface connect rides along (D37).
        token: typedToken || undefined,
        token_enc: !typedToken && pendingTokenEnc ? pendingTokenEnc : undefined,
        token_expires_at: !typedToken && pendingTokenEnc ? pendingTokenExpiry : undefined,
      },
      {
        onSuccess: () => {
          setServiceId('');
          setWorkspaceId('');
          setBoardRef('');
          setTriggerCol('');
          setDoneCol('');
          setToken('');
          setPasteMode(false);
          setPendingTokenEnc(undefined);
          setPendingTokenExpiry(undefined);
          toast.push({ kind: 'success', message: t('projectSettings.kanbanLinkAdded') });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('projectSettings.kanbanLinkAddFailed'),
          }),
      },
    );
  };

  const removeLink = (id: string) => {
    del.mutate(id, {
      onSuccess: () => {
        setConfirmRemove(undefined);
        toast.push({ kind: 'success', message: t('projectSettings.kanbanLinkRemoved') });
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : t('projectSettings.kanbanLinkRemoveFailed'),
        }),
    });
  };

  // Disconnect the project's credential: clear the tokened link's token
  // (credential_status → missing, fail-visible) or drop the in-memory blob.
  const disconnect = () => {
    if (pendingTokenEnc) {
      setPendingTokenEnc(undefined);
      setPendingTokenExpiry(undefined);
      return;
    }
    if (tokenedLink) {
      updateToken.mutate(
        { linkId: tokenedLink.id, token: '' },
        {
          onSuccess: () => toast.push({ kind: 'success', message: t('projectSettings.tokenCleared') }),
          onError: (err) =>
            toast.push({
              kind: 'error',
              message: err instanceof ApiError ? err.message : t('projectSettings.tokenUpdateFailed'),
            }),
        },
      );
    }
  };

  return (
    <div className={styles.body} data-testid="kanban-panel">
      <p className={styles.guardrailHint}>{t('projectSettings.kanbanIntro')}</p>

      {kanbanOff && (
        <p className={styles.kanbanError} data-testid="kanban-disabled">
          {t('projectSettings.kanbanDisabled')}
        </p>
      )}

      <section className={styles.integrationCard} data-testid="kanban-integration">
        <header className={styles.integrationHead}>
          <span className={styles.integrationTitle}>
            <strong>{t('projectSettings.kanbanIntegrationTitle')}</strong>
            <small>{t('projectSettings.kanbanIntegrationSub')}</small>
          </span>
          <span
            className={styles.badge}
            data-state={headStatus.tone === 'success' ? 'per_link' : headStatus.tone === 'danger' ? 'missing' : 'unvalidated'}
            data-testid="kanban-head-status"
          >
            {headStatus.label}
          </span>
        </header>

        {/* ---- 集成登录 -------------------------------------------------- */}
        <div className={styles.integrationSection} data-testid="kanban-login-section">
          {connected ? (
            <div className={styles.boardRow} data-testid="kanban-connected-identity">
              <div className={styles.kanbanMeta}>
                <div className={styles.kanbanTitle}>
                  {tokenedLink
                    ? tokenedLink.board_title || `${tokenedLink.workspace_id} / ${tokenedLink.board_ref}`
                    : t('projectSettings.connectedPendingSave')}
                </div>
                <div className={styles.kanbanSub}>{t('projectSettings.sealedTokenNote')}</div>
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={disconnect}
                  loading={updateToken.isPending}
                  data-testid="kanban-disconnect"
                >
                  {t('projectSettings.disconnect')}
                </Button>
              </div>
            </div>
          ) : pasteMode ? (
            <div data-testid="kanban-paste-token">
              <TextField
                label={t('projectSettings.jtypeTokenOptional')}
                type="password"
                placeholder={t('projectSettings.tokenFallbackPlaceholder')}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                disabled={kanbanOff}
                data-testid="kanban-link-token"
                autoComplete="off"
                hint={t('projectSettings.tokenStoredHint')}
              />
              <div className={styles.kanbanFormActions}>
                <Button type="button" variant="ghost" size="sm" onClick={() => setPasteMode(false)}>
                  {t('common.cancel')}
                </Button>
              </div>
            </div>
          ) : (
            <div data-testid="kanban-project-connect-panel">
              <p className={styles.guardrailHint}>{t('projectSettings.connectFirstHint')}</p>
              <KanbanConnectFlow
                idPrefix="kanban-project-connect"
                disabled={kanbanOff}
                disabledHint={t('projectSettings.enableJtypeHint')}
                active={!!connectId}
                starting={startConnect.isPending}
                startError={startConnect.error}
                connectStart={startConnect.data}
                status={connectStatus.data}
                statusError={connectStatus.error}
                onStart={() => startConnect.mutate(undefined, { onSuccess: (s) => setConnectId(s.connect_id) })}
                onReset={() => {
                  setConnectId(undefined);
                  startConnect.reset();
                }}
              />
              <div className={styles.kanbanFormActions}>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setPasteMode(true)}
                  disabled={kanbanOff}
                  data-testid="kanban-paste-instead"
                >
                  {t('projectSettings.pasteTokenInstead')}
                </Button>
              </div>
            </div>
          )}
        </div>

        {/* ---- 看板设置(仅未设置看板时;产品模型:一个项目一个看板)--------- */}
        {linkList.length === 0 && (
          <form
            className={styles.integrationSection}
            data-dimmed={!hasCredential || kanbanOff || undefined}
            onSubmit={submit}
            noValidate
            data-testid="kanban-link-form"
          >
            <span className={styles.integrationSectionTitle}>
              {hasCredential
                ? t('projectSettings.boardSettingsHint')
                : t('projectSettings.boardSettingsLockedHint')}
            </span>
            {!kanbanOff && hasCredential && (
              <div className={styles.kanbanModeRow}>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setManual((m) => {
                      const next = !m;
                      if (!next) {
                        void workspaces.refetch();
                        if (workspaceId) void boards.refetch();
                      }
                      return next;
                    });
                    setDiscoveryError('');
                  }}
                  data-testid="kanban-link-manual-toggle"
                >
                  {manual ? t('projectSettings.usePickers') : t('projectSettings.enterManually')}
                </Button>
              </div>
            )}
            {discoveryError && (
              <p className={styles.kanbanError} data-testid="kanban-link-discovery-error">
                {discoveryError}
              </p>
            )}

            <div className={styles.boardGrid}>
              <SelectField
                label={t('projectSettings.service')}
                required
                value={serviceId}
                onChange={setServiceId}
                disabled={kanbanOff || !hasCredential}
                data-testid="kanban-link-service"
                placeholder={t('projectSettings.selectService')}
                options={services.map((s) => ({ value: s.id, label: s.name }))}
              />
              {manual ? (
                <TextField
                  label={t('projectSettings.jtypeWorkspaceId')}
                  placeholder="f006b727-…"
                  value={workspaceId}
                  onChange={(e) => setWorkspaceId(e.target.value)}
                  required
                  disabled={kanbanOff || !hasCredential}
                  data-testid="kanban-link-workspace"
                  autoComplete="off"
                />
              ) : (
                <SelectField
                  label={t('projectSettings.jtypeWorkspace')}
                  required
                  value={workspaceId}
                  onChange={pickWorkspace}
                  disabled={kanbanOff || !hasCredential || workspaces.isLoading}
                  data-testid="kanban-link-workspace-select"
                  placeholder={workspaces.isLoading ? t('projectSettings.loadingWorkspaces') : t('projectSettings.selectWorkspace')}
                  options={(workspaces.data ?? []).map((w) => ({ value: w.id, label: w.name }))}
                />
              )}
              {manual ? (
                <TextField
                  label={t('projectSettings.boardRef')}
                  placeholder="jtype.board"
                  value={boardRef}
                  onChange={(e) => setBoardRef(e.target.value)}
                  required
                  disabled={kanbanOff || !hasCredential}
                  data-testid="kanban-link-board"
                  autoComplete="off"
                  hint={t('projectSettings.boardRefHint')}
                />
              ) : (
                <SelectField
                  label={t('projectSettings.board')}
                  required
                  value={boardRef}
                  onChange={pickBoard}
                  disabled={kanbanOff || !hasCredential || !workspaceId || boards.isLoading}
                  data-testid="kanban-link-board-select"
                  placeholder={
                    !workspaceId
                      ? t('projectSettings.pickWorkspaceFirst')
                      : boards.isLoading
                        ? t('projectSettings.loadingBoards')
                        : t('projectSettings.selectBoard')
                  }
                  options={boardList.map((b) => ({ value: b.ref, label: b.title }))}
                />
              )}
              {manual ? (
                <div className={styles.boardPairCell}>
                  <TextField
                    label={t('projectSettings.triggerColumn')}
                    placeholder="ai"
                    value={triggerCol}
                    onChange={(e) => setTriggerCol(e.target.value)}
                    required
                    disabled={kanbanOff || !hasCredential}
                    data-testid="kanban-link-trigger"
                    autoComplete="off"
                  />
                  <TextField
                    label={t('projectSettings.doneColumnOptional')}
                    placeholder="done"
                    value={doneCol}
                    onChange={(e) => setDoneCol(e.target.value)}
                    disabled={kanbanOff || !hasCredential}
                    data-testid="kanban-link-done"
                    autoComplete="off"
                  />
                </div>
              ) : (
                <div className={styles.boardPairCell}>
                  <SelectField
                    label={t('projectSettings.triggerColumn')}
                    required
                    value={triggerCol}
                    onChange={setTriggerCol}
                    disabled={kanbanOff || !hasCredential || !boardRef}
                    data-testid="kanban-link-trigger-select"
                    placeholder={boardRef ? t('projectSettings.selectColumn') : t('projectSettings.pickBoardFirst')}
                    options={columnOptions}
                  />
                  <SelectField
                    label={t('projectSettings.doneColumnOptional')}
                    value={doneCol}
                    onChange={setDoneCol}
                    disabled={kanbanOff || !hasCredential || !boardRef}
                    data-testid="kanban-link-done-select"
                    placeholder={boardRef ? t('projectSettings.noneOption') : t('projectSettings.pickBoardFirst')}
                    options={[{ value: '', label: t('projectSettings.noneOption') }, ...columnOptions]}
                  />
                </div>
              )}
            </div>

            <div className={styles.kanbanFormActions}>
              <Button
                type="submit"
                variant="primary"
                loading={create.isPending}
                disabled={kanbanOff || !hasCredential || incomplete}
                data-testid="kanban-link-add"
              >
                {t('projectSettings.saveBoardSettings')}
              </Button>
            </div>
          </form>
        )}

        {/* ---- 当前看板 --------------------------------------------------- */}
        {linkList.length > 0 && (
          <div className={styles.integrationSection} data-testid="kanban-current-board">
            <span className={styles.integrationSectionTitle}>{t('projectSettings.currentBoardTitle')}</span>
            {linkList.map((l) => (
              <CurrentBoardRow
                key={l.id}
                projectId={project.id}
                link={l}
                serviceName={services.find((s) => s.id === l.service_id)?.name ?? l.service_id}
                kanbanOff={kanbanOff}
                confirming={confirmRemove === l.id}
                deleting={del.isPending}
                onAskRemove={() => setConfirmRemove(l.id)}
                onCancelRemove={() => setConfirmRemove(undefined)}
                onConfirmRemove={() => removeLink(l.id)}
                onOpenBoard={() => setBoardOpen(true)}
              />
            ))}
          </div>
        )}
      </section>

      {boardOpen && boardLinks.data && boardLinks.data.length > 0 && (
        <KanbanBoardModal
          projectId={project.id}
          links={boardLinks.data}
          onClose={() => setBoardOpen(false)}
        />
      )}
    </div>
  );
}

/**
 * CurrentBoardRow — the project's current board binding (one per project by
 * product model; legacy rows all render). Status is fail-visible: 生效中 /
 * 无凭据 / 已过期. Actions: open the embedded board, connect/reconnect the
 * per-link token (D28 device flow), remove behind a confirm step.
 */
function CurrentBoardRow({
  projectId,
  link,
  serviceName,
  kanbanOff,
  confirming,
  deleting,
  onAskRemove,
  onCancelRemove,
  onConfirmRemove,
  onOpenBoard,
}: {
  projectId: string;
  link: KanbanLink;
  serviceName: string;
  kanbanOff: boolean;
  confirming: boolean;
  deleting: boolean;
  onAskRemove: () => void;
  onCancelRemove: () => void;
  onConfirmRemove: () => void;
  onOpenBoard: () => void;
}) {
  const { t } = useTranslation();
  // D28: per-link "Connect with jtype" device flow — the link already exists,
  // so connect seals a per-link token onto it (credential_status → per_link).
  const startConnect = useStartLinkConnect(projectId);
  const [connectId, setConnectId] = useState<string | undefined>();
  const connectStatus = useLinkConnectStatus(projectId, link.id, connectId, !!connectId);
  const launchConnect = () =>
    startConnect.mutate(link.id, { onSuccess: (s) => setConnectId(s.connect_id) });
  const resetConnect = () => {
    setConnectId(undefined);
    startConnect.reset();
  };

  const boardLabel = link.board_title || `${link.workspace_id} / ${link.board_ref}`;
  const expired =
    !!link.token_expires_at && new Date(link.token_expires_at).getTime() < Date.now();
  const boardStatus = link.board_status ?? 'ok';
  const needsConnect = link.credential_status === 'missing' || expired;
  const statusLabel = expired
    ? t('projectSettings.expiredReconnect')
    : link.credential_status === 'per_link'
      ? t('projectSettings.boardActive')
      : t('projectSettings.credMissing');

  return (
    <div data-testid={`kanban-link-${link.id}`}>
      <div className={styles.boardRow}>
        <div className={styles.kanbanMeta}>
          <div className={styles.kanbanTitle}>
            <span title={`${link.workspace_id} / ${link.board_ref}`}>{boardLabel}</span>
            <span
              className={styles.badge}
              data-state={needsConnect ? 'missing' : 'per_link'}
              data-testid={`kanban-cred-${link.id}`}
            >
              {statusLabel}
            </span>
            {boardStatus !== 'ok' && (
              <span
                className={styles.badge}
                data-state={boardStatus === 'invalid' ? 'invalid' : 'unvalidated'}
                data-testid={`kanban-board-status-${link.id}`}
              >
                {boardStatus === 'invalid' ? t('projectSettings.boardColumnsInvalid') : t('projectSettings.columnsNotValidated')}
              </span>
            )}
          </div>
          <div className={styles.kanbanSub}>
            {serviceName} · {link.trigger_column}
            {link.done_column ? ` → ${link.done_column}` : ''}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {!needsConnect && (
            <Button type="button" variant="ghost" size="sm" onClick={onOpenBoard} data-testid={`kanban-open-board-${link.id}`}>
              {t('projectSettings.openBoard')}
            </Button>
          )}
          {confirming ? (
            <>
              <Button type="button" variant="ghost" size="sm" onClick={onCancelRemove} disabled={deleting}>
                {t('common.cancel')}
              </Button>
              <Button
                type="button"
                variant="danger"
                size="sm"
                onClick={onConfirmRemove}
                loading={deleting}
                data-testid={`kanban-link-delete-confirm-${link.id}`}
              >
                {t('projectSettings.removeBoardSetting')}
              </Button>
            </>
          ) : (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onAskRemove}
              data-testid={`kanban-link-delete-${link.id}`}
            >
              {t('projectSettings.removeBoardSetting')}
            </Button>
          )}
        </div>
      </div>
      {boardStatus === 'unvalidated' && (
        <p className={styles.kanbanBoardNotice} data-testid={`kanban-board-notice-${link.id}`}>
          {t('projectSettings.boardUnvalidatedNotice')}
        </p>
      )}
      {boardStatus === 'invalid' && (
        <p className={styles.kanbanError} role="alert" data-testid={`kanban-board-notice-${link.id}`}>
          {t('projectSettings.boardInvalidNotice')}
        </p>
      )}
      {needsConnect && (
        <div className={styles.kanbanConnect}>
          <KanbanConnectFlow
            idPrefix={`kanban-link-connect-${link.id}`}
            disabled={kanbanOff}
            disabledHint={t('projectSettings.enableJtypeHint')}
            active={!!connectId}
            starting={startConnect.isPending}
            startError={startConnect.error}
            connectStart={startConnect.data}
            status={connectStatus.data}
            statusError={connectStatus.error}
            onStart={launchConnect}
            onReset={resetConnect}
          />
        </div>
      )}
    </div>
  );
}

/**
 * ApiKeysPanel — the project owner's API keys (F12 / D24). A key is a
 * revocable, project-scoped automation credential (`Authorization: Bearer
 * <key>`, capped at the Member role on THIS project only) meant to replace
 * borrowing the cluster-wide console token for external/CI use. The plaintext
 * is shown ONCE, right after creation — there is no read-back endpoint, so the
 * reveal card below is the only chance to copy it.
 */
function ApiKeysPanel({ project }: { project: Project }) {
  const { t } = useTranslation();
  const toast = useToast();
  const keys = useApiKeys(project.id);
  const create = useCreateApiKey(project.id);
  const revoke = useRevokeApiKey(project.id);

  const [name, setName] = useState('');
  const [revealed, setRevealed] = useState<CreateApiKeyResponse | null>(null);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name: name.trim() },
      {
        onSuccess: (created) => {
          setName('');
          setRevealed(created);
          toast.push({ kind: 'success', message: t('projectSettings.apiKeyCreated', { name: created.name }) });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('projectSettings.apiKeyCreateFailed'),
          }),
      },
    );
  };

  const doRevoke = (id: string) => {
    revoke.mutate(id, {
      onSuccess: () => toast.push({ kind: 'success', message: t('projectSettings.apiKeyRevoked') }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : t('projectSettings.apiKeyRevokeFailed'),
        }),
    });
  };

  return (
    <div className={styles.body} data-testid="apikeys-panel">
      <p className={styles.guardrailHint}>
        {t('projectSettings.apiKeyIntro1')}<code>Authorization: Bearer &lt;key&gt;</code>{t('projectSettings.apiKeyIntro2')}
      </p>

      {revealed && (
        <ApiKeyReveal created={revealed} onDismiss={() => setRevealed(null)} />
      )}

      {keys.data && keys.data.length > 0 ? (
        <div className={styles.kanbanList} data-testid="apikeys-list">
          {keys.data.map((k) => (
            <ApiKeyRow
              key={k.id}
              apiKey={k}
              revoking={revoke.isPending}
              onRevoke={() => doRevoke(k.id)}
            />
          ))}
        </div>
      ) : (
        <p className={styles.guardrailHint} data-testid="apikeys-empty">
          {t('projectSettings.apiKeysEmpty')}
        </p>
      )}

      <form className={styles.kanbanForm} onSubmit={submit} noValidate data-testid="apikey-form">
        <TextField
          label={t('projectSettings.name')}
          placeholder="ci-bot"
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          data-testid="apikey-name"
          autoComplete="off"
          hint={t('projectSettings.apiKeyNameHint')}
        />
        <div className={styles.kanbanFormActions}>
          <Button type="submit" variant="primary" loading={create.isPending} data-testid="apikey-create">
            {t('projectSettings.createKey')}
          </Button>
        </div>
      </form>
    </div>
  );
}

/**
 * ApiKeyReveal — the one-time plaintext display right after creation. There is
 * no read-back endpoint, so this card (plus its copy button) is the only
 * chance the owner gets to grab the key; dismissing it is a UI-only action
 * (the key keeps working — dismissing does NOT revoke it).
 */
function ApiKeyReveal({
  created,
  onDismiss,
}: {
  created: CreateApiKeyResponse;
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(created.key);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — the text is still selectable */
    }
  };
  return (
    <section className={styles.apiKeyReveal} data-testid="apikey-reveal">
      <div className={styles.guardrailHead}>
        <span className={styles.guardrailTitle}>{t('projectSettings.apiKeyRevealTitle', { name: created.name })}</span>
        <span className={styles.guardrailHint}>
          {t('projectSettings.apiKeyRevealHint')}
        </span>
      </div>
      <div className={styles.apiKeyRevealRow}>
        <code className={styles.apiKeyRevealCode} data-testid="apikey-reveal-value">
          {created.key}
        </code>
        <Button type="button" variant="secondary" size="sm" onClick={copy} data-testid="apikey-reveal-copy">
          {copied ? t('common.copied') : t('common.copy')}
        </Button>
      </div>
      <div className={styles.kanbanFormActions}>
        <Button type="button" variant="ghost" size="sm" onClick={onDismiss} data-testid="apikey-reveal-dismiss">
          {t('common.done')}
        </Button>
      </div>
    </section>
  );
}

/**
 * ApiKeyRow — one API key: name, status badge (active/revoked), prefix,
 * created/last-used, and Revoke (hidden once already revoked).
 */
function ApiKeyRow({
  apiKey,
  revoking,
  onRevoke,
}: {
  apiKey: ApiKey;
  revoking: boolean;
  onRevoke: () => void;
}) {
  const { t } = useTranslation();
  const revoked = !!apiKey.revoked_at;
  return (
    <div className={styles.kanbanRow} data-testid={`apikey-${apiKey.id}`}>
      <div className={styles.kanbanMeta}>
        <div className={styles.kanbanTitle}>
          {apiKey.name}
          <span
            className={styles.badge}
            data-state={revoked ? 'missing' : 'per_link'}
            data-testid={`apikey-status-${apiKey.id}`}
          >
            {revoked ? t('projectSettings.statusRevoked') : t('projectSettings.statusActive')}
          </span>
          <code className={styles.repoField}>{apiKey.prefix}…</code>
        </div>
        <div className={styles.kanbanSub}>
          {t('projectSettings.createdAt', { time: timeAgo(apiKey.created_at) })}
          {apiKey.last_used_at ? t('projectSettings.lastUsed', { time: timeAgo(apiKey.last_used_at) }) : t('projectSettings.neverUsed')}
        </div>
      </div>
      {!revoked && (
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={revoking}
          onClick={onRevoke}
          data-testid={`apikey-revoke-${apiKey.id}`}
        >
          {t('projectSettings.revoke')}
        </Button>
      )}
    </div>
  );
}
