/*
 * DevicePairingCard — the pairing guide shown in the device views while this
 * client holds no CEK for the device (docs/17 §6.3 / §7.1). It drives the
 * useDevicePairing state machine: idle offers "start pairing" (and points
 * phones at the faster desktop-QR path), pending tells the user to approve in
 * the desktop app's pulsing cloud badge (M17 — the CLI command is now a
 * fallback footnote with a copy button), denied / expired / error offer a
 * retry. `ready` renders nothing — E2EE is live.
 */
import { Check, Copy, Key, WarningCircle } from '@phosphor-icons/react';
import type { ReactNode } from 'react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { useDevicePairing } from '../hooks/useDevicePairing';
import styles from './DevicePairingCard.module.css';

export function DevicePairingCard({ deviceId, guideLink }: { deviceId: string; guideLink?: ReactNode }) {
  const { t } = useTranslation();
  const pairing = useDevicePairing(deviceId);
  const [copied, setCopied] = useState(false);

  if (pairing.phase === 'loading' || pairing.phase === 'ready') return null;

  const copyCommand = async (command: string) => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard unavailable (insecure context) — the command stays selectable.
    }
  };

  return (
    <div className={styles.card} role="status" data-testid="device-pairing-card" data-phase={pairing.phase}>
      <div className={styles.icon} aria-hidden="true">
        {pairing.phase === 'idle' || pairing.phase === 'pending' ? <Key size={18} /> : <WarningCircle size={18} />}
      </div>
      <div className={styles.body}>
        <h3 className={styles.title}>{t(`device.pairing.${pairing.phase}.title`)}</h3>
        <p className={styles.text}>{t(`device.pairing.${pairing.phase}.body`)}</p>
        {pairing.phase === 'pending' && pairing.pairingId && (
          <div className={styles.cliFallback}>
            <span className={styles.cliHint}>{t('device.pairing.pending.cliHint')}</span>
            <code className={styles.command} data-testid="pairing-cli-command">
              jcode cloud approve {pairing.pairingId}
            </code>
            <button
              type="button"
              className={styles.copyButton}
              aria-label={copied ? t('device.pairing.copied') : t('device.pairing.copy')}
              title={copied ? t('device.pairing.copied') : t('device.pairing.copy')}
              data-testid="pairing-copy-command"
              onClick={() => void copyCommand(`jcode cloud approve ${pairing.pairingId}`)}
            >
              {copied ? <Check size={13} weight="bold" /> : <Copy size={13} />}
            </button>
          </div>
        )}
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
