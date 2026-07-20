import { ArrowLeft, ArrowRight, Warning } from '@phosphor-icons/react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import {
  ApiError,
  DevicePairingCard,
  apiErrorCode,
  useDeviceSessions,
  useDevices,
  useSendDeviceMessage,
  type DeviceSession,
} from '@jcloud/device-ui';
import { timeAgo } from '../lib/time';

type Mode = '' | 'plan' | 'full_access';

const MODES: Mode[] = ['', 'plan', 'full_access'];

/**
 * DeviceWelcomePage — the desktop-welcome equivalent: new-session composer
 * (with mode picker) + the device's session list (docs/17 §7.2).
 */
export function DeviceWelcomePage() {
  const { deviceId = '' } = useParams();
  const { t } = useTranslation();
  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const send = useSendDeviceMessage(deviceId);
  const [text, setText] = useState('');
  const [mode, setMode] = useState<Mode>('');
  const [sendError, setSendError] = useState<string | null>(null);

  const device = devices.data?.find((d) => d.id === deviceId);
  const online = device?.online ?? false;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const prompt = text.trim();
    if (!prompt || !online || send.isPending) return;
    setSendError(null);
    send.mutate(
      // sid "new": the device assigns the session id; the polling session
      // list surfaces it for the user to open.
      { sessionId: 'new', text: prompt, ...(mode ? { mode } : {}) },
      {
        onSuccess: () => setText(''),
        onError: (error) => {
          setSendError(
            apiErrorCode(error) === 'device_offline'
              ? t('mobile.session.deviceOffline')
              : t('mobile.session.sendFailed', {
                  message: error instanceof ApiError ? error.message : String(error),
                }),
          );
        },
      },
    );
  };

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

      <div className="content" data-testid="device-welcome">
        <DevicePairingCard deviceId={deviceId} />

        {!online && (
          <div className="banner" role="alert">
            <Warning size={16} aria-hidden />
            <span>{t('device.welcome.offlineBanner')}</span>
          </div>
        )}

        <h2 className="section-title">{t('device.welcome.sessions')}</h2>
        {sessions.isLoading ? (
          <p className="state-block">{t('mobile.common.loading')}</p>
        ) : (sessions.data?.length ?? 0) === 0 ? (
          <p className="state-block">{t('device.welcome.noSessions')}</p>
        ) : (
          <div>
            {(sessions.data ?? []).map((session) => (
              <SessionRow key={session.session_id} deviceId={deviceId} session={session} />
            ))}
          </div>
        )}
      </div>

      <form className="composer" onSubmit={submit} data-testid="new-session-composer">
        <textarea
          aria-label={t('device.welcome.newSession')}
          placeholder={t('device.welcome.composerPlaceholder')}
          value={text}
          disabled={!online || send.isPending}
          onChange={(e) => setText(e.target.value)}
        />
        {sendError && <p className="send-error" role="alert">{sendError}</p>}
        <div className="composer-actions">
          <div className="mode-picker" role="group" aria-label={t('device.welcome.mode')}>
            {MODES.map((m) => (
              <button
                key={m || 'default'}
                type="button"
                data-active={mode === m}
                disabled={!online || send.isPending}
                onClick={() => setMode(m)}
              >
                {m || 'default'}
              </button>
            ))}
          </div>
          <button
            type="submit"
            className="topbar-back"
            aria-label={t('device.welcome.send')}
            disabled={!online || !text.trim() || send.isPending}
          >
            <ArrowRight size={18} color="var(--color-accent)" />
          </button>
        </div>
      </form>
    </div>
  );
}

function SessionRow({ deviceId, session }: { deviceId: string; session: DeviceSession }) {
  const { t } = useTranslation();
  const running = session.status === 'running';
  return (
    <Link
      to={`/devices/${deviceId}/sessions/${session.session_id}`}
      className="session-row"
      data-testid="session-row"
    >
      <span className="session-row-main">
        <span className="session-row-title">{session.meta?.title || t('mobile.common.untitled')}</span>
        <span className="session-row-meta">
          {session.meta?.project && <span>{session.meta.project} · </span>}
          {timeAgo(session.updated_at)}
        </span>
      </span>
      <span className="pill" data-tone={running ? 'warning' : 'neutral'}>
        {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
      </span>
    </Link>
  );
}
