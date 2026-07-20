import { Devices } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link } from 'react-router-dom';
import { ApiError } from '../api/client';
import { useDevices } from '../api/deviceQueries';
import type { Device } from '../api/devices';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { timeAgo } from '../lib/format';
import styles from './DevicesPage.module.css';

function lastSeenLabel(device: Device, t: TFunction): string {
  return device.last_seen_at
    ? t('device.list.lastSeen', { time: timeAgo(device.last_seen_at) })
    : t('device.list.neverSeen');
}

export function DevicesPage() {
  const { t } = useTranslation();
  const devices = useDevices();

  return (
    <SurfaceInner>
      <div data-testid="devices-page">
        <PageHeader
          eyebrow={t('shell.workspaceEyebrow')}
          title={t('device.list.title')}
          description={t('device.list.subtitle')}
        />

        {devices.isLoading ? (
          <div className={styles.state}><LoadingBlock label={t('common.loading')} /></div>
        ) : devices.isError ? (
          <div className={styles.state}>
            {devices.error instanceof ApiError && devices.error.status === 400 ? (
              // 400 here means the console is acting as a service principal; the
              // device surface is user-account scoped (device.list.servicePrincipal).
              <div className={styles.serviceError} role="alert">
                <strong>{t('device.list.loadErrorTitle')}</strong>
                <p>{t('device.list.servicePrincipal')}</p>
                <div><Button variant="secondary" size="sm" onClick={() => devices.refetch()}>{t('common.retry')}</Button></div>
              </div>
            ) : (
              <ErrorBlock error={devices.error} onRetry={() => devices.refetch()} title={t('device.list.loadErrorTitle')} />
            )}
          </div>
        ) : (devices.data?.length ?? 0) === 0 ? (
          <div className={styles.state}>
            <EmptyState
              title={t('device.list.emptyTitle')}
              description={t('device.list.emptyBody')}
              icon={<Devices size={28} aria-hidden="true" />}
              data-testid="devices-empty"
            />
          </div>
        ) : (
          <div className={styles.grid}>
            {(devices.data ?? []).map((device) => (
              <Link key={device.id} to={`/devices/${device.id}`} className={styles.card} data-testid="device-card">
                <header className={styles.cardHead}>
                  <h2>{device.name}</h2>
                  <StatusLabel tone={device.online ? 'success' : 'neutral'}>
                    {device.online ? t('device.list.online') : t('device.list.offline')}
                  </StatusLabel>
                </header>
                <dl className={styles.facts}>
                  {device.hostname && (
                    <div className={styles.fact}><dt>Hostname</dt><dd className={styles.mono}>{device.hostname}</dd></div>
                  )}
                  {device.jcode_version && (
                    <div className={styles.fact}><dt>jcode</dt><dd className={styles.mono}>{device.jcode_version}</dd></div>
                  )}
                  <div className={styles.fact}><dd className={styles.seen}>{lastSeenLabel(device, t)}</dd></div>
                </dl>
              </Link>
            ))}
          </div>
        )}
      </div>
    </SurfaceInner>
  );
}
