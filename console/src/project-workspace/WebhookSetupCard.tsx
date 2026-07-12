import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../api/client';
import { useEnsureServiceWebhook } from '../api/queries';
import type { AuthProviderInfo, Me, Service } from '../api/types';
import { Button } from '../components/Button';
import { serviceProviderLabel, serviceSource } from './presentation';
import styles from './WebhookSetupCard.module.css';

const PROVIDER_LABELS: Record<string, string> = {
  gitea: 'Gitea',
  github: 'GitHub',
  gitlab: 'GitLab',
};

export function WebhookSetupCard({
  service,
  me,
  providers,
  canConfigure,
  returnTo,
  oauthReturned = false,
}: {
  service: Service;
  me: Me | null;
  providers: readonly AuthProviderInfo[];
  /** Members may sync a webhook; viewers get an explicit read-only explanation. */
  canConfigure: boolean;
  /** Verified by the OAuth server before it uses this path after reauthorization. */
  returnTo: string;
  /** OAuth returned to this service after a successful provider authorization. */
  oauthReturned?: boolean;
}) {
  const sync = useEnsureServiceWebhook();
  const autoSyncedService = useRef<string | null>(null);
  const [success, setSuccess] = useState<{ provider: string; endpoint: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const provider = service.provider?.toLowerCase() ?? '';
  const providerName = PROVIDER_LABELS[provider] ?? serviceProviderLabel(service);
  const oauthConfigured = providers.some((entry) => entry.id === provider);
  const hasIdentity =
    !me?.is_service && me?.identities.some((identity) => identity.provider === provider) === true;
  const connectURL = `/auth/link/${encodeURIComponent(provider)}?${new URLSearchParams({
    return_to: returnTo,
  }).toString()}`;

  const requestSync = useCallback(() => {
    setError(null);
    setSuccess(null);
    sync.mutate(service.id, {
      onSuccess: (result) => setSuccess({ provider: result.provider, endpoint: result.endpoint }),
      onError: (reason) =>
        setError(
          reason instanceof ApiError
            ? reason.message
            : 'Could not synchronize the provider webhook. Try again or contact a cluster administrator.',
        ),
    });
  }, [service.id, sync]);

  // The caller has just completed a provider-controlled OAuth consent flow for
  // this exact service. Register the webhook once on return so onboarding does
  // not turn into a second, unnecessary click. The server operation is
  // idempotent and any failure remains visible in this card.
  const canAutoSync =
    oauthReturned &&
    service.repo_kind === 'provider' &&
    !!provider &&
    canConfigure &&
    oauthConfigured &&
    !me?.is_service &&
    hasIdentity;
  useEffect(() => {
    if (!canAutoSync || autoSyncedService.current === service.id) return;
    autoSyncedService.current = service.id;
    requestSync();
  }, [canAutoSync, requestSync, service.id]);

  if (service.repo_kind !== 'provider' || !provider) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>PR review webhook</span>
          <h3 id="webhook-heading">Provider events need a provider-backed repository</h3>
        </div>
        <p>
          <code>{serviceSource(service)}</code> is a path or raw URL, so it cannot receive pull-request or merge-request comment events.
        </p>
      </section>
    );
  }

  if (!canConfigure) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>PR review webhook</span>
          <h3 id="webhook-heading">Automation is read-only for viewers</h3>
        </div>
        <p>A project member can connect {providerName} and synchronize this repository’s review webhook.</p>
      </section>
    );
  }

  if (!oauthConfigured) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>PR review webhook</span>
          <h3 id="webhook-heading">{providerName} OAuth is not configured in this cluster</h3>
        </div>
        <p>Ask a cluster administrator to configure {providerName} OAuth and the webhook receiver. No fallback credential will be used.</p>
      </section>
    );
  }

  if (me?.is_service) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>PR review webhook</span>
          <h3 id="webhook-heading">A signed-in project member must authorize {providerName}</h3>
        </div>
        <p>The console-token session has no personal OAuth identity. Sign in with a project member account to synchronize this webhook.</p>
      </section>
    );
  }

  if (!hasIdentity) {
    return (
      <section className={styles.card} data-testid="webhook-setup-oauth" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <div>
            <span className={styles.eyebrow}>Provider webhook</span>
            <h3 id="webhook-heading">Connect {providerName} for PR review commands</h3>
          </div>
          <span className={styles.scope}>{providerName}</span>
        </div>
        <p>
          Connect the account that administers <code>{service.repo_owner_name}</code>. jcode will request OAuth permission for this repository; it never asks you to paste a personal access token here.
        </p>
        <a className={styles.primaryLink} href={connectURL} data-testid="webhook-oauth-connect">
          Connect {providerName} with OAuth
        </a>
      </section>
    );
  }

  return (
    <section className={styles.card} data-testid="webhook-setup-ready" aria-labelledby="webhook-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>Provider webhook</span>
          <h3 id="webhook-heading">{providerName} PR review commands</h3>
        </div>
        <span className={styles.scope}>{providerName}</span>
      </div>
      <p>
        Sync registers the provider’s comment webhook for <code>{service.repo_owner_name}</code> using your {providerName} OAuth account. A collaborator can then start a review with <code>@jcode review</code>. It is safe to run again when the repository changes.
      </p>
      {oauthReturned && (
        <p className={styles.returned} data-testid="webhook-oauth-returned">
          OAuth is connected. {sync.isPending ? 'Synchronizing the webhook…' : 'Webhook setup is ready.'}
        </p>
      )}
      {success && (
        <p className={styles.success} data-testid="webhook-setup-success" role="status">
          {PROVIDER_LABELS[success.provider] ?? success.provider} webhook is synced to <code>{success.endpoint}</code>.
        </p>
      )}
      {error && (
        <p className={styles.error} data-testid="webhook-setup-error" role="alert">
          {error}
        </p>
      )}
      <div className={styles.actions}>
        <Button
          type="button"
          variant="primary"
          size="sm"
          onClick={requestSync}
          loading={sync.isPending}
          data-testid="webhook-sync"
        >
          Sync {providerName} webhook
        </Button>
        <a className={styles.reconnect} href={connectURL} data-testid="webhook-oauth-reconnect">
          Reconnect OAuth
        </a>
      </div>
    </section>
  );
}
