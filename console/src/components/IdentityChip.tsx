/*
 * IdentityChip — names the current principal + role in the header.
 *
 * The MVP is single-tenant: ONE static console token, no OIDC/RBAC yet. So this
 * chip is honest about what it can know — it labels the trust level of the token
 * holder (cluster-admin by default, or project-admin when VITE_ROLE=project-admin)
 * rather than faking per-user identity or per-request authorization. The tooltip
 * says so plainly. When OIDC lands, the principal/role resolve from the identity
 * claim instead of an env signal.
 */
import type { Role } from '../api/config';
import styles from './IdentityChip.module.css';

const ROLE_LABEL: Record<Role, string> = {
  'cluster-admin': 'Cluster admin',
  'project-admin': 'Project admin',
};

// The principal is the token holder; without OIDC we have no per-user identity,
// so we name the trust boundary ("console token") rather than invent a username.
const PRINCIPAL = 'console token';

export function IdentityChip({ role }: { role: Role }) {
  const label = ROLE_LABEL[role];
  return (
    <span
      className={styles.chip}
      data-testid="identity-chip"
      data-role={role}
      title={`Signed in with the ${PRINCIPAL} (${label}). Single-tenant MVP — OIDC identities and RBAC roles are on the roadmap; this names the current trust level, not per-request authz.`}
    >
      <span className={styles.dot} aria-hidden />
      <span className={styles.principal}>{PRINCIPAL}</span>
      <span className={styles.sep} aria-hidden>
        ·
      </span>
      <span className={styles.role}>{label}</span>
    </span>
  );
}
