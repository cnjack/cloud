import { ArrowLeft, StopCircle, Warning } from '@phosphor-icons/react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import {
  ApiError,
  DevicePairingCard,
  DeviceTimeline,
  apiErrorCode,
  resolveOnline,
  useDeviceSessionStream,
  useDeviceSessions,
  useDevices,
  useRespondDeviceApproval,
  useSendDeviceMessage,
  useStopDeviceSession,
} from '@jcloud/device-ui';

/**
 * DeviceSessionPage — one session: durable history replay + live SSE stream
 * (via the shared useDeviceSessionStream), message composer, stop, and
 * approval decisions (docs/17 §7.2).
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
  const send = useSendDeviceMessage(deviceId);
  const respondApproval = useRespondDeviceApproval(deviceId, sessionId);
  const [text, setText] = useState('');
  const [sendError, setSendError] = useState<string | null>(null);

  // Follow the tail as new events / streaming text arrive.
  const endRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' });
  }, [state.events.length, state.streamingText, state.finalizedText.length]);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const prompt = text.trim();
    if (!prompt || !online || send.isPending) return;
    setSendError(null);
    send.mutate(
      { sessionId, text: prompt },
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

  const emptyTimeline =
    state.events.length === 0 && state.finalizedText.length === 0 && !state.streamingText && !state.agentRunning;

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

      <div className="timeline-scroll" data-testid="device-session">
        {emptyTimeline ? (
          <p className="state-block">{t('mobile.session.emptyHistory')}</p>
        ) : (
          <DeviceTimeline
            events={state.events}
            finalizedText={state.finalizedText}
            streamingText={state.streamingText}
            agentRunning={state.agentRunning}
            approvals={{
              onDecide: (approvalId, decision) => respondApproval.mutate({ approvalId, decision }),
              disabled: !online || respondApproval.isPending,
            }}
          />
        )}
        <div ref={endRef} aria-hidden />
      </div>

      <form className="composer" onSubmit={submit} data-testid="session-composer">
        <textarea
          aria-label={t('device.session.send')}
          placeholder={t('device.session.composerPlaceholder')}
          value={text}
          disabled={!online || send.isPending}
          onChange={(e) => setText(e.target.value)}
        />
        {sendError && <p className="send-error" role="alert">{sendError}</p>}
        <div className="composer-actions">
          <button
            type="submit"
            className="topbar-back"
            aria-label={t('device.session.send')}
            disabled={!online || !text.trim() || send.isPending}
          >
            <ArrowLeft size={18} style={{ transform: 'rotate(180deg)' }} color="var(--color-accent)" />
          </button>
        </div>
      </form>
    </div>
  );
}
