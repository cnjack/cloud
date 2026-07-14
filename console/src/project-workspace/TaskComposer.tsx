import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { PaperPlaneTilt } from '@phosphor-icons/react';
import { Button } from '../components/Button';
import { Select } from '../components/Select';
import type { ProjectModel, Service } from '../api/types';
import { serviceSource } from './presentation';
import styles from './TaskComposer.module.css';

export function TaskComposer({
  service,
  notice,
  configured,
  prompt,
  promptError,
  onPromptChange,
  models,
  selectedModel,
  onSelectedModelChange,
  askApproval,
  onAskApprovalChange,
  onSubmit,
  busy,
}: {
  service: Service;
  notice: ReactNode;
  configured: boolean;
  prompt: string;
  promptError?: string;
  onPromptChange: (prompt: string) => void;
  models: readonly ProjectModel[];
  selectedModel: string;
  onSelectedModelChange: (id: string) => void;
  askApproval: boolean;
  onAskApprovalChange: (enabled: boolean) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  busy: boolean;
}) {
  const { t } = useTranslation();
  return (
    <section className={styles.section} aria-labelledby="task-composer-heading">
      <div className={styles.heading}>
        <div>
          <span className={styles.eyebrow}>{t('taskComposer.eyebrow')}</span>
          <h2 id="task-composer-heading">{t('taskComposer.heading', { name: service.name })}</h2>
          <p>{serviceSource(service)} · {service.default_branch}</p>
        </div>
        <span className={styles.isolation}>{t('taskComposer.isolation')}</span>
      </div>

      {notice && <div className={styles.notice}>{notice}</div>}

      <form className={styles.composer} onSubmit={onSubmit} noValidate>
        <textarea
          className={styles.input}
          aria-label={t('taskComposer.messageAria')}
          aria-invalid={!!promptError}
          required
          placeholder={t('taskComposer.placeholder')}
          value={prompt}
          onChange={(event) => onPromptChange(event.target.value)}
          data-testid="run-input"
          rows={4}
          disabled={!configured}
        />
        {promptError && <p className={styles.error}>{promptError}</p>}
        <div className={styles.controls}>
          <Select
            className={styles.pill}
            aria-label={t('taskComposer.permissionModeAria')}
            title={t('taskComposer.permissionModeTitle')}
            value={askApproval ? 'approval' : ''}
            onChange={(value) => onAskApprovalChange(value === 'approval')}
            disabled={!configured}
            data-testid="composer-approval-toggle"
            options={[
              { value: '', label: t('taskComposer.fullAccess') },
              { value: 'approval', label: t('taskComposer.askBeforeActions') },
            ]}
          />
          <span className={styles.controlHint}>{t('taskComposer.session')}</span>
          <div className={styles.controlsEnd}>
            {models.length > 0 && (
              <Select
                className={styles.pill}
                aria-label={t('taskComposer.modelAria')}
                value={selectedModel}
                onChange={onSelectedModelChange}
                disabled={!configured}
                data-testid="composer-model-select"
                options={[
                  { value: '', label: t('taskComposer.serviceDefault') },
                  ...models.map((model) => ({ value: model.id, label: model.name })),
                ]}
              />
            )}
            <Button
              type="submit"
              variant="primary"
              size="sm"
              className={styles.send}
              loading={busy}
              disabled={!configured}
              data-testid="run-submit"
            >
              <PaperPlaneTilt size={16} weight="regular" aria-hidden="true" />
              <span>{t('taskComposer.send')}</span>
            </Button>
          </div>
        </div>
      </form>
    </section>
  );
}
