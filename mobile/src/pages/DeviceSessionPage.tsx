import { ArrowLeft, StopCircle, Warning } from '@phosphor-icons/react';
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
  useDeviceSessions,
  useDevices,
  useStopDeviceSession,
} from '@jcloud/device-ui';

/**
 * DeviceSessionPage — one session: durable history replay + live SSE stream
 * (via the shared useDeviceSessionStream), rendered by the stock jcode Thread
 * with the product composer docked below (M14). Stop lives in the top bar;
 * approvals resolve through the runtime (relay chat approval vocabulary).
 */
export function DeviceSessionPage() {
  const { deviceId = '', sessionId = '' } = useParams();
  const { t } = useTranslation();

  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const device = devices.data?.find((d) => d.id === deviceId);
  const session = sessions.data?.find((s) => s.session_id === sessionId);

  const { state, online: streamOnline, phase, reconnect } = useDeviceSessionStream(deviceId, sessionId);
  const online = resolveOnline(streamOnline, device?.online);
  const running = state.agentRunning || session?.status === 'running';
  const title = session?.meta?.title || t('mobile.common.untitled');

  const stop = useStopDeviceSession(deviceId);

  const emptyTimeline =
    state.events.length === 0 && state.finalizedText.length === 0 && !state.streamingText && !state.agentRunning;

  // M14: send/approval failures land as local system rows in the Thread.
  const { host, runtime } = useDeviceComposer({
    deviceId,
    sessionId,
    device,
    streamState: state,
    sessionRunning: session?.status === 'running',
    hasMessages: !emptyTimeline,
    initialModel: session?.meta?.provider && session.meta.model
      ? { provider: session.meta.provider, id: session.meta.model }
      : null,
  });

  return (
    <div className="app-shell">
      <header className="topbar">
        <Link to={`/devices/${deviceId}`} className="topbar-back" aria-label={t('device.session.back')}>
          <ArrowLeft size={18} />
        </Link>
        <div className="topbar-title">
          {title}
          {device && <span className="topbar-sub">{device.name}</span>}
        </div>
        <button
          type="button"
          className="topbar-back"
          onClick={() => stop.mutate(sessionId)}
          disabled={!running || stop.isPending}
          aria-label={t('device.session.stop')}
          data-testid="stop-session"
        >
          <StopCircle size={20} color={running ? 'var(--color-danger)' : 'currentColor'} />
        </button>
      </header>

      {/* M13: e2ee-enforcing devices hide the timeline and composer behind
          the pairing gate (fullscreen centered guide) until this client
          holds the CEK. */}
      <DevicePairingGate device={device} fullscreen>
        {!online && (
          <div className="banner" role="alert" data-testid="offline-banner">
            <Warning size={16} aria-hidden />
            <span>{t('device.session.offlineBanner')}</span>
          </div>
        )}

        <DevicePairingCard deviceId={deviceId} />

        {phase === 'error' && (
          <div className="banner" role="alert">
            <Warning size={16} aria-hidden />
            <span>{t('mobile.session.streamError')}</span>
            <button type="button" onClick={reconnect}>{t('mobile.session.reconnect')}</button>
          </div>
        )}

        {/* M14: the jcode Thread owns this scroll region (height:100% of the
            bounded .timeline-scroll parent) including scroll-follow. */}
        <div className="timeline-scroll" data-testid="device-session">
          <RuntimeProvider runtime={runtime}>
            <Thread
              virtualize={false}
              pendingLabel={t('device.session.thinking')}
              emptyState={<p className="state-block">{t('mobile.session.emptyHistory')}</p>}
              overscanBottom={16}
            />
          </RuntimeProvider>
        </div>

        <div className="composer product-composer jcode-product" data-testid="session-composer">
          <RuntimeProvider runtime={runtime}>
            <ChatInput host={host} />
          </RuntimeProvider>
        </div>
      </DevicePairingGate>
    </div>
  );
}
