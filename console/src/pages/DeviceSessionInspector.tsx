import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useDeviceApi, type Device, type DeviceSession, type DeviceWorkspaceChangedFile } from '@jcloud/device-ui';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { ErrorBlock, LoadingBlock } from '../components/States';
import styles from './DeviceSessionInspector.module.css';

// This component is mounted only inside DevicePairingGate, after session lookup.
export function DeviceSessionInspector({ device, session, online }: { device: Device; session: DeviceSession; online: boolean }) {
  const { t } = useTranslation();
  const api = useDeviceApi();
  const [tab, setTab] = useState<'changes' | 'details'>('changes');
  const [selected, setSelected] = useState<DeviceWorkspaceChangedFile | null>(null);
  const [reviewOpen, setReviewOpen] = useState(false);
  const prepare = useMutation({ mutationFn: () => api.prepareWorkspaceDraftPR(device.id, session.session_id) });
  const review = prepare.data ?? null;
  const [paths, setPaths] = useState<string[]>([]);
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const deliver = useMutation({ mutationFn: () => api.createWorkspaceDraftPR(device.id, session.session_id, { repository_url: review!.remote_url, revision: review!.revision, base_sha: review!.base_sha, paths, title: title.trim(), body }) });
  const deliverySupported = device.capabilities?.workspace_actions?.includes('draft_pr') === true;
  const supported = device.capabilities?.workspace_actions?.includes('changes') === true;
  const changes = useQuery({
    queryKey: ['device-workspace-changes', device.id, session.session_id],
    queryFn: () => api.inspectWorkspace(device.id, session.session_id),
    enabled: supported && online,
    retry: false,
    staleTime: 10_000,
    gcTime: 0,
  });
  const files = changes.data?.files ?? [];
  return <aside className={styles.inspector} aria-label={t('cloudRemoteInspector.title')}>
    <div className={styles.tabs} role="tablist" aria-label={t('cloudRemoteInspector.title')}>
      {(['changes', 'details'] as const).map((name) => <button key={name} role="tab" aria-selected={tab === name} aria-controls={`remote-${name}`} id={`remote-${name}-tab`} tabIndex={tab === name ? 0 : -1} onKeyDown={(event) => {
        if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); const next = name === 'changes' ? 'details' : 'changes'; setTab(next); document.getElementById(`remote-${next}-tab`)?.focus(); }
      }} onClick={() => setTab(name)}>{t(`cloudRemoteInspector.${name}`)}</button>)}
    </div>
    {tab === 'changes' ? <section id="remote-changes" role="tabpanel" aria-labelledby="remote-changes-tab" className={styles.panel}>
      {!supported ? <p className={styles.notice}>{t('cloudRemoteInspector.upgradeRequired')} <Link to="/devices/guide">{t('cloudRemoteInspector.setup')}</Link></p> : <>
        <div className={styles.summary}><strong>{t('cloudRemoteInspector.files', { count: files.length })}</strong><Button variant="ghost" size="sm" disabled={!online || changes.isFetching} onClick={() => void changes.refetch()}>{t('cloudRemoteInspector.refresh')}</Button></div>
        {!online && <p className={styles.notice}>{t('cloudRemoteInspector.offline')}</p>}
        {changes.isFetching && !changes.data && <LoadingBlock />}
        {changes.isError && <ErrorBlock error={changes.error} onRetry={online ? () => void changes.refetch() : undefined} />}
        {changes.data && <>
          <p className={styles.caption}>{t('cloudRemoteInspector.workspaceChanges')}</p>
          <div className={styles.files}>{files.map((file) => <button key={file.path} className={styles.file} onClick={() => setSelected(file)}><span className={styles.path}>{file.path}</span><span className={styles.count}><b>+{file.additions}</b> −{file.deletions}</span></button>)}</div>
          {files.length === 0 && <p className={styles.notice}>{t('cloudRemoteInspector.clean')}</p>}
          {changes.data.truncated && <p className={styles.notice} role="status">{t('cloudRemoteInspector.truncated')}</p>}
          <Button variant="secondary" size="sm" disabled={!online || !deliverySupported} onClick={() => { setReviewOpen(true); prepare.reset(); prepare.mutate(); setPaths([]); setTitle(session.meta?.title || ''); setBody(''); deliver.reset(); }}>{t('cloudRemoteInspector.createDraft')}</Button>
          {!deliverySupported && <p className={styles.caption}>{t('cloudRemoteInspector.deliveryUpgrade')} <Link to="/devices/guide">{t('cloudRemoteInspector.setup')}</Link></p>}
          <p className={styles.caption}>{t('cloudRemoteInspector.updated', { time: new Date(changes.dataUpdatedAt).toLocaleTimeString() })}</p>
        </>}
      </>}
    </section> : <section id="remote-details" role="tabpanel" aria-labelledby="remote-details-tab" className={styles.panel}>
      <dl className={styles.details}>
        <dt>{t('cloudRemoteInspector.device')}</dt><dd>{device.name}</dd>
        <dt>{t('cloudRemoteInspector.workspace')}</dt><dd>{session.meta?.project || '—'}</dd>
        <dt>{t('cloudRemoteInspector.branch')}</dt><dd>{changes.data?.branch || '—'}</dd>
        <dt>{t('cloudRemoteInspector.model')}</dt><dd>{[session.meta?.provider, session.meta?.model].filter(Boolean).join(' / ') || '—'}</dd>
        <dt>{t('cloudRemoteInspector.connection')}</dt><dd>{online ? t('cloudRemoteInspector.online') : t('cloudRemoteInspector.offlineLabel')}</dd>
        <dt>{t('cloudRemoteInspector.version')}</dt><dd>{device.jcode_version || '—'}</dd>
      </dl>
      <Link to={`/devices/${encodeURIComponent(device.id)}`}>{t('cloudRemoteInspector.openDevice')}</Link>
    </section>}
    <Modal open={reviewOpen} onClose={() => { if (!deliver.isPending) setReviewOpen(false); }} title={t('cloudRemoteInspector.createDraft')} size="wide" footer={<>
      <Button variant="secondary" disabled={deliver.isPending} onClick={() => setReviewOpen(false)}>{t('cloudRemoteInspector.close')}</Button>
      {!deliver.isSuccess && <Button disabled={!online || !review || review.truncated || !review.revision || prepare.isPending || deliver.isPending || !title.trim() || paths.length === 0} onClick={() => deliver.mutate()}>{deliver.isPending ? t('cloudRemoteInspector.delivering') : t('cloudRemoteInspector.publishDraft')}</Button>}
    </>}>
      {prepare.isPending ? <LoadingBlock /> : prepare.isError ? <ErrorBlock error={prepare.error} onRetry={() => prepare.mutate()} /> : deliver.isSuccess ? <div className={styles.deliverySuccess}><p>{t('cloudRemoteInspector.delivered')}</p><a href={deliver.data.url} target="_blank" rel="noreferrer">{t('cloudRemoteInspector.openPR')}</a><code>{deliver.data.branch}</code></div> : <div className={styles.deliveryForm}>
        <p>{t('cloudRemoteInspector.deliveryExplanation')}</p>
        <p className={styles.caption}>{review?.remote_url || review?.repository} · {t('cloudRemoteInspector.defaultBase')} ({review?.base_branch})</p>
        <p className={styles.caption}>{t('cloudRemoteInspector.fullPreview')}</p>
        {review?.truncated && <p role="alert">{t('cloudRemoteInspector.truncated')}</p>}
        <label>{t('cloudRemoteInspector.prTitle')}<input value={title} maxLength={200} disabled={deliver.isPending} onChange={(event) => setTitle(event.target.value)} /></label>
        <label>{t('cloudRemoteInspector.prBody')}<textarea value={body} maxLength={20000} disabled={deliver.isPending} onChange={(event) => setBody(event.target.value)} /></label>
        <fieldset disabled={deliver.isPending}><legend>{t('cloudRemoteInspector.selectFiles')}</legend>{review?.files.map((file) => <div className={styles.reviewFile} key={file.path}>
          <label><input type="checkbox" checked={paths.includes(file.path)} onChange={(event) => setPaths((current) => event.target.checked ? [...current, file.path] : current.filter((path) => path !== file.path))} />{file.path}</label>
          <details><summary>{t('cloudRemoteInspector.viewPatch')}</summary>{file.binary ? <p>{t('cloudRemoteInspector.binary')}</p> : <pre className={styles.patch}>{file.patch}</pre>}</details>
        </div>)}</fieldset>
        {deliver.isError && <ErrorBlock error={deliver.error} />}
        {!online && <p role="alert">{t('cloudRemoteInspector.offline')}</p>}
      </div>}
    </Modal>
    <Modal open={selected !== null} onClose={() => setSelected(null)} title={selected?.path ?? ''} size="wide">
      {selected?.binary ? <p>{t('cloudRemoteInspector.binary')}</p> : <pre className={styles.patch}>{selected?.patch}</pre>}
      {selected?.truncated && <p className={styles.notice}>{t('cloudRemoteInspector.truncated')}</p>}
    </Modal>
  </aside>;
}
