/*
 * ProjectSettingsPage — route-owned Project administration (blueprint §2/§5).
 * Sections:
 *   - General: project rename, the guardrails editor (owner only — concurrency
 *     cap, run timeout, injected env), and a Delete-project action behind a
 *     confirm step. Repo config (branch / git mode) lives on each repository on
 *     the project page — a project is a pure container.
 *   - Members: roster with role management + add-by-search (MembersPanel).
 *   - Plugins: provider credentials, repository bindings, and JType Kanban
 *     resources are managed through the unified Project Plugin surface.
 *   - Model access: models granted to this project (members can view).
 *   - API keys (F12 / D24): project-scoped, revocable automation credentials
 *     (owner) — replaces borrowing CONSOLE_TOKEN for external/CI use.
 */
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash } from '@phosphor-icons/react';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { MembersPanel } from './MembersPanel';
import { ProjectPluginsPanel } from './ProjectPluginsPanel';
import { ProjectModelsPanel } from './models/ProjectModelsPanel';
import {
  useUpdateProject,
  useDeleteProject,
  useApiKeys,
  useCreateApiKey,
  useRevokeApiKey,
} from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';
import { timeAgo } from '../lib/format';
import { PageHeader, SurfaceInner, pageLayoutStyles } from '../components/PageLayout';
import type {
  ApiKey,
  CreateApiKeyResponse,
  Project,
  UpdateProjectInput,
} from '../api/types';
import styles from './ProjectSettingsModal.module.css';

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

export type ProjectSettingsSectionId = 'general' | 'members' | 'plugins' | 'models' | 'apikeys';

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
    id: 'plugins',
    labelKey: 'projectSettings.navPlugins',
    titleKey: 'projectSettings.pluginsTitle',
    descriptionKey: 'projectSettings.pluginsDesc',
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
          {section === 'plugins' && <ProjectPluginsPanel project={project} />}
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
