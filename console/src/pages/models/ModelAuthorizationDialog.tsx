import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useApi } from '../../api/ApiProvider';
import type { ModelAuthorization, ModelProvider } from '../../api/types';
import { qk } from '../../api/queries';
import { Button } from '../../components/Button';
import { TextField } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { ProviderIcon } from '../../components/ProviderIcon';
import styles from './ModelAuthorizationDialog.module.css';

export function ModelAuthorizationDialog({ open, provider, onClose }: { open: boolean; provider: ModelProvider | null; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const { t } = useTranslation();
  const [id, setId] = useState(provider?.id ?? '');
  const [name, setName] = useState('ChatGPT');
  const [status, setStatus] = useState<ModelAuthorization>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [paused, setPaused] = useState(false);
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['account-model-providers'] });
    void qc.invalidateQueries({ queryKey: qk.accountModels });
  };
  const report = (cause: unknown) => setError(cause instanceof Error ? cause.message : t('modelAuthorization.failed'));

  useEffect(() => {
    if (!open) return;
    let active = true;
    setId(provider?.id ?? ''); setName(provider?.name ?? 'ChatGPT'); setStatus(undefined); setError(''); setPaused(false);
    if (provider) {
      setBusy(true);
      void api.modelAuthorization(provider.id, 'status').then(value => { if (active) setStatus(value); }).catch(cause => { if (active) report(cause); }).finally(() => { if (active) setBusy(false); });
    } else setBusy(false);
    return () => { active = false; };
  }, [open, provider?.id, api]); // Read a fresh persisted flow whenever reopened.

  useEffect(() => {
    if (!open || !id || status?.state !== 'pending' || paused) return;
    let active = true;
    const timer = window.setTimeout(() => {
      void api.modelAuthorization(id, 'poll').then(value => {
        if (!active) return;
        setStatus(value);
        if (value.state === 'ready') invalidate();
      }).catch(cause => { if (active) { report(cause); setPaused(true); } });
    }, Math.max(1, status.interval_seconds ?? 5) * 1000);
    return () => { active = false; window.clearTimeout(timer); };
  }, [open, id, status, paused, api]);

  const start = async () => {
    setBusy(true); setError(''); setPaused(false);
    try {
      const providerId = id || (await api.createChatGPTProvider(name.trim())).id;
      setId(providerId); invalidate();
      setStatus(await api.modelAuthorization(providerId, 'start'));
      invalidate();
    } catch (cause) { report(cause); }
    finally { setBusy(false); }
  };
  const cancel = async () => {
    setBusy(true); setError(''); setPaused(true);
    try { await api.cancelModelAuthorization(id); invalidate(); onClose(); }
    catch (cause) { report(cause); }
    finally { setBusy(false); }
  };
  const pending = status?.state === 'pending';
  return <Modal open={open} onClose={() => { if (!busy) onClose(); }} title={t('modelAuthorization.title')}>
    <div className={styles.body}>
      <div className={styles.identity}><ProviderIcon kind="openai" name="ChatGPT" /><div><strong>ChatGPT</strong><p>{t('modelAuthorization.description')}</p></div></div>
      {!id && <TextField label={t('modelAuthorization.name')} value={name} maxLength={120} onChange={event => setName(event.target.value)} />}
      {status && <p role="status">{t(`modelAuthorization.state.${status.state}`)}{status.login && <span className={styles.login}>{status.login}</span>}</p>}
      {pending && <div className={styles.authorization}>
        <p>{t('modelAuthorization.enterCode')}</p><code>{status.user_code}</code>
        {status.verification_uri === 'https://auth.openai.com/codex/device' && <a href={status.verification_uri} target="_blank" rel="noopener noreferrer">{t('modelAuthorization.openProvider')} ↗</a>}
        <p className={styles.hint}>{t('modelAuthorization.enableDevice')}</p>
      </div>}
      {status?.state === 'ready' && <p>{t('modelAuthorization.chooseModels')}</p>}
      {error && <div role="alert" className={styles.error}>{error}</div>}
      <div className={styles.actions}>
        {pending ? <><Button variant="ghost" onClick={() => void cancel()} loading={busy}>{t('modelAuthorization.cancel')}</Button>{paused && <Button onClick={() => { setError(''); setPaused(false); }}>{t('modelAuthorization.retry')}</Button>}</> : status?.state === 'ready' ? <><Button variant="ghost" onClick={() => void start()} loading={busy}>{t('modelAuthorization.reauthorize')}</Button><Button onClick={onClose}>{t('modelAuthorization.done')}</Button></> : <Button onClick={() => void start()} loading={busy} disabled={!name.trim()}>{id ? t('modelAuthorization.reauthorize') : t('modelAuthorization.connect')}</Button>}
      </div>
    </div>
  </Modal>;
}
