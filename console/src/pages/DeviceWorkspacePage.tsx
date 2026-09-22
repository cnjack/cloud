import { ArrowLeft, ArrowRight, Desktop, LockKey, Plus } from '@phosphor-icons/react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { DevicePairingGate, useDevices, useDeviceSessions, type Device } from '@jcloud/device-ui';
import { EmptyState } from '../components/EmptyState';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { timeAgo } from '../lib/format';
import { RemoteComposer } from '../work-home/RemoteComposer';
import styles from './DeviceWorkspacePage.module.css';

export function DeviceListPage() {
  const { t } = useTranslation();
  const devices = useDevices();
  const [query, setQuery] = useState('');
  const visible = (devices.data ?? []).filter((device) => `${device.name} ${device.hostname ?? ''} ${device.platform ?? ''}`.toLowerCase().includes(query.trim().toLowerCase()));
  const connect = <Link className={styles.primary} to="/devices/guide"><Plus size={16} />{t('cloudRefresh.connectDevice')}</Link>;
  return <main className={styles.page} data-testid="device-list">
    <header className={styles.heading}><div><h1>{t('cloudRefresh.devicesTitle')}</h1><p>{t('cloudRefresh.devicesDescription')}</p></div>{connect}</header>
    <div className={styles.toolbar}><input type="search" aria-label={t('cloudRefresh.searchDevices')} placeholder={t('cloudRefresh.searchDevices')} value={query} onChange={(event) => setQuery(event.target.value)} /><button type="button" onClick={() => void devices.refetch()} disabled={devices.isFetching}>{t('cluster.overview.refresh')}</button></div>
    {devices.isPending ? <LoadingBlock /> : devices.isError ? <ErrorBlock error={devices.error} onRetry={() => void devices.refetch()} /> : !devices.data?.length ? <EmptyState icon={<Desktop size={30} />} title={t('cloudRefresh.noDevices')} description={t('cloudRefresh.noDevicesDescription')} action={connect} /> : !visible.length ? <EmptyState title={t('cloudRefresh.noMatchingDevices')} description={t('cloudRefresh.noMatchDescription')} action={<button type="button" onClick={() => setQuery('')}>{t('cloudRefresh.clearFilters')}</button>} /> : <div className={styles.devices}>{visible.map((device) => <Link key={device.id} className={styles.device} to={`/devices/${encodeURIComponent(device.id)}`}>
      <span className={styles.deviceIcon}><Desktop size={25} /></span><span className={styles.deviceCopy}><strong>{device.name}</strong><small>{[device.hostname, device.platform, device.jcode_version].filter(Boolean).join(' · ')}</small><span className={styles.status} data-online={device.online}>{t(device.online ? 'repositories.online' : 'device.list.offline')}{!device.online && device.last_seen_at && <time dateTime={device.last_seen_at}> · {timeAgo(device.last_seen_at)}</time>}</span></span><ArrowRight size={17} />
    </Link>)}</div>}
    <p className={styles.security}><LockKey size={15} />{t('cloudRefresh.encryptedSession')}</p>
  </main>;
}

export function DeviceWorkspacePage() {
  const { deviceId = '' } = useParams();
  const { t } = useTranslation();
  const devices = useDevices();
  const device = devices.data?.find((item) => item.id === deviceId);
  return <main className={styles.page} data-testid="device-workspace">
    <Link className={styles.back} to="/devices"><ArrowLeft size={15} />{t('cloudRefresh.backDevices')}</Link>
    {devices.isPending ? <LoadingBlock /> : devices.isError ? <ErrorBlock error={devices.error} onRetry={() => void devices.refetch()} /> : !device ? <EmptyState title={t('cloudRefresh.deviceNotFound')} description={t('cloudRefresh.deviceUnavailable')} action={<Link to="/devices/guide">{t('cloudRefresh.connectDevice')}</Link>} /> : <>
      <header className={styles.heading}><div><h1>{device.name}</h1><p>{[device.hostname, device.platform, device.jcode_version].filter(Boolean).join(' · ')}</p></div><span className={styles.status} data-online={device.online}>{t(device.online ? 'repositories.online' : 'device.list.offline')}</span></header>
      <DevicePairingGate device={device} guideLink={<Link to="/devices/guide">{t('cloudRefresh.connectDevice')}</Link>}><PairedDeviceWorkspace device={device} /></DevicePairingGate>
    </>}
  </main>;
}

// Mount the decrypting session query and composer only after the pairing gate.
function PairedDeviceWorkspace({ device }: { device: Device }) {
  const { t } = useTranslation();
  const sessions = useDeviceSessions(device.id);
  const [query, setQuery] = useState('');
  const visible = (sessions.data ?? []).filter((session) => `${session.meta?.title ?? ''} ${session.meta?.project ?? ''}`.toLowerCase().includes(query.trim().toLowerCase()));
  return <>
    <section className={styles.composer}><h2>{t('cloudRefresh.startRemote')}</h2><RemoteComposer device={device} /></section>
    <section className={styles.sessions}><div className={styles.toolbar}><h2>{t('cloudRefresh.recentSessions')}</h2><input type="search" aria-label={t('cloudRefresh.searchTasks')} placeholder={t('cloudRefresh.searchTasks')} value={query} onChange={(event) => setQuery(event.target.value)} /></div>
      {sessions.isPending ? <LoadingBlock /> : sessions.isError ? <ErrorBlock error={sessions.error} onRetry={() => void sessions.refetch()} /> : !visible.length ? <EmptyState title={t(query ? 'cloudRefresh.noMatch' : 'device.session.emptyHistory')} /> : <ul className={styles.sessionList}>{visible.map((session) => <li key={session.session_id}><Link to={`/devices/${encodeURIComponent(device.id)}/sessions/${encodeURIComponent(session.session_id)}`}><span><strong>{session.meta?.title || t('device.welcome.untitled')}</strong><small>{session.meta?.project}</small></span><small>{session.status === 'running' ? t('cloudRefresh.active') : t('device.welcome.status.idle')}</small>{session.last_activity_at && <time dateTime={session.last_activity_at}>{timeAgo(session.last_activity_at)}</time>}<ArrowRight size={15} /></Link></li>)}</ul>}
    </section>
  </>;
}
