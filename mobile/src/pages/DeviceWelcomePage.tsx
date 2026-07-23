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
import { navigateBack } from '../navigation';

/**
 * DeviceWelcomePage — the desktop-welcome equivalent: new-session composer
 * (M14: the stock jcode product composer) + the device's session list
 * (docs/17 §7.2).
 */
export function DeviceWelcomePage() {
  const { deviceId = '' } = useParams();
  return <DeviceWelcomeContent key={deviceId} deviceId={deviceId} />;
}

function DeviceWelcomeContent({ deviceId }: { deviceId: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const visibleSessions = (sessions.data ?? []).filter((session) => session.meta !== null);
  const [sendError, setSendError] = useState<string | null>(null);

  const device = devices.data?.find((d) => d.id === deviceId);
  const online = device?.online ?? false;

  // A send to 'new' is tracked as pending: the list shows a creating row
  // immediately (2s poll) and the session opens automatically once mirrored.
  const { pending, issue, found, markSent, clear, isRetryingCommandState } = usePendingNewSession(deviceId);
  const { host, runtime, isSendLocked, releaseNewSessionLock } = useDeviceComposer({
    deviceId,
    sessionId: 'new',
    device,
    hasMessages: false,
    onError: setSendError,
    onSent: markSent,
  });
  useEffect(() => {
    if (found) {
      clear();
      navigate(`/devices/${deviceId}/sessions/${found.session_id}`);
    }
  }, [found, clear, deviceId, navigate]);
  useEffect(() => {
    if (issue) releaseNewSessionLock();
  }, [issue, releaseNewSessionLock]);

  return (
    <div className="app-shell">
      <header className="topbar">
        <button type="button" onClick={() => navigateBack(navigate, '/')} className="topbar-back" aria-label={t('device.list.title')}>
          <ArrowLeft size={18} />
        </button>
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
                    {isRetryingCommandState && <span className="session-row-meta">{t('device.welcome.createSlow')}</span>}
                  </span>
                  <span className="pill" data-tone="warning">{t('device.welcome.status.running')}</span>
                </div>
              )}
              {visibleSessions.map((session) => (
                <SessionRow key={session.session_id} deviceId={deviceId} session={session} />
              ))}
              {!pending && visibleSessions.length === 0 && (
                <p className="state-block">{t('device.welcome.noSessions')}</p>
              )}
            </div>
          )}
        </div>

        <div className="composer product-composer jcode-product" data-testid="new-session-composer">
        {/* M14: stock jcode product composer (welcome placement). Offline
            sends fail visibly via the inline error below. */}
        <fieldset className="composer-lock" disabled={isSendLocked} aria-busy={isSendLocked}>
          <RuntimeProvider runtime={runtime}>
            {/* M15: placement "top" — the welcome composer is bottom-docked on
                the phone shell, so a downward picker opens off-screen. */}
            <ChatInput host={host} pickerPlacement="top" elevated />
          </RuntimeProvider>
        </fieldset>
        {sendError && <p className="send-error" role="alert">{sendError}</p>}
        {issue && <p className="send-error" role="alert">{t('device.welcome.createSlow')}</p>}
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
            {session.last_activity_at && timeAgo(session.last_activity_at)}
          </span>
        </span>
        <span className="pill" data-tone={running ? 'warning' : 'neutral'}>
          {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
        </span>
      </Link>
    </div>
  );
}
