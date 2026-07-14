/*
 * IntegrationsPanel — the project owner's git integrations (D19 / F5). Lists the
 * project's host bindings (provider · host · bot_username), each with a write-only
 * token rotation editor and a delete action, plus an add form. The bot token is
 * WRITE-ONLY: it is sent on create/rotate and never returned (token_set is the
 * only echo). The server verifies the token against the provider at create/rotate
 * (discovering bot_username) and validates the host against the cluster allowlist,
 * so a bad token / disallowed host surfaces here as a readable error (fail-visible).
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { Select } from '../components/Select';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import {
  useIntegrations,
  useCreateIntegration,
  useUpdateIntegration,
  useDeleteIntegration,
} from '../api/queries';
import type { GitProvider, Integration, Project } from '../api/types';
import styles from './ProjectSettingsModal.module.css';

const PROVIDERS: GitProvider[] = ['gitea', 'github', 'gitlab'];

export function IntegrationsPanel({ project }: { project: Project }) {
  const { t } = useTranslation();
  const toast = useToast();
  const integrations = useIntegrations(project.id);
  const create = useCreateIntegration(project.id);
  const del = useDeleteIntegration(project.id);

  const [name, setName] = useState('');
  const [provider, setProvider] = useState<GitProvider>('gitea');
  const [host, setHost] = useState('');
  const [token, setToken] = useState('');
  const [mode, setMode] = useState<'oauth' | 'token'>('oauth');
  const [clientId, setClientId] = useState('');
  const [clientSecret, setClientSecret] = useState('');

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      {
        name: name.trim() || undefined,
        provider,
        host: host.trim(),
        token: token.trim(),
      },
      {
        onSuccess: (integ) => {
          setName('');
          setHost('');
          setToken('');
          toast.push({
            kind: 'success',
            message: t('integrations.connected', { name: integ.name, bot: integ.bot_username }),
          });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('integrations.addError'),
          }),
      },
    );
  };

  const remove = (id: string) => {
    del.mutate(id, {
      onSuccess: () =>
        toast.push({
          kind: 'success',
          message: t('integrations.removed'),
        }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : t('integrations.removeError'),
        }),
    });
  };

  return (
    <div className={styles.body} data-testid="integrations-panel">
      <p className={styles.guardrailHint}>
        {t('integrations.intro')}
      </p>

      {integrations.data && integrations.data.length > 0 ? (
        <div className={styles.kanbanList} data-testid="integrations-list">
          {integrations.data.map((i) => (
            <IntegrationRow
              key={i.id}
              projectId={project.id}
              integration={i}
              deleting={del.isPending}
              onRemove={() => remove(i.id)}
            />
          ))}
        </div>
      ) : (
        <p className={styles.guardrailHint} data-testid="integrations-empty">
          {t('integrations.empty')}
        </p>
      )}

      <div className={styles.integrationMode} role="group" aria-label={t('integrations.methodAria')}>
        <button type="button" data-active={mode === 'oauth' || undefined} onClick={() => setMode('oauth')} data-testid="integration-mode-oauth">
          {t('integrations.oauthApp')}
        </button>
        <button type="button" data-active={mode === 'token' || undefined} onClick={() => setMode('token')} data-testid="integration-mode-token">
          {t('integrations.botToken')}
        </button>
      </div>

      <form
        className={styles.kanbanForm}
        {...(mode === 'oauth'
          ? { method: 'post', action: `/auth/integrations/${provider}` }
          : { onSubmit: submit })}
        noValidate
        data-testid="integration-form"
      >
        {mode === 'oauth' && (
          <>
            <input type="hidden" name="project_id" value={project.id} />
            <input type="hidden" name="return_to" value={`/projects/${project.id}?view=project-settings`} />
          </>
        )}
        <div>
          <label className={styles.guardrailTitle} htmlFor="integration-provider">
            {t('integrations.provider')}
          </label>
          <Select
            id="integration-provider"
            value={provider}
            onChange={(value) => setProvider(value as GitProvider)}
            data-testid="integration-provider"
            style={{ display: 'flex', width: '100%', marginTop: 4 }}
            options={PROVIDERS.map((p) => ({ value: p, label: p }))}
          />
        </div>
        <TextField
          label={t('integrations.host')}
          placeholder={t('integrations.hostPlaceholder')}
          value={host}
          onChange={(e) => setHost(e.target.value)}
          required
          data-testid="integration-host"
          autoComplete="off"
          hint={t('integrations.hostHint')}
          name="host"
        />
        <TextField
          label={t('integrations.nameLabel')}
          placeholder={t('integrations.namePlaceholder')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          data-testid="integration-name"
          autoComplete="off"
          name="name"
        />
        {mode === 'oauth' ? (
          <>
            <div className={styles.oauthCallback}>
              <span>{t('integrations.oauthCallbackUrl')}</span>
              <code>{`${window.location.origin}/auth/callback/${provider}`}</code>
              <small>{t('integrations.oauthCallbackHint')}</small>
            </div>
            <TextField
              label={t('integrations.clientId')}
              name="client_id"
              placeholder={t('integrations.clientIdPlaceholder')}
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              required
              data-testid="integration-client-id"
              autoComplete="off"
            />
            <TextField
              label={t('integrations.clientSecret')}
              name="client_secret"
              type="password"
              placeholder={t('integrations.clientSecretPlaceholder')}
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              required
              data-testid="integration-client-secret"
              autoComplete="new-password"
              hint={t('integrations.clientSecretHint')}
            />
          </>
        ) : (
          <TextField
            label={t('integrations.botToken')}
            type="password"
            placeholder={t('integrations.botTokenPlaceholder')}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            required
            data-testid="integration-token"
            autoComplete="new-password"
            hint={t('integrations.botTokenHint')}
          />
        )}
        <div className={styles.kanbanFormActions}>
          <Button
            type="submit"
            variant="primary"
            loading={mode === 'token' && create.isPending}
            disabled={mode === 'oauth' ? !host.trim() || !clientId.trim() || !clientSecret.trim() : !host.trim() || !token.trim()}
            data-testid="integration-add"
          >
            {mode === 'oauth' ? t('integrations.authorizeWith', { provider: providerLabel(provider) }) : t('integrations.connectWithBotToken')}
          </Button>
        </div>
      </form>
    </div>
  );
}

function providerLabel(provider: GitProvider) {
  if (provider === 'github') return 'GitHub';
  if (provider === 'gitlab') return 'GitLab';
  return 'Gitea';
}

/**
 * IntegrationRow — one integration: provider · host · bot badge, a write-only
 * "Rotate token" editor (re-verifies against the provider and refreshes the bot
 * username; the token is never displayed), and Remove.
 */
