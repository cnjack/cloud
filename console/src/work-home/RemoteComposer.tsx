import { useEffect, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { RuntimeProvider } from 'jcode-ui';
import { ChatInput } from 'jcode-ui/product';
import {
  DevicePairingApprovals,
  DevicePairingCard,
  DevicePairingGate,
  useDeviceComposer,
  usePendingNewSession,
  type Device,
} from '@jcloud/device-ui';
import { useToast } from '../components/Toast';
import styles from './WorkHomePage.module.css';

export function RemoteComposer({ device, contextHeader }: { device: Device; contextHeader?: ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const { pending, issue, found, markSent, clear, isRetryingCommandState } = usePendingNewSession(device.id);
  const { host, runtime, isSendLocked, releaseNewSessionLock } = useDeviceComposer({
    deviceId: device.id,
    sessionId: 'new',
    device,
    hasMessages: false,
    onError: (message) => toast.push({ kind: 'error', message }),
    onSent: markSent,
  });

  useEffect(() => {
    if (!found) return;
    clear();
    navigate(`/devices/${encodeURIComponent(device.id)}/sessions/${encodeURIComponent(found.session_id)}`);
  }, [clear, device.id, found, navigate]);

  useEffect(() => {
    if (!issue) return;
    releaseNewSessionLock();
  }, [issue, releaseNewSessionLock]);

  return (
    <DevicePairingGate device={device} guideLink={<Link to="/devices/guide">{t('repositories.remoteSetup')}</Link>}>
      <div className={styles.remoteStack}>
        {!device.online && <div className={styles.blocker} role="alert">{t('repositories.deviceOffline')}</div>}
        <DevicePairingCard deviceId={device.id} guideLink={<Link to="/devices/guide">{t('repositories.remoteSetup')}</Link>} />
        <DevicePairingApprovals deviceId={device.id} />
        <div className={`${styles.remoteComposer} jcode-product`} data-testid="remote-composer">
          {contextHeader && <div className={styles.remoteContextHeader}>{contextHeader}</div>}
          <fieldset disabled={isSendLocked || !device.online} aria-busy={isSendLocked}>
            <RuntimeProvider runtime={runtime}>
              <ChatInput host={host} pickerPlacement="bottom" elevated />
            </RuntimeProvider>
          </fieldset>
        </div>
        {pending && (
          <div className={styles.pendingRemote} role="status">
            <strong>{pending.text || t('repositories.startingConversation')}</strong>
            <span>{isRetryingCommandState ? t('repositories.deviceSlow') : t('repositories.creatingOnDevice')}</span>
          </div>
        )}
      </div>
    </DevicePairingGate>
  );
}
