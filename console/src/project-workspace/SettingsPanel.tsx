import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { Select } from '../components/Select';
import type { PRReadyPolicy, ProjectModel, Service } from '../api/types';
import { serviceProviderLabel, serviceSource } from './presentation';
import styles from './SettingsPanel.module.css';

export interface RepositorySettingsPreview {
  name: string;
  source: string;
  provider: string;
  defaultBranch: string;
  gitMode: 'draft_pr' | 'readonly';
}

export function SettingsPanel({
  service,
  preview,
  models,
  modelState,
  updating,
  onDefaultModelChange,
  onPRReadyPolicyChange,
  runnerProfiles,
  onRunnerProfileChange,
  onRetryModels,
}: {
  service: Service | undefined;
  preview?: RepositorySettingsPreview;
  models: readonly ProjectModel[];
  modelState: 'loading' | 'ready' | 'unverified';
  updating: boolean;
  onDefaultModelChange: (modelId: string) => void;
  onPRReadyPolicyChange: (policy: PRReadyPolicy) => void;
  runnerProfiles: readonly string[];
  onRunnerProfileChange: (profile: string) => void;
  onRetryModels: () => void;
}) {
  const { t } = useTranslation();
  if (!service && !preview) {
    return (
      <section className={styles.empty}>
        <h2>{t('settingsPanel.emptyTitle')}</h2>
        <p>{t('settingsPanel.emptyDescription')}</p>
      </section>
    );
  }

  const name = service?.name ?? preview!.name;
  const source = service ? serviceSource(service) : preview!.source;
  const provider = service ? serviceProviderLabel(service) : preview!.provider;
  const defaultBranch = service?.default_branch ?? preview!.defaultBranch;
  const gitMode = service?.git_mode ?? preview!.gitMode;

  return (
    <section className={styles.panel} aria-labelledby="service-settings-heading" data-testid={!service ? 'repository-default-settings' : undefined}>
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>{t('settingsPanel.eyebrow')}</span>
          <h2 id="service-settings-heading">{name}</h2>
          <p>{t('settingsPanel.description')}</p>
        </div>
      </div>

      <div className={styles.grid}>
        <dl className={styles.facts}>
          <div>
            <dt>{t('settingsPanel.source')}</dt>
            <dd><code>{source}</code></dd>
          </div>
          <div>
            <dt>{t('settingsPanel.provider')}</dt>
            <dd>{provider}</dd>
          </div>
          <div>
            <dt>{t('settingsPanel.defaultBranch')}</dt>
            <dd><code>{defaultBranch}</code></dd>
          </div>
          <div>
            <dt>{t('settingsPanel.gitMode')}</dt>
            <dd>{gitMode === 'draft_pr' ? t('settingsPanel.gitModePullRequest') : t('settingsPanel.gitModeReadOnly')}</dd>
          </div>
        </dl>

        <section className={styles.policy} aria-labelledby="service-model-policy-heading">
          <span className={styles.eyebrow}>{t('settingsPanel.modelPolicyEyebrow')}</span>
          <h3 id="service-model-policy-heading">{t('settingsPanel.modelPolicyTitle')}</h3>
          <p>{t('settingsPanel.modelPolicyDescription')}</p>
          {!service ? (
            <p className={styles.unavailable} data-testid="repository-default-model">No Repository override</p>
          ) : modelState === 'loading' ? (
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

        {gitMode === 'draft_pr' && (
          <section className={styles.policy} aria-labelledby="service-pr-policy-heading">
            <span className={styles.eyebrow}>{t('settingsPanel.deliveryPolicyEyebrow')}</span>
            <h3 id="service-pr-policy-heading">{t('settingsPanel.deliveryPolicyTitle')}</h3>
            <p>{t('settingsPanel.deliveryPolicyDescription')}</p>
            <Select
              aria-label={t('settingsPanel.deliveryPolicyAria')}
              value={service ? service.pr_ready_policy ?? 'always_draft' : 'lifecycle_aware'}
              data-testid="service-pr-ready-policy-select"
              disabled={updating || !service}
              onChange={(value) => onPRReadyPolicyChange(value as PRReadyPolicy)}
              options={[
                { value: 'lifecycle_aware', label: t('settingsPanel.lifecycleAware') },
                { value: 'always_draft', label: t('settingsPanel.alwaysDraft') },
              ]}
            />
          </section>
        )}

        <section className={styles.policy} aria-labelledby="service-runtime-policy-heading">
          <span className={styles.eyebrow}>{t('settingsPanel.runtimePolicyEyebrow')}</span>
          <h3 id="service-runtime-policy-heading">{t('settingsPanel.runtimePolicyTitle')}</h3>
          <p>{t('settingsPanel.runtimePolicyDescription')}</p>
          {!service ? (
            <Select
              aria-label={t('settingsPanel.runnerProfileAria')}
              value="default"
              data-testid="service-runner-profile-select"
              disabled
              onChange={onRunnerProfileChange}
              options={[{ value: 'default', label: 'default' }]}
            />
          ) : runnerProfiles.length > 0 ? (
            <Select
              aria-label={t('settingsPanel.runnerProfileAria')}
              value={service.runner_profile || 'default'}
              data-testid="service-runner-profile-select"
              disabled={updating}
              onChange={onRunnerProfileChange}
              options={runnerProfiles.map((profile) => ({ value: profile, label: profile }))}
            />
          ) : (
            <p className={styles.unavailable}>{t('settingsPanel.runtimeProfilesUnavailable')}</p>
          )}
        </section>
      </div>
    </section>
  );
}
