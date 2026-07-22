/*
 * DevicePairingCard — the pairing guide shown in the device views while this
 * client holds no CEK for the device (docs/17 §6.3 / §7.1). It drives the
 * useDevicePairing state machine: idle offers "start pairing", pending tells
 * the user to review the request in Desktop Settings → Cloud, denied / expired
 * / error offer a retry. `ready` renders nothing — E2EE is live.
 */
import { Key, WarningCircle } from '@phosphor-icons/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { useDevicePairing } from '../hooks/useDevicePairing';
import styles from './DevicePairingCard.module.css';

export function DevicePairingCard({ deviceId, guideLink }: { deviceId: string; guideLink?: ReactNode }) {
  const { t } = useTranslation();
  const pairing = useDevicePairing(deviceId);

  if (pairing.phase === 'loading' || pairing.phase === 'ready') return null;

  return (
    <div className={styles.card} role="status" data-testid="device-pairing-card" data-phase={pairing.phase}>
      <div className={styles.icon} aria-hidden="true">
        {pairing.phase === 'idle' || pairing.phase === 'pending' ? <Key size={18} /> : <WarningCircle size={18} />}
      </div>
      <div className={styles.body}>
        <h3 className={styles.title}>{t(`device.pairing.${pairing.phase}.title`)}</h3>
        <p className={styles.text}>{t(`device.pairing.${pairing.phase}.body`)}</p>
        {pairing.phase !== 'pending' && (
          <div className={styles.actions}>
            <Button variant="primary" size="sm" onClick={pairing.start} loading={pairing.starting}>
              {t('device.pairing.start')}
            </Button>
          </div>
        )}
        {guideLink && <div className={styles.guide}>{guideLink}</div>}
      </div>
    </div>
  );
}
