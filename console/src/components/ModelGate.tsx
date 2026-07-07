/*
 * ModelGate — the shared fail-visible gate for every run-triggering surface
 * (project composer, run Retry, PR "Request AI review"). One hook resolves the
 * cluster model status and hands back a `configured` flag (to disable the
 * action) plus a ready-to-render `notice` explaining why / where to fix it.
 *
 * Deliberately NOT EmptyState: EmptyState is a large centered placeholder for
 * a whole empty region, while this is a compact `role="alert"` strip that sits
 * inline next to still-visible live controls on three different surfaces — a
 * shared small component keeps the three consistent without inflating them.
 */
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useModelConfig } from '../api/queries';
import { useRole } from '../api/ApiProvider';
import styles from './ModelGate.module.css';

export interface ModelGate {
  /**
   * False ONLY when the backend positively reports no model configured. While
   * loading — and when the status query itself fails — this stays true so the
   * action remains usable (the backend's 409 is the authoritative gate).
   */
  configured: boolean;
  /** Warning to render near the gated action; null when there is nothing to say. */
  notice: ReactNode;
}

export function useModelGate(enabled = true): ModelGate {
  const q = useModelConfig(enabled);
  const isClusterAdmin = useRole() === 'cluster-admin';

  if (!enabled) return { configured: true, notice: null };

  // Status UNVERIFIED (query error): keep the action enabled — the backend 409
  // still backstops — but never silently pretend everything is fine.
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
            Couldn’t verify the model status — runs may fail if no LLM is configured.
          </span>
        </div>
      ),
    };
  }

  // Optimistic while loading (avoids a disabled flash on every page view).
  const configured = q.data?.configured ?? true;
  if (configured) return { configured: true, notice: null };

  return {
    configured: false,
    notice: (
      <div className={styles.notice} role="alert" data-testid="model-not-configured">
        <span className={styles.text}>No LLM is configured, so runs can’t start.</span>
        {isClusterAdmin ? (
          <Link to="/system" className={styles.link} data-testid="model-config-link">
            Configure it on the Cluster page →
          </Link>
        ) : (
          <span className={styles.text}>
            Contact a cluster administrator to configure a model.
          </span>
        )}
      </div>
    ),
  };
}
