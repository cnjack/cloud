import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
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
  const { t } = useTranslation();
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
            : t('webhook.errSync'),
        ),
    });
  }, [service.id, sync, t]);

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
          <span className={styles.eyebrow}>{t('webhook.eyebrowReview')}</span>
          <h3 id="webhook-heading">{t('webhook.unavailableTitle')}</h3>
        </div>
        <p>
          <code>{serviceSource(service)}</code> {t('webhook.pathOrUrlNote')}
        </p>
      </section>
    );
  }

  if (!canConfigure) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>{t('webhook.eyebrowReview')}</span>
          <h3 id="webhook-heading">{t('webhook.readonlyTitle')}</h3>
        </div>
        <p>{t('webhook.readonlyBody', { provider: providerName })}</p>
      </section>
    );
  }

  if (!oauthConfigured) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>{t('webhook.eyebrowReview')}</span>
          <h3 id="webhook-heading">{t('webhook.oauthNotConfiguredTitle', { provider: providerName })}</h3>
        </div>
        <p>{t('webhook.oauthNotConfiguredBody', { provider: providerName })}</p>
      </section>
    );
  }

  if (me?.is_service) {
    return (
      <section className={styles.card} data-testid="webhook-setup-unavailable" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <span className={styles.eyebrow}>{t('webhook.eyebrowReview')}</span>
          <h3 id="webhook-heading">{t('webhook.serviceTitle', { provider: providerName })}</h3>
        </div>
        <p>{t('webhook.serviceBody')}</p>
      </section>
    );
  }

  if (!hasIdentity) {
    return (
      <section className={styles.card} data-testid="webhook-setup-oauth" aria-labelledby="webhook-heading">
        <div className={styles.head}>
          <div>
            <span className={styles.eyebrow}>{t('webhook.eyebrowProvider')}</span>
            <h3 id="webhook-heading">{t('webhook.connectTitle', { provider: providerName })}</h3>
          </div>
          <span className={styles.scope}>{providerName}</span>
        </div>
        <p>
          {t('webhook.connectBodyPre')} <code>{service.repo_owner_name}</code>{t('webhook.connectBodyPost')}
        </p>
        <a className={styles.primaryLink} href={connectURL} data-testid="webhook-oauth-connect">
          {t('webhook.connectCta', { provider: providerName })}
        </a>
      </section>
    );
  }

  return (
    <section className={styles.card} data-testid="webhook-setup-ready" aria-labelledby="webhook-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>{t('webhook.eyebrowProvider')}</span>
          <h3 id="webhook-heading">{t('webhook.readyTitle', { provider: providerName })}</h3>
        </div>
        <span className={styles.scope}>{providerName}</span>
      </div>
      <p>
        {t('webhook.readyBodyPre')} <code>{service.repo_owner_name}</code>{t('webhook.readyBodyMid', { provider: providerName })}<code>@jcode review</code>{t('webhook.readyBodyPost')}
      </p>
      {oauthReturned && (
        <p className={styles.returned} data-testid="webhook-oauth-returned">
          {t('webhook.oauthConnected')} {sync.isPending ? t('webhook.synchronizing') : t('webhook.setupReady')}
        </p>
      )}
      {success && (
        <p className={styles.success} data-testid="webhook-setup-success" role="status">
          {PROVIDER_LABELS[success.provider] ?? success.provider} {t('webhook.syncedTo')} <code>{success.endpoint}</code>.
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
          {t('webhook.syncButton', { provider: providerName })}
        </Button>
        <a className={styles.reconnect} href={connectURL} data-testid="webhook-oauth-reconnect">
          {t('webhook.reconnectOauth')}
        </a>
      </div>
    </section>
  );
}
