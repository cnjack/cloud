import { useEffect } from 'react';
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

export function RemoteComposer({ device }: { device: Device }) {
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
    <DevicePairingGate device={device} guideLink={<Link to="/devices/guide">Remote setup</Link>}>
      <div className={styles.remoteStack}>
        {!device.online && <div className={styles.blocker} role="alert">This device is offline. Open jcode on the device and try again.</div>}
        <DevicePairingCard deviceId={device.id} guideLink={<Link to="/devices/guide">Remote setup</Link>} />
        <DevicePairingApprovals deviceId={device.id} />
        <div className={`${styles.remoteComposer} jcode-product`} data-testid="remote-composer">
          <fieldset disabled={isSendLocked || !device.online} aria-busy={isSendLocked}>
            <RuntimeProvider runtime={runtime}>
              <ChatInput host={host} pickerPlacement="bottom" elevated />
            </RuntimeProvider>
          </fieldset>
        </div>
        {pending && (
          <div className={styles.pendingRemote} role="status">
            <strong>{pending.text || 'Starting a new conversation'}</strong>
            <span>{isRetryingCommandState ? 'The device is taking longer than expected…' : 'Creating on device…'}</span>
          </div>
        )}
      </div>
    </DevicePairingGate>
  );
}
