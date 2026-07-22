import { ArrowLeft, Warning } from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { RuntimeProvider } from 'jcode-ui';
import { ChatInput } from 'jcode-ui/product';
import {
  DevicePairingCard,
  DevicePairingApprovals,
  DevicePairingGate,
  useDeviceComposer,
  useDeviceSessions,
  useDevices,
  usePendingNewSession,
  type DeviceSession,
} from '@jcloud/device-ui';
import { timeAgo } from '../lib/time';

/**
 * DeviceWelcomePage — the desktop-welcome equivalent: new-session composer
 * (M14: the stock jcode product composer) + the device's session list
 * (docs/17 §7.2).
 */
export function DeviceWelcomePage() {
  const { deviceId = '' } = useParams();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const [sendError, setSendError] = useState<string | null>(null);

  const device = devices.data?.find((d) => d.id === deviceId);
  const online = device?.online ?? false;

  // A send to 'new' is tracked as pending: the list shows a creating row
  // immediately (2s poll) and the session opens automatically once mirrored.
  const { pending, found, markSent, clear } = usePendingNewSession(deviceId);
  const { host, runtime } = useDeviceComposer({
    deviceId,
    sessionId: 'new',
    device,
    hasMessages: false,
    onError: setSendError,
    onSent: (info: { sessionId: string; text: string; at: number }) => markSent({ text: info.text, at: info.at }),
  });
  useEffect(() => {
    if (found) {
      clear();
      navigate(`/devices/${deviceId}/sessions/${found.session_id}`);
    }
  }, [found, clear, deviceId, navigate]);

  return (
    <div className="app-shell">
      <header className="topbar">
        <Link to="/" className="topbar-back" aria-label={t('device.list.title')}>
          <ArrowLeft size={18} />
        </Link>
        <div className="topbar-title">
          {device?.name ?? deviceId}
          <span className="topbar-sub">
            {[device?.hostname, device?.jcode_version].filter(Boolean).join(' · ')}
          </span>
        </div>
        <span className="pill" data-tone={online ? 'success' : 'neutral'}>
          {online ? t('mobile.devices.online') : t('mobile.devices.offline')}
        </span>
      </header>

      {/* M13: e2ee-enforcing devices hide the session list and composer
          behind the pairing gate (fullscreen centered guide) until this
          client holds the CEK; gray-rollout devices pass straight through. */}
      <DevicePairingGate
        device={device}
        fullscreen
        guideLink={<Link to="/guide">{t('device.guide.entry')}</Link>}
      >
        <div className="content" data-testid="device-welcome">
          <DevicePairingCard
            deviceId={deviceId}
            guideLink={<Link to="/guide">{t('device.guide.entry')}</Link>}
          />
          <DevicePairingApprovals deviceId={deviceId} />

          {!online && (
            <div className="banner" role="alert">
              <Warning size={16} aria-hidden />
              <span>{t('device.welcome.offlineBanner')}</span>
            </div>
          )}

          <h2 className="section-title">{t('device.welcome.sessions')}</h2>
          {sessions.isLoading ? (
            <p className="state-block">{t('mobile.common.loading')}</p>
          ) : (
            <div>
              {pending && (
                <div className="session-row pending-row" data-testid="pending-session-row" aria-live="polite">
                  <span className="session-row-main">
                    <span className="session-row-title">{pending.text || t('mobile.common.untitled')}</span>
                    <span className="session-row-meta">{t('device.welcome.creating')}</span>
                  </span>
                  <span className="pill" data-tone="warning">{t('device.welcome.status.running')}</span>
                </div>
              )}
              {(sessions.data ?? []).map((session) => (
                <SessionRow key={session.session_id} deviceId={deviceId} session={session} />
              ))}
              {!pending && (sessions.data?.length ?? 0) === 0 && (
                <p className="state-block">{t('device.welcome.noSessions')}</p>
              )}
            </div>
          )}
        </div>

        <div className="composer product-composer jcode-product" data-testid="new-session-composer">
        {/* M14: stock jcode product composer (welcome placement). Offline
            sends fail visibly via the inline error below. */}
        <RuntimeProvider runtime={runtime}>
          {/* M15: placement "top" — the welcome composer is bottom-docked on
              the phone shell, so a downward picker opens off-screen. */}
          <ChatInput host={host} pickerPlacement="top" elevated />
        </RuntimeProvider>
        {sendError && <p className="send-error" role="alert">{sendError}</p>}
        </div>
      </DevicePairingGate>
    </div>
  );
}

function SessionRow({ deviceId, session }: { deviceId: string; session: DeviceSession }) {
  const { t } = useTranslation();
  const running = session.status === 'running';
  const title = session.meta?.title || t('mobile.common.untitled');
  return (
    <div className="session-row" data-testid="session-row">
      <Link to={`/devices/${deviceId}/sessions/${session.session_id}`} className="session-row-link">
        <span className="session-row-main">
          <span className="session-row-title">{title}</span>
          <span className="session-row-meta">
            {session.meta?.project && <span>{session.meta.project} · </span>}
            {timeAgo(session.updated_at)}
          </span>
        </span>
        <span className="pill" data-tone={running ? 'warning' : 'neutral'}>
          {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
        </span>
      </Link>
    </div>
  );
}
