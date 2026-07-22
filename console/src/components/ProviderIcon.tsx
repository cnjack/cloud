import { iconForDeviceProvider } from '@jcloud/device-ui';
import styles from './ProviderIcon.module.css';

export function ProviderIcon({ kind, name }: { kind: string; name: string }) {
  const identity = `${kind} ${name}`;
  const svg = iconForDeviceProvider(identity);
  const initial = identity.replace(/[^a-z0-9]/gi, '').charAt(0).toUpperCase() || '?';
  return (
    <span className={styles.wrap} aria-hidden="true">
      {svg ? <span className={styles.svg} dangerouslySetInnerHTML={{ __html: svg }} /> : <span className={styles.fallback}>{initial}</span>}
    </span>
  );
}
