/*
 * IdentityChip — names the current principal + role in the header.
 *
 * The MVP is single-tenant: ONE static console token, no OIDC/RBAC yet. Since
 * the login gate landed, holding this token is a VERIFIED trust level (the
 * session probed GET /api/v1/system successfully) — but it is still not
 * per-user identity or per-request authorization, and the tooltip says so
 * plainly. When OIDC lands, the principal/role resolve from the identity claim
 * instead.
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

export function IdentityChip({
  role,
  onSignOut,
}: {
  role: Role;
  /** When provided (real sessions, not demo), renders a sign-out affordance. */
  onSignOut?: () => void;
}) {
  const label = ROLE_LABEL[role];
  return (
    <span
      className={styles.chip}
      data-testid="identity-chip"
      data-role={role}
      title={`Signed in with the ${PRINCIPAL} (${label}) — verified against the orchestrator. Single-tenant MVP: OIDC identities and RBAC roles are on the roadmap; this names the session's trust level, not per-request authz.`}
    >
      <span className={styles.dot} aria-hidden />
      <span className={styles.principal}>{PRINCIPAL}</span>
      <span className={styles.sep} aria-hidden>
        ·
      </span>
      <span className={styles.role}>{label}</span>
      {onSignOut && (
        <button
          type="button"
          className={styles.signOut}
          onClick={onSignOut}
          data-testid="sign-out"
          title="Sign out (clears the token stored in this browser)"
        >
          Sign out
        </button>
      )}
    </span>
  );
}
