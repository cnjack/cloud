import { ArrowLeft, Warning } from '@phosphor-icons/react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { ApiError, apiErrorCode } from '../api/client';
import {
  useDevices,
  useDeviceSessions,
  useRespondDeviceApproval,
  useSendDeviceMessage,
  useStopDeviceSession,
} from '../api/deviceQueries';
import { Button } from '../components/Button';
import { StatusLabel, SurfaceInner } from '../components/PageLayout';
import { useToast } from '../components/Toast';
import { DeviceTimeline } from '../deviceview';
import { resolveOnline } from '../deviceview/offline';
import { useDeviceSessionStream } from '../hooks/useDeviceSessionStream';
import styles from './DeviceSessionPage.module.css';

export function DeviceSessionPage() {
  const { deviceId = '', sessionId = '' } = useParams();
  const { t } = useTranslation();
  const toast = useToast();

  const devices = useDevices();
  const sessions = useDeviceSessions(deviceId);
  const device = devices.data?.find((d) => d.id === deviceId);
  const session = sessions.data?.find((s) => s.session_id === sessionId);

  const { state, online: streamOnline, phase, reconnect } = useDeviceSessionStream(deviceId, sessionId);
  const online = resolveOnline(streamOnline, device?.online);
  const running = state.agentRunning || session?.status === 'running';
  const title = session?.meta?.title || t('device.welcome.untitled');

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

  const doStop = () => {
    stop.mutate(sessionId, {
      onError: (error) => {
        toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : String(error) });
      },
    });
  };

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
              ? t('device.session.deviceOffline')
              : t('device.session.sendFailed', {
                  message: error instanceof Error ? error.message : String(error),
                }),
          );
        },
      },
    );
  };

  const emptyTimeline =
    state.events.length === 0 && state.finalizedText.length === 0 && !state.streamingText && !state.agentRunning;

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
          <div className={styles.headerActions}>
            <StatusLabel tone={running ? 'warning' : 'neutral'}>
              {running ? t('device.welcome.status.running') : t('device.welcome.status.idle')}
            </StatusLabel>
            <Button
              variant="danger"
              size="sm"
              onClick={doStop}
              disabled={!running || stop.isPending}
              loading={stop.isPending}
            >
              {t('device.session.stop')}
            </Button>
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

        {emptyTimeline ? (
          <p className={styles.emptyHistory}>{t('device.session.emptyHistory')}</p>
        ) : (
          <DeviceTimeline
            events={state.events}
            finalizedText={state.finalizedText}
            streamingText={state.streamingText}
            agentRunning={state.agentRunning}
            approvals={{
              onDecide: (approvalId, decision) => {
                respondApproval.mutate(
                  { approvalId, decision },
                  {
                    onError: (error) => {
                      toast.push({
                        kind: 'error',
                        message: error instanceof ApiError ? error.message : String(error),
                      });
                    },
                  },
                );
              },
              disabled: !online || respondApproval.isPending,
            }}
          />
        )}
        <div ref={endRef} aria-hidden="true" />

        <form className={styles.composer} data-testid="session-composer" onSubmit={submit}>
          <textarea
            className={styles.textarea}
            aria-label={t('device.session.send')}
            placeholder={t('device.session.composerPlaceholder')}
            value={text}
            disabled={!online || send.isPending}
            onChange={(event) => setText(event.target.value)}
          />
          {sendError && <p className={styles.sendError} role="alert">{sendError}</p>}
          <div className={styles.composerActions}>
            {running && (
              <Button type="button" variant="danger" onClick={doStop} disabled={stop.isPending} loading={stop.isPending}>
                {t('device.session.stop')}
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={!online || !text.trim()} loading={send.isPending}>
              {t('device.session.send')}
            </Button>
          </div>
        </form>
      </div>
    </SurfaceInner>
  );
}
