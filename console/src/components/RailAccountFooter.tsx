import { useTranslation } from 'react-i18next';
import type { Role } from '../api/config';
import type { AuthProviderInfo, Me } from '../api/types';
import { IdentityChip } from './IdentityChip';
import { CONSOLE_VERSION } from '../version';
import styles from './RailAccountFooter.module.css';

export function RailAccountFooter({
  demo,
  me,
  providers,
  role,
  onSignOut,
  testId,
}: {
  demo: boolean;
  me: Me | null;
  providers: AuthProviderInfo[];
  role: Role;
  onSignOut?: () => void;
  testId?: string;
}) {
  const { t } = useTranslation();

  return (
    <div className={styles.footer} data-testid={testId}>
      <div className={styles.environment}>
        <span>{demo ? t('shell.demoEnv') : t('shell.orchestratorEnv')}</span>
        <span>{CONSOLE_VERSION}</span>
      </div>
      <div className={styles.identityRow}>
        <IdentityChip me={me} providers={providers} role={role} onSignOut={onSignOut} />
      </div>
    </div>
  );
}
