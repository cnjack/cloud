import { CaretDown, GitPullRequest, HardDrives, SignOut, User } from '@phosphor-icons/react';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useRole } from '../api/ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import { LanguageToggle } from './LanguageToggle';
import { Wordmark } from './Wordmark';
import styles from './AccountHeader.module.css';

/** The single global header for Work Home, account, review, and admin surfaces. */
export function AccountHeader() {
  const { t } = useTranslation();
  const auth = useOptionalAuth();
  const role = useRole();
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => { if (!ref.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', escape); };
  }, [open]);

  const name = auth?.me?.user.display_name || t('accountHeader.account');
  return (
    <header className={styles.header}>
      <Wordmark />
      <div className={styles.utilities}>
        <div className={styles.account} ref={ref}>
          <button type="button" className={styles.trigger} aria-label={t('accountHeader.menu')} aria-expanded={open} onClick={() => setOpen((value) => !value)}>
            <span className={styles.avatar}>{name.slice(0, 2).toUpperCase()}</span><span>{name}</span><CaretDown size={12} />
          </button>
          {open && <div className={styles.menu} role="menu">
            <Link to="/code-reviews" role="menuitem"><GitPullRequest size={15} />{t('accountHeader.codeReviews')}</Link>
            <Link to="/account/settings" role="menuitem"><User size={15} />{t('accountHeader.settings')}</Link>
            {role === 'cluster-admin' && <Link to="/cluster" role="menuitem"><HardDrives size={15} />{t('accountHeader.clusterSettings')}<span className={styles.admin}>{t('accountHeader.admin')}</span></Link>}
            {auth?.logout && <button type="button" role="menuitem" onClick={() => void auth.logout()}><SignOut size={15} />{t('accountHeader.signOut')}</button>}
          </div>}
        </div>
        <LanguageToggle />
      </div>
    </header>
  );
}