function IntegrationRow({
  projectId,
  integration,
  deleting,
  onRemove,
}: {
  projectId: string;
  integration: Integration;
  deleting: boolean;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const updateToken = useUpdateIntegration(projectId);
  const [editing, setEditing] = useState(false);
  const [token, setToken] = useState('');

  const rotate = (e: React.FormEvent) => {
    e.preventDefault();
    if (!token.trim()) {
      toast.push({ kind: 'error', message: t('integrations.enterToken') });
      return;
    }
    updateToken.mutate(
      { integrationId: integration.id, input: { token: token.trim() } },
      {
        onSuccess: (updated) => {
          setToken('');
          setEditing(false);
          toast.push({ kind: 'success', message: t('integrations.tokenRotated', { bot: updated.bot_username }) });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('integrations.rotateError'),
          }),
      },
    );
  };

  return (
    <div className={styles.kanbanRow} data-testid={`integration-${integration.id}`}>
      <div className={styles.kanbanMeta}>
        <div className={styles.kanbanTitle}>
          {integration.name}
          <span className={styles.badge} data-state="per_link" data-testid={`integration-provider-${integration.id}`}>
            {integration.provider}
          </span>
          <span className={styles.badge} data-testid={`integration-credential-${integration.id}`}>
            {integration.cred_type === 'oauth' ? 'OAuth' : t('integrations.botToken')}
          </span>
        </div>
        <div className={styles.kanbanSub}>
          {integration.host}
          {integration.bot_username ? ` · @${integration.bot_username}` : ''}
        </div>
        {editing && (
          <form className={styles.tokenEditor} onSubmit={rotate} noValidate>
            <TextField
              label={t('integrations.newBotToken')}
              type="password"
              placeholder={t('integrations.rotatePlaceholder')}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              data-testid={`integration-token-input-${integration.id}`}
              autoComplete="new-password"
            />
            <Button
              type="submit"
              variant="primary"
              size="sm"
              loading={updateToken.isPending}
              data-testid={`integration-token-save-${integration.id}`}
            >
              {t('integrations.rotate')}
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
              {t('common.cancel')}
            </Button>
          </form>
        )}
      </div>
      <div style={{ display: 'flex', gap: 8 }}>
        {!editing && (
          integration.cred_type === 'oauth' ? (
            <span className={styles.guardrailHint}>{t('integrations.reconnectHint')}</span>
          ) : (
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => setEditing(true)}
              data-testid={`integration-rotate-${integration.id}`}
            >
              {t('integrations.rotateToken')}
            </Button>
          )
        )}
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={deleting}
          onClick={onRemove}
          data-testid={`integration-delete-${integration.id}`}
        >
          {t('common.remove')}
        </Button>
      </div>
    </div>
  );
}
