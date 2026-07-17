/*
 * ProjectModelsPanel — the project settings "Model access" section (M2).
 *
 * Owner: a project-scoped copy of the cluster catalog experience — provider
 * cards, catalog discovery, custom models, verify, a per-model enable Switch and
 * custom request headers — for the project's OWN providers/models, plus a
 * read-only list of every model the project can actually use (project-owned ∪
 * cluster-granted). Member: just that read-only list (the old ModelAccessPanel
 * behaviour). Writes are gated on `canManage`; the nav entry stays member-visible.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus } from '@phosphor-icons/react';
import { Button } from '../../components/Button';
import { useProjectModels } from '../../api/queries';
import type { ModelProvider } from '../../api/types';
import { GrantedModelsList, ModelsCatalog } from './ModelsCatalog';
import styles from './ModelsCatalog.module.css';

export function ProjectModelsPanel({ projectId, canManage }: { projectId: string; canManage: boolean }) {
  const { t } = useTranslation();
  const [editor, setEditor] = useState<ModelProvider | 'new' | null>(null);

  return (
    <div className={styles.panel} data-testid="project-models-panel">
      {canManage && (
        <section className={styles.panelSection}>
          <div className={styles.panelHead}>
            <div className={styles.panelHeadCopy}>
              <h3>{t('projectSettings.models.ownedTitle')}</h3>
              <p>{t('projectSettings.models.ownedDescription')}</p>
            </div>
            <Button variant="primary" onClick={() => setEditor('new')} data-testid="project-add-provider">
              <Plus size={14} aria-hidden="true" />{t('projectSettings.models.addProvider')}
            </Button>
          </div>
          <ModelsCatalog
            scope={{ kind: 'project', projectId }}
            editor={editor}
            onEditorChange={setEditor}
            searchable={false}
          />
        </section>
      )}

      <ClusterGrantedModels projectId={projectId} canManage={canManage} />
    </div>
  );
}

/**
 * The read-only union of models available to this project. For an owner it is a
 * secondary "everything usable, incl. cluster-granted" view under the manager;
 * for a member it is the whole section.
 */
function ClusterGrantedModels({ projectId, canManage }: { projectId: string; canManage: boolean }) {
  const { t } = useTranslation();
  const models = useProjectModels(projectId);

  if (models.isLoading) return <p className={styles.panelState}>{t('projectSettings.loadingModelAccess')}</p>;
  if (models.isError) return <p className={styles.panelState} role="alert">{t('projectSettings.modelAccessLoadError')}</p>;

  const data = models.data ?? { models: [], env_fallback: false };
  return (
    <section className={styles.panelSection} data-testid="project-available-models">
      <div className={styles.panelHead}>
        <div className={styles.panelHeadCopy}>
          <h3>{t('projectSettings.models.availableTitle')}</h3>
          <p>{canManage ? t('projectSettings.models.availableOwnerHint') : t('projectSettings.models.availableMemberHint')}</p>
        </div>
      </div>
      <GrantedModelsList models={data.models} envFallback={data.env_fallback} />
    </section>
  );
}
