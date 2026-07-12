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
  if (!service) {
    return (
      <section className={styles.empty}>
        <h2>Settings need a service</h2>
        <p>Connect a repository before configuring its model policy.</p>
      </section>
    );
  }

  return (
    <section className={styles.panel} aria-labelledby="service-settings-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>Service settings</span>
          <h2 id="service-settings-heading">{service.name}</h2>
          <p>Execution policy for this service. Project membership and integrations are managed separately.</p>
        </div>
      </div>

      <div className={styles.grid}>
        <dl className={styles.facts}>
          <div>
            <dt>Source</dt>
            <dd><code>{serviceSource(service)}</code></dd>
          </div>
          <div>
            <dt>Provider</dt>
            <dd>{serviceProviderLabel(service)}</dd>
          </div>
          <div>
            <dt>Default branch</dt>
            <dd><code>{service.default_branch}</code></dd>
          </div>
          <div>
            <dt>Git mode</dt>
            <dd>{service.git_mode === 'draft_pr' ? 'Draft pull request' : 'Read only'}</dd>
          </div>
        </dl>

        <section className={styles.policy} aria-labelledby="service-model-policy-heading">
          <span className={styles.eyebrow}>Model policy</span>
          <h3 id="service-model-policy-heading">Service default model</h3>
          <p>This is used only when a task composer keeps the per-run choice at Service default.</p>
          {modelState === 'loading' ? (
            <p className={styles.unavailable} data-testid="service-model-policy-loading">
              Loading the project model grants…
            </p>
          ) : modelState === 'unverified' ? (
            <div className={styles.unverified} data-testid="service-model-policy-unverified">
              <p>Couldn’t verify this project’s model grants. Do not change the default until the model catalog is available.</p>
              <Button type="button" variant="secondary" size="sm" onClick={onRetryModels}>
                Retry model check
              </Button>
            </div>
          ) : models.length > 0 ? (
            <Select
              aria-label="Default model for this repository"
              value={service.default_model_id ?? ''}
              data-testid="service-default-model-select"
              disabled={updating}
              onChange={onDefaultModelChange}
              options={[
                { value: '', label: 'No default' },
                ...models.map((model) => ({ value: model.id, label: model.name })),
              ]}
            />
          ) : (
            <p className={styles.unavailable} data-testid="service-model-policy-unavailable">
              No model has been granted to this project. A cluster administrator must grant one before a service default can be selected.
            </p>
          )}
        </section>
      </div>
    </section>
  );
}
