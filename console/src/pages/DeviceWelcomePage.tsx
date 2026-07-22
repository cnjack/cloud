import { ArrowLeft, ArrowRight, Lock, Trash, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { RuntimeProvider } from 'jcode-ui';
import { ChatInput } from 'jcode-ui/product';
import { useDevices, useDeviceSessions, useDeleteDevice, useDeleteDeviceSession, DevicePairingCard, DevicePairingGate, Button, channelLabelKey, useDeviceComposer, usePendingNewSession } from '@jcloud/device-ui';
import type { DeviceSession } from '@jcloud/device-ui';
import { PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { e2eeBadgeTooltip, platformBadgeLabel } from '../lib/deviceBadges';
import { timeAgo } from '../lib/format';
import styles from './DeviceWelcomePage.module.css';
import { useEffect } from 'react';

export function DeviceWelcomePage() {
  const { deviceId = '' } = useParams();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);

  const device = devices.data?.find((d) => d.id === deviceId);
  const online = device?.online ?? false;
  const name = device?.name ?? deviceId;

  // M16: delete the device (soft revoke server-side) after a confirm step,
  // then drop back to the device list.
  const deleteDevice = useDeleteDevice();
  const onDelete = () => {
    if (!window.confirm(t('device.welcome.deleteConfirm', { name }))) return;
    deleteDevice.mutate(deviceId, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: t('device.welcome.deleted', { name }) });
        navigate('/devices');
      },
      onError: (err) => toast.push({ kind: 'error', message: err instanceof Error ? err.message : String(err) }),
    });
  };

  // M14: the stock jcode product composer. A send to 'new' is tracked as a
  // pending session: the list shows a creating row immediately (2s poll) and
  // the session opens automatically the moment the relay mirrors it.
  const { pending, expired, found, markSent, clear } = usePendingNewSession(deviceId);
  const { host, runtime } = useDeviceComposer({
    deviceId,
    sessionId: 'new',
    device,
    hasMessages: false,
    onError: (message) => toast.push({ kind: 'error', message }),
    onSent: (info: { sessionId: string; text: string; at: number }) => markSent({ text: info.text, at: info.at }),
  });
  useEffect(() => {
    if (found) {
      clear();
      navigate(`/devices/${deviceId}/sessions/${found.session_id}`);
    }
  }, [found, clear, deviceId, navigate]);
  useEffect(() => {
    if (expired) toast.push({ kind: 'info', message: t('device.welcome.createSlow') });
  }, [expired, toast, t]);

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
  const platform = platformBadgeLabel(device?.platform, t);

  return (
    <SurfaceInner>
      <div data-testid="device-welcome">
        <Link to="/devices" className={styles.back}>
          <ArrowLeft size={14} aria-hidden="true" />
          {t('device.guide.back')}
        </Link>
        <PageHeader
          eyebrow={t('device.list.title')}
          title={name}
          description={metaParts.join(' · ')}
          actions={
            <span className={styles.badges}>
              {platform && <StatusLabel tone="neutral">{platform}</StatusLabel>}
              {device?.pubkey && (
                <StatusLabel tone="success" title={e2eeBadgeTooltip(device, t)}>
                  <Lock size={11} weight="bold" aria-hidden="true" />
                </StatusLabel>
              )}
              <StatusLabel tone={online ? 'success' : 'neutral'}>
                {online ? t('device.list.online') : t('device.list.offline')}
              </StatusLabel>
              <Button
                variant="danger"
                size="sm"
                onClick={onDelete}
                loading={deleteDevice.isPending}
                data-testid="device-delete"
                title={t('device.welcome.delete')}
              >
                <Trash size={13} weight="bold" aria-hidden="true" />
                {t('device.welcome.delete')}
              </Button>
            </span>
          }
        />

        <div className={styles.stack}>
          {!online && (
            <div className={styles.banner} role="alert">
              <Warning size={16} aria-hidden="true" />
              <span>{t('device.welcome.offlineBanner')}</span>
            </div>
          )}

          {/* M13: e2ee-enforcing devices hide the session surfaces (composer,
              session list) behind the pairing gate until this client holds
              the CEK; gray-rollout devices pass straight through. */}
          <DevicePairingGate
            device={device}
            guideLink={<Link to="/devices/guide">{t('device.guide.entry')}</Link>}
          >
            <div className={styles.stack}>
              <DevicePairingCard
                deviceId={deviceId}
                guideLink={<Link to="/devices/guide">{t('device.guide.entry')}</Link>}
              />

              <section aria-labelledby="new-session-title">
            <div className={styles.sectionHead}><h2 id="new-session-title">{t('device.welcome.newSession')}</h2></div>
            {/* M14: stock jcode product composer (welcome placement: pickers
                open downward, card elevated). Offline sends fail visibly via
                the onError toast. */}
            <div data-testid="new-session-composer" className="jcode-product">
              <RuntimeProvider runtime={runtime}>
                <ChatInput host={host} pickerPlacement="bottom" elevated />
              </RuntimeProvider>
            </div>
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
            ) : (
              <ul className={styles.list}>
                {pending && (
                  <li className={styles.pendingRow} data-testid="pending-session-row" aria-live="polite">
                    <span className={styles.rowMain}>
                      <span className={styles.rowTitle}>
                        <strong>{pending.text || t('device.welcome.untitled')}</strong>
                        <span className={styles.statusBadge} data-tone="running">{t('device.welcome.creating')}</span>
                      </span>
                    </span>
                  </li>
                )}
                {(sessions.data ?? []).map((session) => (
                  <SessionRow key={session.session_id} deviceId={deviceId} session={session} />
                ))}
                {!pending && (sessions.data ?? []).length === 0 && (
                  <p className={styles.empty}>{t('device.welcome.noSessions')}</p>
                )}
              </ul>
            )}
              </section>
            </div>
          </DevicePairingGate>
        </div>
      </div>
    </SurfaceInner>
  );
}

