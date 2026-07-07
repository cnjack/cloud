/*
 * IdentityChip — the current principal in the header: avatar + display name +
 * (for a cluster admin) a role badge. Clicking opens a menu with the linked
 * identities, "Link …" affordances for still-unlinked configured providers, and
 * Sign out (M4, multitenant blueprint §5).
 *
 * The CONSOLE_TOKEN service principal (is_service) has no identities to link —
 * it shows "console token · CLUSTER ADMIN" and just a sign-out.
 *
 * A null `me` (the shell rendered without the auth gate, e.g. in unit tests)
 * falls back to a plain role label so the header still renders.
 */
import { useEffect, useRef, useState } from 'react';
import type { Role } from '../api/config';
import type { AuthProviderInfo, Me } from '../api/types';
import styles from './IdentityChip.module.css';

const ROLE_LABEL: Record<Role, string> = {
  'cluster-admin': 'Cluster admin',
  'project-admin': 'Project admin',
};

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0]!.charAt(0).toUpperCase();
  return (parts[0]!.charAt(0) + parts[parts.length - 1]!.charAt(0)).toUpperCase();
}

function Avatar({ me }: { me: Me }) {
  if (me.user.avatar_url) {
    return <img className={styles.avatar} src={me.user.avatar_url} alt="" aria-hidden />;
  }
  return (
    <span className={styles.avatarFallback} aria-hidden data-service={me.is_service || undefined}>
      {me.is_service ? '⌘' : initials(me.user.display_name)}
    </span>
  );
}

export function IdentityChip({
  me,
  providers = [],
  role,
  onSignOut,
}: {
  me: Me | null;
  providers?: AuthProviderInfo[];
  /** Fallback role label when `me` is absent (shell without the auth gate). */
  role: Role;
  /** When provided (real sessions, not demo), renders a sign-out affordance. */
  onSignOut?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  // Fallback: no principal (e.g. the shell in a bare unit test). Keep the old
  // trust-level chip so the header still names a role.
  if (!me) {
    return (
      <span className={styles.chip} data-testid="identity-chip" data-role={role} title={ROLE_LABEL[role]}>
        <span className={styles.dot} aria-hidden />
        <span className={styles.role}>{ROLE_LABEL[role]}</span>
        {onSignOut && (
          <button type="button" className={styles.signOutInline} onClick={onSignOut} data-testid="sign-out">
            Sign out
          </button>
        )}
      </span>
    );
  }

  const linked = new Set(me.identities.map((i) => i.provider));
  const linkable = me.is_service ? [] : providers.filter((p) => !linked.has(p.id));
  const badge = me.is_service
    ? 'Cluster admin'
    : me.user.is_cluster_admin
      ? 'Cluster admin'
      : null;

  return (
    <div className={styles.wrap} ref={ref}>
      <button
        type="button"
        className={styles.trigger}
        data-testid="identity-chip"
        data-service={me.is_service || undefined}
        data-cluster-admin={me.user.is_cluster_admin || undefined}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Avatar me={me} />
        <span className={styles.name}>{me.user.display_name}</span>
        {badge && <span className={styles.badge}>{badge}</span>}
        <span className={styles.caret} aria-hidden>
          ▾
        </span>
      </button>

      {open && (
        <div className={styles.menu} role="menu" data-testid="identity-menu">
          <div className={styles.menuHead}>
            <Avatar me={me} />
            <div className={styles.menuHeadText}>
              <span className={styles.menuName}>{me.user.display_name}</span>
              {badge && <span className={styles.menuBadge}>{badge}</span>}
            </div>
          </div>

          {me.is_service ? (
            <div className={styles.section}>
              <span className={styles.sectionLabel}>Session</span>
              <span className={styles.serviceNote}>
                Authenticated with the console token — no linked identities.
              </span>
            </div>
          ) : (
            <>
              <div className={styles.section} data-testid="linked-identities">
                <span className={styles.sectionLabel}>Linked accounts</span>
                {me.identities.length === 0 ? (
                  <span className={styles.serviceNote}>No linked accounts yet.</span>
                ) : (
                  me.identities.map((id) => (
                    <div className={styles.identity} key={`${id.provider}:${id.username}`}>
                      <span className={styles.identityIcon} aria-hidden>
                        {id.provider.charAt(0).toUpperCase()}
                      </span>
                      <span className={styles.identityName}>{id.username}</span>
                      <span className={styles.identityProvider}>{id.provider}</span>
                    </div>
                  ))
                )}
              </div>

              {linkable.length > 0 && (
                <div className={styles.section} data-testid="linkable-providers">
                  <span className={styles.sectionLabel}>Link another</span>
                  {linkable.map((p) => (
                    <a
                      key={p.id}
                      className={styles.menuItem}
                      href={`/auth/link/${p.id}`}
                      data-testid={`link-${p.id}`}
                      role="menuitem"
                    >
                      <span className={styles.identityIcon} aria-hidden>
                        {p.name.charAt(0)}
                      </span>
                      Link {p.name}
                    </a>
                  ))}
                </div>
              )}
            </>
          )}

          {onSignOut && (
            <button
              type="button"
              className={styles.signOut}
              onClick={() => {
                setOpen(false);
                onSignOut();
              }}
              data-testid="sign-out"
              role="menuitem"
            >
              Sign out
            </button>
          )}
        </div>
      )}
    </div>
  );
}
