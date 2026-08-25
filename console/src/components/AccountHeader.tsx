import { CaretDown, GitPullRequest, HardDrives, Lightning, SignOut, User } from '@phosphor-icons/react';
import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useRole } from '../api/ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import { Wordmark } from './Wordmark';
import styles from './AccountHeader.module.css';

/** The single global header for Work Home, account, review, and admin surfaces. */
export function AccountHeader() {
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

  const name = auth?.me?.user.display_name || 'Account';
  return (
    <header className={styles.header}>
      <Wordmark />
      <div className={styles.account} ref={ref}>
        <button type="button" className={styles.trigger} aria-label="Account menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
          <span className={styles.avatar}>{name.slice(0, 2).toUpperCase()}</span><span>{name}</span><CaretDown size={12} />
        </button>
        {open && <div className={styles.menu} role="menu">
          <Link to="/code-reviews" role="menuitem"><GitPullRequest size={15} />Code reviews</Link>
          <Link to="/account/settings" role="menuitem"><User size={15} />Personal settings</Link>
          <Link to="/account/settings?section=usage" role="menuitem"><Lightning size={15} />Account usage</Link>
          {role === 'cluster-admin' && <Link to="/cluster" role="menuitem"><HardDrives size={15} />Cluster settings<span className={styles.admin}>Admin</span></Link>}
          {auth?.logout && <button type="button" role="menuitem" onClick={() => void auth.logout()}><SignOut size={15} />Sign out</button>}
        </div>}
      </div>
    </header>
  );
}