function SessionRow({ deviceId, session }: { deviceId: string; session: DeviceSession }) {
  const { t } = useTranslation();
  const toast = useToast();
  const deleteSession = useDeleteDeviceSession(deviceId);
  const running = session.status === 'running';
  const title = session.meta?.title || t('device.welcome.untitled');
  const onDelete = () => {
    if (!window.confirm(t('device.welcome.sessionDeleteConfirm', { name: title }))) return;
    deleteSession.mutate(session.session_id, {
      onSuccess: () => toast.push({ kind: 'success', message: t('device.welcome.sessionDeleted', { name: title }) }),
      onError: (err) => toast.push({ kind: 'error', message: err instanceof Error ? err.message : String(err) }),
    });
  };
  // List-level source badge: only renders when jcode relays `source` in the
  // session meta (DeviceSessionMeta passthrough) — it does not today, so this
  // degrades to nothing until the device starts sending it. Known channels
  // get a translated label; unknown non-empty values render raw (same rule
  // as the timeline channel badge).
  const source = typeof session.meta?.source === 'string' ? session.meta.source.trim() : '';
  const sourceKey = channelLabelKey(source);
  return (
    <li className={styles.rowShell}>
      <Link
        to={`/devices/${deviceId}/sessions/${session.session_id}`}
        className={styles.row}
        data-testid="session-row"
      >
        <span className={styles.rowMain}>
          <span className={styles.rowTitle}>
            <strong>{title}</strong>
            <span className={styles.statusBadge} data-tone={running ? 'running' : 'idle'}>
              {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
            </span>
            {source && <span className={styles.sourceBadge}>{sourceKey ? t(sourceKey) : source}</span>}
          </span>
          <span className={styles.rowMeta}>
            {session.meta?.project && <span className={styles.mono}>{session.meta.project}</span>}
            <span>{timeAgo(session.updated_at)}</span>
          </span>
        </span>
        <ArrowRight size={16} aria-hidden="true" className={styles.rowArrow} />
      </Link>
      <button
        type="button"
        className={styles.sessionDelete}
        onClick={onDelete}
        disabled={running || deleteSession.isPending}
        title={running ? t('device.welcome.sessionDeleteRunning') : t('device.welcome.sessionDelete')}
        aria-label={t('device.welcome.sessionDelete')}
      >
        <Trash size={15} aria-hidden="true" />
      </button>
    </li>
  );
}
