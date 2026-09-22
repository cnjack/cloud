import { Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { RuntimeProvider, Thread } from 'jcode-ui';
import { ChatInput } from 'jcode-ui/product';
import {
  DevicePairingCard,
  DevicePairingGate,
  resolveOnline,
  useDeviceComposer,
  useDeviceSessionStream,
  useDevices,
  useDeviceSessions,
  type Device,
  type DeviceSession,
} from '@jcloud/device-ui';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { EmptyState } from '../components/EmptyState';
import { Button } from '../components/Button';
import styles from './DeviceSessionPage.module.css';
import { DeviceModelNotice } from '../components/DeviceModelNotice';
import { DeviceSessionInspector } from './DeviceSessionInspector';

export function DeviceSessionPage() {
  const { deviceId = '', sessionId = '' } = useParams();
  const { t } = useTranslation();

  const devices = useDevices();
  const device = devices.data?.find((d) => d.id === deviceId);
  return <main className={styles.sessionShell}>
    <Link to={`/devices/${encodeURIComponent(deviceId)}`}>{t('cloudRefresh.backDevices')}</Link>
    {devices.isPending ? <LoadingBlock /> : devices.isError ? <ErrorBlock error={devices.error} onRetry={() => void devices.refetch()} /> : !device ? <EmptyState title={t('cloudRefresh.deviceNotFound')} action={<Link to="/devices">{t('cloudRefresh.backDevices')}</Link>} /> :
      <DevicePairingGate device={device}><PairedDeviceSession device={device} sessionId={sessionId} /></DevicePairingGate>}
  </main>;
}

// No decrypted title, history query or event stream exists outside the gate.
function PairedDeviceSession({ device, sessionId }: { device: Device; sessionId: string }) {
  const { t } = useTranslation();
  const deviceId = device.id;
  const sessions = useDeviceSessions(deviceId);
  const session = sessions.data?.find((s) => s.session_id === sessionId);

  if (!session && (sessions.isPending || sessions.isFetching)) return <LoadingBlock />;
  if (!session && sessions.isError) return <ErrorBlock error={sessions.error} onRetry={() => void sessions.refetch()} />;
  if (!session) return <EmptyState title={t('cloudRefresh.sessionNotFound')} action={<Link to={`/devices/${encodeURIComponent(deviceId)}`}>{t('cloudRefresh.backDevices')}</Link>} />;
  return <>
    {sessions.isError && <ErrorBlock error={sessions.error} onRetry={() => void sessions.refetch()} />}
    <DeviceSessionContent device={device} session={session} />
  </>;
}

function DeviceSessionContent({ device, session }: { device: Device; session: DeviceSession }) {
  const { t } = useTranslation();
  const deviceId = device.id;
  const sessionId = session.session_id;
  const { state, online: streamOnline, phase, reconnect } = useDeviceSessionStream(deviceId, sessionId);
  const online = resolveOnline(streamOnline, device?.online);
  const title = session?.meta?.title || t('device.welcome.untitled');

  const emptyTimeline =
    state.events.length === 0 && state.finalizedText.length === 0 && !state.streamingText && !state.agentRunning;

  // M14: the stock jcode Thread + product composer over the relay session.
  // Send/approval failures land as local system rows in the Thread itself.
  const { host, runtime } = useDeviceComposer({
    deviceId,
    sessionId,
    device,
    streamState: state,
    sessionRunning: session?.status === 'running',
    hasMessages: !emptyTimeline,
    initialWorkspace: session?.meta?.project ? { path: session.meta.project, kind: session.meta.workspace_kind ?? 'project' } : undefined,
    initialModel: session?.meta?.provider && session.meta.model
      ? { provider: session.meta.provider, id: session.meta.model }
      : null,
  });

  return (
      <div className={styles.page} data-testid="device-session">
        <header className={styles.header}>
          <div className={styles.heading}>
            <h1>{title}</h1>
            {device && (
              <p className={styles.subline}>
                {device.name}
                {session?.meta?.project && <span className={styles.mono}> · {session.meta.project}</span>}
              </p>
            )}
          </div>
        </header>

        {!online && (
          <div className={styles.banner} role="alert" data-testid="offline-banner">
            <Warning size={16} aria-hidden="true" />
            <span>{t('device.session.offlineBanner')}</span>
          </div>
        )}

        {phase === 'error' && (
          <div className={styles.streamError} role="alert">
            <span>{t('device.session.streamError')}</span>
            <Button variant="secondary" size="sm" onClick={reconnect}>{t('device.session.reconnect')}</Button>
          </div>
        )}

        {/* M13: e2ee-enforcing devices hide the timeline and composer behind
            the pairing gate until this client holds the CEK. */}

          <DevicePairingCard deviceId={deviceId} />

          {/* M14: stock jcode Thread (markdown/mermaid/approvals included) +
              product composer. The Thread owns scroll-follow. */}
          <div className={styles.contentGrid}>
          <div className={`${styles.conversation} jcode-product`} data-testid="session-composer">
            <RuntimeProvider runtime={runtime}>
              <Thread
                className={styles.timeline}
                virtualize={false}
                pendingLabel={t('device.session.thinking')}
                emptyState={<p className={styles.emptyHistory}>{t('device.session.emptyHistory')}</p>}
                overscanBottom={24}
              />
              <DeviceModelNotice device={device} />
              <fieldset className={styles.composerFieldset} disabled={!online}><ChatInput host={host} /></fieldset>
            </RuntimeProvider>
          </div>
          <DeviceSessionInspector key={`${deviceId}:${sessionId}`} device={device} session={session} online={online} />
          </div>

      </div>
  );
}
