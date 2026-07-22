/*
 * DevicePairingGate — the M13 pairing gate. Devices registered with
 * `e2ee: true` reject plaintext control (409 pairing_required), so their
 * session surfaces (timeline, composer, session list) must not render until
 * this client holds the CEK. While the key is missing the gate swaps its
 * children for a pairing guide (DevicePairingCard + an explanation); the
 * CekStore subscription re-resolves on every write, so a completed pairing
 * unlocks the children without a reload.
 *
 * The CEK is account-level (one key, stored per device id — see
 * devicecrypto/provider.ts); the gate asks the provider the same way the
 * decrypt layer does: getKey(deviceId). Devices without the flag (gray
 * rollout) pass straight through — no CEK lookup, no gate.
 */
import { Lock } from '@phosphor-icons/react';
import { useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { Device } from '../api/devices';
import { useDeviceCrypto } from '../api/DeviceApiProvider';
import type { DeviceCrypto } from '../devicecrypto/provider';
import { DevicePairingCard } from './DevicePairingCard';
import styles from './DevicePairingGate.module.css';

export interface DevicePairingGateProps {
  /** The device the gated surface belongs to. Undefined (still loading) passes through. */
  device: Device | undefined;
  children: ReactNode;
  /** Optional "how pairing works" link rendered under the guide card. */
  guideLink?: ReactNode;
  /** Mobile layout: the guide fills the viewport below the top bar, centered. */
  fullscreen?: boolean;
  /** Test seam — defaults to the shared runtime crypto. */
  crypto?: DeviceCrypto;
}

export function DevicePairingGate({ device, children, guideLink, fullscreen, crypto }: DevicePairingGateProps) {
  const { t } = useTranslation();
  const gated = device?.e2ee === true;
  const deviceId = device?.id ?? '';
  const providerCrypto = useDeviceCrypto();
  const keySource = crypto ?? providerCrypto;
  // null = still resolving the local CEK; render nothing rather than flash
  // ciphertext for a beat.
  const [hasKey, setHasKey] = useState<boolean | null>(gated ? null : true);

  useEffect(() => {
    if (!gated) {
      setHasKey(true);
      return;
    }
    let cancelled = false;
    const check = async () => {
      const key = await keySource.getKey(deviceId);
      if (!cancelled) setHasKey(key !== null);
    };
    setHasKey(null);
    void check();
    // put/delete bumps the store version (a completed pairing lands here), so
    // the gate re-resolves and unlocks without a reload.
    const unsubscribe = keySource.store.subscribe(() => void check());
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [gated, deviceId, keySource]);

  if (!gated || hasKey === true) return <>{children}</>;
  if (hasKey === null) return null;

  return (
    <div
      className={fullscreen ? styles.fullscreen : styles.gate}
      data-testid="device-pairing-gate"
    >
      <div className={styles.lede}>
        <Lock size={20} weight="bold" aria-hidden="true" />
        <h2 className={styles.title}>{t('device.pairing.gate.title')}</h2>
        <p className={styles.text}>{t('device.pairing.gate.body')}</p>
      </div>
      <DevicePairingCard deviceId={deviceId} guideLink={guideLink} />
    </div>
  );
}
