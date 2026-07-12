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
            message: `Integration “${integ.name}” connected as @${integ.bot_username}.`,
          });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not add the integration.',
          }),
      },
    );
  };

  const remove = (id: string) => {
    del.mutate(id, {
      onSuccess: () =>
        toast.push({
          kind: 'success',
          message: 'Integration removed. Any service that used it falls back to the per-user path.',
        }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not remove the integration.',
        }),
    });
  };

  return (
    <div className={styles.body} data-testid="integrations-panel">
      <p className={styles.guardrailHint}>
        Connect a git identity for unattended repository operations. OAuth is recommended; a
        manually created bot token remains available as an advanced alternative. PR review
        webhooks are connected separately from each service’s Automation tab.
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
          No integrations yet — connect one below.
        </p>
      )}

      <div className={styles.integrationMode} role="group" aria-label="Integration authorization method">
        <button type="button" data-active={mode === 'oauth' || undefined} onClick={() => setMode('oauth')} data-testid="integration-mode-oauth">
          OAuth app
        </button>
        <button type="button" data-active={mode === 'token' || undefined} onClick={() => setMode('token')} data-testid="integration-mode-token">
          Bot token
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
            Provider
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
          label="Host"
          placeholder="github.com or http://gitea.jcloud.svc.cluster.local:3000"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          required
          data-testid="integration-host"
          autoComplete="off"
          hint="Must be an allowed cluster git host (see the Cluster page)."
          name="host"
        />
        <TextField
          label="Name (optional)"
          placeholder="default"
          value={name}
          onChange={(e) => setName(e.target.value)}
          data-testid="integration-name"
          autoComplete="off"
          name="name"
        />
        {mode === 'oauth' ? (
          <>
            <div className={styles.oauthCallback}>
              <span>OAuth callback URL</span>
              <code>{`${window.location.origin}/auth/callback/${provider}`}</code>
              <small>Register this callback URL in the provider OAuth application.</small>
            </div>
            <TextField
              label="Client ID"
              name="client_id"
              placeholder="OAuth application client ID"
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              required
              data-testid="integration-client-id"
              autoComplete="off"
            />
            <TextField
              label="Client secret"
              name="client_secret"
              type="password"
              placeholder="OAuth application client secret"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              required
              data-testid="integration-client-secret"
              autoComplete="new-password"
              hint="Used only during this authorization round trip; it is not stored in the integration."
            />
          </>
        ) : (
          <TextField
            label="Bot token"
            type="password"
            placeholder="org PAT / group token with repo write scope"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            required
            data-testid="integration-token"
            autoComplete="new-password"
            hint="Verified against the provider on save. Stored encrypted; never displayed after."
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
            {mode === 'oauth' ? `Authorize with ${providerLabel(provider)}` : 'Connect with bot token'}
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
  const toast = useToast();
  const updateToken = useUpdateIntegration(projectId);
  const [editing, setEditing] = useState(false);
  const [token, setToken] = useState('');

  const rotate = (e: React.FormEvent) => {
    e.preventDefault();
    if (!token.trim()) {
      toast.push({ kind: 'error', message: 'Enter the new token (an integration always needs one).' });
      return;
    }
    updateToken.mutate(
      { integrationId: integration.id, input: { token: token.trim() } },
      {
        onSuccess: (updated) => {
          setToken('');
          setEditing(false);
          toast.push({ kind: 'success', message: `Token rotated — now @${updated.bot_username}.` });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not rotate the token.',
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
            {integration.cred_type === 'oauth' ? 'OAuth' : 'Bot token'}
          </span>
        </div>
        <div className={styles.kanbanSub}>
          {integration.host}
          {integration.bot_username ? ` · @${integration.bot_username}` : ''}
        </div>
        {editing && (
          <form className={styles.tokenEditor} onSubmit={rotate} noValidate>
            <TextField
              label="New bot token"
              type="password"
              placeholder="verified against the provider on save"
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
              Rotate
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </form>
        )}
      </div>
      <div style={{ display: 'flex', gap: 8 }}>
        {!editing && (
          integration.cred_type === 'oauth' ? (
            <span className={styles.guardrailHint}>Remove and authorize again to reconnect.</span>
          ) : (
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => setEditing(true)}
              data-testid={`integration-rotate-${integration.id}`}
            >
              Rotate token
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
          Remove
        </Button>
      </div>
    </div>
  );
}
