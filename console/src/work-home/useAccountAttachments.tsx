import { Paperclip, X } from '@phosphor-icons/react';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useApi } from '../api/ApiProvider';
import type { AccountRepositoryTarget, RunAttachmentIntent } from '../api/types';
import styles from './WorkHomePage.module.css';

type Upload = { id: number; file: File; intent?: RunAttachmentIntent; error?: string; pending: boolean };

export function useAccountAttachments(target: AccountRepositoryTarget, disabled: boolean) {
  const api = useApi();
  const { t } = useTranslation();
  const input = useRef<HTMLInputElement>(null);
  const nextID = useRef(0);
  const [byRepository, setByRepository] = useState<Record<string, Upload[]>>({});
  const [validation, setValidation] = useState('');
  const [now, setNow] = useState(Date.now());
  const key = `${target.provider}:${target.provider_repo_id}`;
  const uploads = byRepository[key] ?? [];
  useEffect(() => { setValidation(''); }, [key]);
  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 15_000); return () => clearInterval(timer); }, []);
  const expired = (upload: Upload) => !!upload.intent && Date.parse(upload.intent.expires_at) <= now;
  const update = (id: number, patch: Partial<Upload>) => setByRepository(current => ({ ...current,
    [key]: (current[key] ?? []).map(upload => upload.id === id ? { ...upload, ...patch } : upload),
  }));
  const uploadFile = async (upload: Upload) => {
    update(upload.id, { pending: true, error: undefined, intent: undefined });
    try {
      const intent = await api.uploadAccountAttachment(target.provider, target.provider_repo_id, upload.file);
      update(upload.id, { intent, pending: false });
    } catch (error) { update(upload.id, { pending: false, error: error instanceof Error ? error.message : t('cloudRefresh.uploadFailed') }); }
  };
  const pending = uploads.some(upload => upload.pending);
  const blocked = !!validation || uploads.some(upload => upload.pending || upload.error || !upload.intent || expired(upload));
  const add = (files: File[]) => {
    if (!files.length) return;
    if (files.length + uploads.length > 10 || files.some(file => file.size === 0 || file.size > 25 * 1024 * 1024)
      || [...uploads.map(upload => upload.file), ...files].reduce((total,file) => total + file.size, 0) > 100 * 1024 * 1024) {
      setValidation(t('cloudRefresh.attachmentLimits')); return;
    }
    setValidation('');
    const next = files.map(file => ({ id: ++nextID.current, file, pending: true }));
    setByRepository(current => ({ ...current, [key]: [...(current[key] ?? []), ...next] }));
    next.forEach(upload => { void uploadFile(upload); });
  };
  return {
    blocked,
    stageIDs: uploads.flatMap(upload => upload.intent ? [upload.intent.stage.id] : []),
    clear: () => setByRepository(current => ({ ...current, [key]: [] })),
    view: <div className={styles.attachmentArea}>
      <input ref={input} type="file" multiple hidden aria-label={t('cloudRefresh.addAttachments')} disabled={disabled || pending}
        onChange={event => { add(Array.from(event.target.files ?? [])); event.target.value = ''; }} />
      <button className={styles.attachmentButton} type="button" disabled={disabled || pending} onClick={() => input.current?.click()}><Paperclip size={16}/>{t('cloudRefresh.addAttachments')}</button>
      {!!uploads.length && <ul className={styles.attachments}>{uploads.map(upload => <li key={upload.id}>
        <span>{upload.file.name}</span><small>{upload.pending ? t('cloudRefresh.uploading') : expired(upload) ? t('cloudRefresh.attachmentExpired') : upload.error || t('cloudRefresh.attachmentReady')}</small>
        {(upload.error || expired(upload)) && <button type="button" disabled={disabled} onClick={() => void uploadFile(upload)}>{t('common.retry')}</button>}
        <button type="button" disabled={disabled || upload.pending} aria-label={t('cloudRefresh.removeAttachment', {name:upload.file.name})} onClick={() => setByRepository(current => ({...current,[key]:(current[key] ?? []).filter(item => item.id !== upload.id)}))}><X size={14}/></button>
      </li>)}</ul>}
      {validation && <p role="alert">{validation} <button type="button" onClick={() => setValidation('')}>{t('common.dismiss')}</button></p>}
      {uploads.some(upload => upload.error) && <p role="alert">{t('cloudRefresh.uploadFailed')}</p>}
    </div>,
  };
}
