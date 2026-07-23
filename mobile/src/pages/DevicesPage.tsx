import { Devices, GearSix, Question } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { ApiError, useDevices, type Device } from '@jcloud/device-ui';
import { timeAgo } from '../lib/time';

/** DevicesPage — the app's home: every connected jcode device as a card. */
export function DevicesPage() {
  const { t } = useTranslation();
  const devices = useDevices();

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="topbar-title">{t('device.list.title')}</div>
        <Link to="/guide" className="topbar-back" aria-label={t('device.guide.entry')} data-testid="guide-entry">
          <Question size={18} />
        </Link>
        <Link
          to="/settings"
          className="topbar-back"
          aria-label={t('mobile.settings.title')}
          data-testid="settings-entry"
        >
          <GearSix size={18} />
        </Link>
      </header>

      <div className="content content-pad-bottom" data-testid="devices-page">
        {devices.isLoading ? (
          <p className="state-block">{t('mobile.common.loading')}</p>
        ) : devices.isError ? (
          <div className="state-block" role="alert">
            <p>{t('mobile.devices.loadError')}</p>
            {devices.error instanceof ApiError && devices.error.status === 400 && (
              <p>{t('device.list.servicePrincipal')}</p>
            )}
          </div>
        ) : (devices.data?.length ?? 0) === 0 ? (
          <div className="state-block" data-testid="devices-empty">
            <p><Devices size={28} aria-hidden /></p>
            <p><strong>{t('mobile.devices.emptyTitle')}</strong></p>
            <p>{t('mobile.devices.emptyBody')}</p>
          </div>
        ) : (
          <div className="card-list">
            {(devices.data ?? []).map((device) => (
              <DeviceCard key={device.id} device={device} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function DeviceCard({ device }: { device: Device }) {
  const { t } = useTranslation();
  return (
    <Link to={`/devices/${device.id}`} className="device-card" data-testid="device-card">
      <div className="device-card-head">
        <h2 className="device-card-name">{device.name}</h2>
        <span className="pill" data-tone={device.online ? 'success' : 'neutral'}>
          {device.online ? t('mobile.devices.online') : t('mobile.devices.offline')}
        </span>
      </div>
      <div className="device-card-meta">
        {device.hostname && <span>{device.hostname}</span>}
        {device.jcode_version && <span>jcode {device.jcode_version}</span>}
        <span>
          {device.last_seen_at
            ? t('mobile.devices.lastSeen', { time: timeAgo(device.last_seen_at) })
            : t('mobile.devices.neverSeen')}
        </span>
      </div>
    </Link>
  );
}
