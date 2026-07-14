/*
 * ModelGate — the shared fail-visible gate for every run-triggering surface
 * (project composer, run Retry, PR "Request AI review"). One hook resolves the
 * PROJECT's model status (D21) and hands back a `configured` flag (to disable the
 * action) plus a ready-to-render `notice` explaining why / where to fix it.
 *
 * A project is "configured" when it has at least one granted model OR the MODEL_*
 * env fallback is active (empty catalog / local rig). When neither holds, runs
 * cannot start — the notice tells an admin to grant a model and a member to
 * contact one. Every gated surface passes its projectId so the gate is scoped to
 * the project the action would run against.
 *
 * Deliberately NOT EmptyState: EmptyState is a large centered placeholder for a
 * whole empty region, while this is a compact `role="alert"` strip that sits
 * inline next to still-visible live controls on three different surfaces.
 */
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { ArrowRight } from '@phosphor-icons/react';
import { useProjectModels } from '../api/queries';
import { useRole } from '../api/ApiProvider';
import styles from './ModelGate.module.css';

export interface ModelGate {
  /**
   * False ONLY when the backend positively reports the project has no usable
   * model. While loading — and when the status query itself fails — this stays
   * true so the action remains usable (the backend's typed error is the
   * authoritative gate).
   */
  configured: boolean;
  /** Warning to render near the gated action; null when there is nothing to say. */
  notice: ReactNode;
}

export function useModelGate(projectId: string, enabled = true): ModelGate {
  const q = useProjectModels(projectId, enabled);
  const isClusterAdmin = useRole() === 'cluster-admin';
  const { t } = useTranslation();

  if (!enabled) return { configured: true, notice: null };

  // Status UNVERIFIED (query error): keep the action enabled — the backend's
  // typed error still backstops — but never silently pretend everything is fine.
  if (q.isError) {
    return {
      configured: true,
      notice: (
        <div
          className={styles.notice}
          data-variant="unverified"
          role="status"
          data-testid="model-unverified"
        >
          <span className={styles.text}>
            {t('components.modelGate.unverified')}
          </span>
        </div>
      ),
    };
  }

  // Optimistic while loading (avoids a disabled flash on every page view).
  if (!q.data) return { configured: true, notice: null };

  const configured = q.data.models.length > 0 || q.data.env_fallback;
  if (configured) return { configured: true, notice: null };

  return {
    configured: false,
    notice: (
      <div className={styles.notice} role="alert" data-testid="model-not-configured">
        <span className={styles.text}>{t('components.modelGate.notConfigured')}</span>
        {isClusterAdmin ? (
          <Link to="/system" className={styles.link} data-testid="model-config-link">
            <span>{t('components.modelGate.grantLink')}</span>
            <ArrowRight size={15} weight="regular" aria-hidden="true" />
          </Link>
        ) : (
          <span className={styles.text}>
            {t('components.modelGate.contactAdmin')}
          </span>
        )}
      </div>
    ),
  };
}
