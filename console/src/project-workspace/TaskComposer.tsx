import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Paperclip, PaperPlaneTilt, X } from '@phosphor-icons/react';
import { Button } from '../components/Button';
import { Select } from '../components/Select';
import type { ProjectModel, Service, ServiceBranch } from '../api/types';
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
  effortEnabled,
  modelEffort,
  onModelEffortChange,
  goalMode,
  onGoalModeChange,
  attachments,
  attachmentError,
  onAttachmentsAdd,
  onAttachmentRemove,
  branches,
  branchesLoading,
  branchesError,
  selectedBranch,
  onSelectedBranchChange,
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
  effortEnabled: boolean;
  modelEffort: 'auto' | 'low' | 'medium' | 'high';
  onModelEffortChange: (effort: 'auto' | 'low' | 'medium' | 'high') => void;
  goalMode: boolean;
  onGoalModeChange: (enabled: boolean) => void;
  attachments: readonly File[];
  attachmentError?: string;
  onAttachmentsAdd: (files: File[]) => void;
  onAttachmentRemove: (index: number) => void;
  branches: readonly ServiceBranch[];
  branchesLoading: boolean;
  branchesError: boolean;
  selectedBranch: string;
  onSelectedBranchChange: (name: string) => void;
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
        {attachments.length > 0 && (
          <div className={styles.attachments} aria-label={t('taskComposer.attachments')}>
            {attachments.map((file, index) => (
              <span className={styles.attachment} key={`${file.name}-${file.size}-${file.lastModified}-${index}`}>
                <span title={file.name}>{file.name}</span>
                <button
                  type="button"
                  title={t('taskComposer.removeAttachment', { name: file.name })}
                  aria-label={t('taskComposer.removeAttachment', { name: file.name })}
                  onClick={() => onAttachmentRemove(index)}
                  disabled={busy}
                >
                  <X size={13} aria-hidden="true" />
                </button>
              </span>
            ))}
          </div>
        )}
        {attachmentError && <p className={styles.error} role="alert">{attachmentError}</p>}
        <div className={styles.controls}>
          <label
            className={styles.attachButton}
            title={t('taskComposer.addAttachments')}
            aria-label={t('taskComposer.addAttachments')}
          >
            <Paperclip size={16} aria-hidden="true" />
            <input
              type="file"
              multiple
              aria-label={t('taskComposer.addAttachments')}
              data-testid="composer-attachment-input"
              disabled={!configured || busy || attachments.length >= 10}
              onChange={(event) => {
                onAttachmentsAdd(Array.from(event.currentTarget.files ?? []));
                event.currentTarget.value = '';
              }}
            />
          </label>
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
          <label className={styles.goalToggle}>
            <input
              type="checkbox"
              checked={goalMode}
              onChange={(event) => onGoalModeChange(event.target.checked)}
              disabled={!configured || busy}
            />
            <span>{t('taskComposer.goalMode')}</span>
          </label>
          <div className={styles.controlsEnd}>
			<Select
				className={styles.pill}
				aria-label={t('taskComposer.branchAria')}
				title={t('taskComposer.branchAria')}
				value={selectedBranch}
				onChange={onSelectedBranchChange}
				disabled={!configured || branchesLoading || branches.length === 0}
				data-testid="composer-branch-select"
				placeholder={branchesLoading ? t('taskComposer.branchLoading') : t('taskComposer.branchUnavailable')}
				options={branches.map((branch) => ({
					value: branch.name,
					label: branch.default ? `${branch.name} · ${t('taskComposer.branchDefault')}` : branch.name,
				}))}
			/>
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
            {effortEnabled && (
              <Select
                className={styles.pill}
                aria-label={t('taskComposer.effortAria')}
                value={modelEffort}
                onChange={(value) => onModelEffortChange(value as 'auto' | 'low' | 'medium' | 'high')}
                disabled={!configured || busy}
                data-testid="composer-effort-select"
                options={[
                  { value: 'auto', label: t('taskComposer.effortAuto') },
                  { value: 'low', label: t('taskComposer.effortLow') },
                  { value: 'medium', label: t('taskComposer.effortMedium') },
                  { value: 'high', label: t('taskComposer.effortHigh') },
                ]}
              />
            )}
            <Button
              type="submit"
              variant="primary"
              size="sm"
              className={styles.send}
              loading={busy}
              disabled={!configured || busy || branchesLoading || branchesError || branches.length === 0}
              data-testid="run-submit"
            >
              <PaperPlaneTilt size={16} weight="regular" aria-hidden="true" />
              <span>{t('taskComposer.send')}</span>
            </Button>
          </div>
        </div>
		{branchesError && <p className={styles.error} role="status">{t('taskComposer.branchUnavailable')}</p>}
      </form>
    </section>
  );
}
