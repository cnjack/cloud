import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { Device } from '@jcloud/device-ui';

/** Missing models must explain how to recover, rather than leave Send inert. */
export function DeviceModelNotice({ device }: { device: Device }) {
  const { t } = useTranslation();
  if (!Array.isArray(device.capabilities?.models) || device.capabilities.models.length > 0) return null;
  return <p role="status" style={{ fontSize: 12, lineHeight: 1.7, color: 'var(--color-text-muted)', margin: '8px 0' }}>{t('cloudRemoteInspector.noModels')} <Link to="/account/settings?section=models" style={{ textDecoration: 'underline' }}>{t('cloudRemoteInspector.modelSettings')}</Link></p>;
}
