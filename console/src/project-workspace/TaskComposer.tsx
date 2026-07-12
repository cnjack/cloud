import type { FormEvent, ReactNode } from 'react';
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
  return (
    <section className={styles.section} aria-labelledby="task-composer-heading">
      <div className={styles.heading}>
        <div>
          <span className={styles.eyebrow}>New task</span>
          <h2 id="task-composer-heading">Start a session in {service.name}</h2>
          <p>{serviceSource(service)} · {service.default_branch}</p>
        </div>
        <span className={styles.isolation}>Runs in an isolated workspace</span>
      </div>

      {notice && <div className={styles.notice}>{notice}</div>}

      <form className={styles.composer} onSubmit={onSubmit} noValidate>
        <textarea
          className={styles.input}
          aria-label="Message"
          aria-invalid={!!promptError}
          required
          placeholder="Describe the work to complete…"
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
            aria-label="Permission mode"
            title="Full access auto-approves the agent; Ask before actions pauses it for your approval in the timeline."
            value={askApproval ? 'approval' : ''}
            onChange={(value) => onAskApprovalChange(value === 'approval')}
            disabled={!configured}
            data-testid="composer-approval-toggle"
            options={[
              { value: '', label: 'Full access' },
              { value: 'approval', label: 'Ask before actions' },
            ]}
          />
          <span className={styles.controlHint}>Session</span>
          <div className={styles.controlsEnd}>
            {models.length > 0 && (
              <Select
                className={styles.pill}
                aria-label="Model"
                value={selectedModel}
                onChange={onSelectedModelChange}
                disabled={!configured}
                data-testid="composer-model-select"
                options={[
                  { value: '', label: 'Service default' },
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
              Send
            </Button>
          </div>
        </div>
      </form>
    </section>
  );
}
