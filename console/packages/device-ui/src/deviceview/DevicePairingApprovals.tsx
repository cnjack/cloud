import { Key, X } from '@phosphor-icons/react';
import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useDeviceApi, useDeviceCrypto } from '../api/DeviceApiProvider';
import type { DevicePairingRecord } from '../api/devices';
import { Button } from '../components/Button';
import { wrapCekForClient } from '../devicecrypto/pairing';
import styles from './DevicePairingCard.module.css';

/** Pending pairing review available to any already-approved client. */
export function DevicePairingApprovals({ deviceId }: { deviceId: string }) {
  const { t } = useTranslation();
  const api = useDeviceApi();
  const crypto = useDeviceCrypto();
  const [records, setRecords] = useState<DevicePairingRecord[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const stored = await crypto.store.get(deviceId);
    if (!stored?.pairingId) {
      setRecords([]);
      return;
    }
    try {
      setRecords(await api.listPairings(deviceId, stored.pairingId, 'pending'));
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [api, crypto, deviceId]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  async function respond(record: DevicePairingRecord, approve: boolean) {
    if (busy) return;
    setBusy(record.id);
    setError(null);
    try {
      const stored = await crypto.store.get(deviceId);
      if (!stored?.pairingId) throw new Error(t('device.pairing.requests.approvalLost'));
      const wrap = approve
        ? await wrapCekForClient(record.pubkey, stored.cek, stored.keyGen)
        : undefined;
      await api.respondPairing(deviceId, record.id, {
        approver_id: stored.pairingId,
        approve,
        key_gen: approve ? stored.keyGen : undefined,
        wrap,
      });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  if (records.length === 0 && !error) return null;
  return (
    <div className={styles.card} data-testid="device-pairing-approvals">
      <div className={styles.icon} aria-hidden="true"><Key size={18} /></div>
      <div className={styles.body}>
        <h3 className={styles.title}>{t('device.pairing.requests.title')}</h3>
        <p className={styles.text}>{t('device.pairing.requests.body')}</p>
        {records.map((record) => (
          <div className={styles.request} key={record.id}>
            <span className={styles.requestLabel}>{record.label}</span>
            <span className={styles.actions}>
              <Button variant="primary" size="sm" loading={busy === record.id} onClick={() => void respond(record, true)}>
                {t('device.pairing.requests.approve')}
              </Button>
              <Button variant="secondary" size="sm" disabled={busy !== null} onClick={() => void respond(record, false)}>
                <X size={13} aria-hidden="true" />
                {t('device.pairing.requests.deny')}
              </Button>
            </span>
          </div>
        ))}
        {error && <p className={styles.error} role="alert">{error}</p>}
      </div>
    </div>
  );
}
