import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { Select } from '../components/Select';
import type { ProjectModel, Service } from '../api/types';
import { serviceProviderLabel, serviceSource } from './presentation';
import styles from './SettingsPanel.module.css';

export function SettingsPanel({
  service,
  models,
  modelState,
  updating,
  onDefaultModelChange,
  onRetryModels,
}: {
  service: Service | undefined;
  models: readonly ProjectModel[];
  modelState: 'loading' | 'ready' | 'unverified';
  updating: boolean;
  onDefaultModelChange: (modelId: string) => void;
  onRetryModels: () => void;
}) {
  const { t } = useTranslation();
  if (!service) {
    return (
      <section className={styles.empty}>
        <h2>{t('settingsPanel.emptyTitle')}</h2>
        <p>{t('settingsPanel.emptyDescription')}</p>
      </section>
    );
  }

  return (
    <section className={styles.panel} aria-labelledby="service-settings-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>{t('settingsPanel.eyebrow')}</span>
          <h2 id="service-settings-heading">{service.name}</h2>
          <p>{t('settingsPanel.description')}</p>
        </div>
      </div>

      <div className={styles.grid}>
        <dl className={styles.facts}>
          <div>
            <dt>{t('settingsPanel.source')}</dt>
            <dd><code>{serviceSource(service)}</code></dd>
          </div>
          <div>
            <dt>{t('settingsPanel.provider')}</dt>
            <dd>{serviceProviderLabel(service)}</dd>
          </div>
          <div>
            <dt>{t('settingsPanel.defaultBranch')}</dt>
            <dd><code>{service.default_branch}</code></dd>
          </div>
          <div>
            <dt>{t('settingsPanel.gitMode')}</dt>
            <dd>{service.git_mode === 'draft_pr' ? t('settingsPanel.gitModeDraftPr') : t('settingsPanel.gitModeReadOnly')}</dd>
          </div>
        </dl>

        <section className={styles.policy} aria-labelledby="service-model-policy-heading">
          <span className={styles.eyebrow}>{t('settingsPanel.modelPolicyEyebrow')}</span>
          <h3 id="service-model-policy-heading">{t('settingsPanel.modelPolicyTitle')}</h3>
          <p>{t('settingsPanel.modelPolicyDescription')}</p>
          {modelState === 'loading' ? (
            <p className={styles.unavailable} data-testid="service-model-policy-loading">
              {t('settingsPanel.modelsLoading')}
            </p>
          ) : modelState === 'unverified' ? (
            <div className={styles.unverified} data-testid="service-model-policy-unverified">
              <p>{t('settingsPanel.modelsUnverified')}</p>
              <Button type="button" variant="secondary" size="sm" onClick={onRetryModels}>
                {t('settingsPanel.retryModelCheck')}
              </Button>
            </div>
          ) : models.length > 0 ? (
            <Select
              aria-label={t('settingsPanel.defaultModelAria')}
              value={service.default_model_id ?? ''}
              data-testid="service-default-model-select"
              disabled={updating}
              onChange={onDefaultModelChange}
              options={[
                { value: '', label: t('settingsPanel.noDefault') },
                ...models.map((model) => ({ value: model.id, label: model.name })),
              ]}
            />
          ) : (
            <p className={styles.unavailable} data-testid="service-model-policy-unavailable">
              {t('settingsPanel.noModelGranted')}
            </p>
          )}
        </section>
      </div>
    </section>
  );
}
