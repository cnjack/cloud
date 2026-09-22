import { useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAccountNavigation } from '../work-home/useAccountNavigation';
import { ConversationRail } from '../work-home/ConversationRail';
import { AccountHeader } from './AccountHeader';
import styles from './UtilityWorkspace.module.css';

/** Shared account navigation for utility routes; data stays principal-scoped. */
export function UtilityWorkspace({ children, ownsHeader = false }: { children: ReactNode; ownsHeader?: boolean }) {
  const navigation = useAccountNavigation();
  const { pathname } = useLocation();
  const { t } = useTranslation();
  const sectionTitle = pathname.startsWith('/devices') ? t('cloudRefresh.remoteDevices') : pathname.startsWith('/cluster') || pathname.startsWith('/system') ? t('accountHeader.clusterSettings') : t('accountHeader.account');
  const [collapsed, setCollapsed] = useState(false);
  return <div className={styles.frame} data-collapsed={collapsed || undefined} data-testid="utility-workspace">
    <ConversationRail {...navigation} collapsed={collapsed} onCollapsedChange={setCollapsed} />
    <section className={styles.surface} data-owns-header={ownsHeader || undefined}>
      {!ownsHeader && <AccountHeader sectionTitle={sectionTitle} />}
      <div className={styles.content}>{children}</div>
    </section>
  </div>;
}
