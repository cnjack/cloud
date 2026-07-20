/*
 * DeviceAuthorizePage — the browser half of the jcode device-code login
 * (docs/17 §3, RFC 8628). The CLI prints a short user_code; the signed-in
 * user enters it here (or arrives with ?user_code= prefilled), confirms the
 * code matches what the CLI shows, then approves or denies. Kept deliberately
 * minimal for P1 — the device management UI is a later module.
 */
import { useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { CheckCircle, XCircle } from '@phosphor-icons/react';
import { postDeviceAuthorize, apiErrorCode } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { readQueryParam } from '../lib/url';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import styles from './DeviceAuthorizePage.module.css';

type Stage = 'enter' | 'confirm' | 'done';

export function DeviceAuthorizePage() {
  const { t } = useTranslation();
  const { getToken } = useAuth();
  const [code, setCode] = useState(() => readQueryParam('user_code') ?? '');
  const [stage, setStage] = useState<Stage>('enter');
  const [approved, setApproved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>();

  const normalized = code.trim().toUpperCase();

  const toConfirm = (e: FormEvent) => {
    e.preventDefault();
    if (!normalized) {
      setError(t('device.codeRequired'));
      return;
    }
    setError(undefined);
    setStage('confirm');
  };

  const decide = async (approve: boolean) => {
    setBusy(true);
    setError(undefined);
    try {
      await postDeviceAuthorize(getToken(), normalized, approve);
      setApproved(approve);
      setStage('done');
    } catch (err) {
      const errCode = apiErrorCode(err);
      setError(
        errCode === 'not_found'
          ? t('device.errorNotFound')
          : errCode === 'already_decided'
            ? t('device.errorAlreadyDecided')
            : err instanceof Error
              ? err.message
              : t('device.errorGeneric'),
      );
      setStage('enter');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={styles.stage}>
      <Card className={styles.card}>
        {stage === 'done' ? (
          <div className={styles.result} data-testid="device-result" data-approved={approved}>
            {approved ? (
              <CheckCircle size={32} weight="fill" aria-hidden="true" />
            ) : (
              <XCircle size={32} weight="fill" aria-hidden="true" />
            )}
            <h1 className={styles.title}>
              {approved ? t('device.approvedTitle') : t('device.deniedTitle')}
            </h1>
            <p className={styles.lede}>
              {approved ? t('device.approvedBody') : t('device.deniedBody')}
            </p>
            <Button
              variant="secondary"
              onClick={() => {
                setCode('');
                setStage('enter');
              }}
            >
              {t('device.another')}
            </Button>
          </div>
        ) : stage === 'confirm' ? (
          <div className={styles.result} data-testid="device-confirm">
            <h1 className={styles.title}>{t('device.confirmTitle')}</h1>
            <p className={styles.lede}>{t('device.confirmBody')}</p>
            <code className={styles.code} data-testid="device-code">{normalized}</code>
            <div className={styles.actions}>
              <Button variant="primary" loading={busy} onClick={() => decide(true)} data-testid="device-approve">
                {t('device.approve')}
              </Button>
              <Button variant="ghost" disabled={busy} onClick={() => decide(false)} data-testid="device-deny">
                {t('device.deny')}
              </Button>
            </div>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setStage('enter')}>
              {t('common.back')}
            </Button>
          </div>
        ) : (
          <form onSubmit={toConfirm} className={styles.form} data-testid="device-enter">
            <h1 className={styles.title}>{t('device.title')}</h1>
            <p className={styles.lede}>{t('device.lede')}</p>
            <TextField
              label={t('device.codeLabel')}
              value={code}
              onChange={(e) => {
                setCode(e.target.value);
                setError(undefined);
              }}
              error={error}
              placeholder="XXXX-XXXX"
              autoComplete="off"
              autoFocus
              hint={t('device.codeHint')}
            />
            <Button type="submit" variant="primary">
              {t('device.continue')}
            </Button>
          </form>
        )}
      </Card>
    </section>
  );
}
