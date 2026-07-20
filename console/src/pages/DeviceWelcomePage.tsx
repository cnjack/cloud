import { ArrowRight, Warning } from '@phosphor-icons/react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { useDevices, useDeviceSessions, useSendDeviceMessage, DevicePairingCard } from '@jcloud/device-ui';
import type { DeviceSession } from '@jcloud/device-ui';
import { Button } from '../components/Button';
import { PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { Select } from '../components/Select';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import styles from './DeviceWelcomePage.module.css';

type Mode = '' | 'plan' | 'full_access';

const MODE_OPTIONS: { value: Mode; label: string }[] = [
  { value: '', label: 'default' },
  { value: 'plan', label: 'plan' },
  { value: 'full_access', label: 'full_access' },
];

export function DeviceWelcomePage() {
  const { deviceId = '' } = useParams();
  const { t } = useTranslation();
  const toast = useToast();
  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const send = useSendDeviceMessage(deviceId);
  const [text, setText] = useState('');
  const [mode, setMode] = useState<Mode>('');

  const device = devices.data?.find((d) => d.id === deviceId);
  const online = device?.online ?? false;
  const name = device?.name ?? deviceId;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const prompt = text.trim();
    if (!prompt || !online || send.isPending) return;
    send.mutate(
      // sid "new" lets the device assign the session id; the 202 reply may
      // carry session_id: null, so we stay put — the polling session list
      // surfaces the new session for the user to open.
      { sessionId: 'new', text: prompt, ...(mode ? { mode } : {}) },
      {
        onSuccess: () => setText(''),
        onError: (error) => {
          toast.push({
            kind: 'error',
            message: error instanceof ApiError
              ? error.message
              : t('device.session.sendFailed', { message: String(error) }),
          });
        },
      },
    );
  };

  if (devices.isLoading) {
    return <SurfaceInner><div className={styles.state}><LoadingBlock label={t('common.loading')} /></div></SurfaceInner>;
  }
  if (devices.isError) {
    return (
      <SurfaceInner>
        <div className={styles.state}>
          <ErrorBlock error={devices.error} onRetry={() => devices.refetch()} title={t('device.list.loadErrorTitle')} />
        </div>
      </SurfaceInner>
    );
  }

  const metaParts = [
    device?.hostname,
    device?.jcode_version,
    device?.last_seen_at
      ? t('device.list.lastSeen', { time: timeAgo(device.last_seen_at) })
      : t('device.list.neverSeen'),
  ].filter((part): part is string => !!part);

  return (
    <SurfaceInner>
      <div data-testid="device-welcome">
        <PageHeader
          eyebrow={t('device.list.title')}
          title={name}
          description={metaParts.join(' · ')}
          actions={
            <StatusLabel tone={online ? 'success' : 'neutral'}>
              {online ? t('device.list.online') : t('device.list.offline')}
            </StatusLabel>
          }
        />

        <div className={styles.stack}>
          <DevicePairingCard deviceId={deviceId} />

          {!online && (
            <div className={styles.banner} role="alert">
              <Warning size={16} aria-hidden="true" />
              <span>{t('device.welcome.offlineBanner')}</span>
            </div>
          )}

          <section aria-labelledby="new-session-title">
            <div className={styles.sectionHead}><h2 id="new-session-title">{t('device.welcome.newSession')}</h2></div>
            <form className={styles.composer} data-testid="new-session-composer" onSubmit={submit}>
              <textarea
                className={styles.textarea}
                aria-label={t('device.welcome.newSession')}
                placeholder={t('device.welcome.composerPlaceholder')}
                value={text}
                disabled={!online || send.isPending}
                onChange={(event) => setText(event.target.value)}
              />
              <div className={styles.composerActions}>
                <Select
                  value={mode}
                  onChange={(value) => setMode(value as Mode)}
                  options={MODE_OPTIONS}
                  aria-label={t('device.welcome.mode')}
                  disabled={!online || send.isPending}
                  className={styles.modeSelect}
                />
                <Button type="submit" variant="primary" disabled={!online || !text.trim()} loading={send.isPending}>
                  {t('device.welcome.send')}
                </Button>
              </div>
            </form>
          </section>

          <section aria-labelledby="device-sessions-title">
            <div className={styles.sectionHead}>
              <h2 id="device-sessions-title">{t('device.welcome.sessions')}</h2>
              {sessions.data && <span className={styles.count}>{t('device.welcome.sessionCount', { count: sessions.data.length })}</span>}
            </div>
            {sessions.isLoading ? (
              <LoadingBlock label={t('common.loading')} />
            ) : sessions.isError ? (
              <ErrorBlock error={sessions.error} onRetry={() => sessions.refetch()} />
            ) : (sessions.data?.length ?? 0) === 0 ? (
              <p className={styles.empty}>{t('device.welcome.noSessions')}</p>
            ) : (
              <ul className={styles.list}>
                {(sessions.data ?? []).map((session) => (
                  <SessionRow key={session.session_id} deviceId={deviceId} session={session} />
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
    </SurfaceInner>
  );
}

function SessionRow({ deviceId, session }: { deviceId: string; session: DeviceSession }) {
  const { t } = useTranslation();
  const running = session.status === 'running';
  return (
    <li>
      <Link
        to={`/devices/${deviceId}/sessions/${session.session_id}`}
        className={styles.row}
        data-testid="session-row"
      >
        <span className={styles.rowMain}>
          <span className={styles.rowTitle}>
            <strong>{session.meta?.title || t('device.welcome.untitled')}</strong>
            <span className={styles.statusBadge} data-tone={running ? 'running' : 'idle'}>
              {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
            </span>
          </span>
          <span className={styles.rowMeta}>
            {session.meta?.project && <span className={styles.mono}>{session.meta.project}</span>}
            <span>{timeAgo(session.updated_at)}</span>
          </span>
        </span>
        <ArrowRight size={16} aria-hidden="true" className={styles.rowArrow} />
      </Link>
    </li>
  );
}
