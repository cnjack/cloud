import { ArrowLeft, Warning } from '@phosphor-icons/react';
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
} from '@jcloud/device-ui';
import { Button } from '../components/Button';
import { SurfaceInner } from '../components/PageLayout';
import styles from './DeviceSessionPage.module.css';

export function DeviceSessionPage() {
  const { deviceId = '', sessionId = '' } = useParams();
  const { t } = useTranslation();

  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const device = devices.data?.find((d) => d.id === deviceId);
  const session = sessions.data?.find((s) => s.session_id === sessionId);

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
  });

  return (
    <SurfaceInner>
      <div className={styles.page} data-testid="device-session">
        <Link to={`/devices/${deviceId}`} className={styles.back}>
          <ArrowLeft size={14} aria-hidden="true" />
          <span>{t('device.session.back')}</span>
        </Link>

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
        <DevicePairingGate device={device}>
          <DevicePairingCard deviceId={deviceId} />

          {/* M14: stock jcode Thread (markdown/mermaid/approvals included) +
              product composer. The Thread owns scroll-follow. */}
          <div className={`${styles.conversation} jcode-product`} data-testid="session-composer">
            <RuntimeProvider runtime={runtime}>
              <Thread
                virtualize={false}
                pendingLabel={t('device.session.thinking')}
                emptyState={<p className={styles.emptyHistory}>{t('device.session.emptyHistory')}</p>}
                overscanBottom={24}
              />
              <ChatInput host={host} />
            </RuntimeProvider>
          </div>
        </DevicePairingGate>
      </div>
    </SurfaceInner>
  );
}
