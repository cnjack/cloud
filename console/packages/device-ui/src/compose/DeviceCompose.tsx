/*
 * DeviceCompose — the M12 compose panel shared by the console and the mobile
 * app. It renders the five compose elements a connector advertises through
 * Device.capabilities:
 *
 *   project directory  (select over capabilities.projects)
 *   model              (select over capabilities.models, grouped by provider)
 *   effort             (segmented buttons over capabilities.efforts)
 *   goal               (collapsible text input)
 *   attachments        (non-image files, ≤2 MB each, ≤5 total, read as base64)
 *
 * Degradation rule: a device whose connector never reported capabilities
 * (capabilities == null) hides the WHOLE panel; a partially populated
 * capabilities object hides just the empty sections.
 *
 * The component is CONTROLLED — the shell owns a ComposeValue and assembles
 * the send payload with composeExtras() at submit time. The extras travel
 * under the E2EE layer (withDeviceCrypto); this component never touches
 * ciphertext.
 */
import { Paperclip, X } from '@phosphor-icons/react';
import { useRef, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { ComposeAttachment, DeviceCapabilities, SendMessageExtras } from '../api/devices';
import styles from './DeviceCompose.module.css';

/** Single-attachment size cap (contract: block >2 MB on the client). */
export const COMPOSE_MAX_ATTACHMENT_BYTES = 2 * 1024 * 1024;
/** Total attachment count cap (contract: block >5 on the client). */
export const COMPOSE_MAX_ATTACHMENTS = 5;

/** The controlled state of the compose panel. ''/empty means "device default". */
export interface ComposeValue {
  projectPath: string;
  /** Selected model, null = device default. */
  model: { provider: string; id: string } | null;
  effort: string;
  goal: string;
  attachments: ComposeAttachment[];
}

export function initialComposeValue(): ComposeValue {
  return { projectPath: '', model: null, effort: '', goal: '', attachments: [] };
}

/**
 * Assemble the chat.send extension fields for the current panel state.
 * Returns undefined when nothing is set, so the payload stays byte-identical
 * to a pre-M12 client. Selections that are no longer advertised by the
 * device (stale capabilities) are dropped rather than sent.
 */
export function composeExtras(value: ComposeValue, capabilities?: DeviceCapabilities | null): SendMessageExtras | undefined {
  const extras: SendMessageExtras = {};
  if (value.projectPath && capabilities?.projects?.some((p) => p.path === value.projectPath)) {
    extras.project_path = value.projectPath;
  }
  if (value.model && capabilities?.models?.some((m) => m.provider === value.model?.provider && m.id === value.model?.id)) {
    extras.model = { provider: value.model.provider, id: value.model.id };
  }
  if (value.effort && capabilities?.efforts?.includes(value.effort)) {
    extras.effort = value.effort;
  }
  const goal = value.goal.trim();
  if (goal) extras.goal = goal;
  if (value.attachments.length > 0) extras.attachments = value.attachments;
  return Object.keys(extras).length > 0 ? extras : undefined;
}

/** Select value encoding for a model option (provider/id pair). */
function modelOptionValue(m: { provider: string; id: string }): string {
  return `${m.provider}::${m.id}`;
}

/** Read a File into base64 (no data: prefix). */
function readFileB64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => {
      const result = String(reader.result ?? '');
      resolve(result.slice(result.indexOf(',') + 1));
    };
    reader.readAsDataURL(file);
  });
}

export interface DeviceComposeProps {
  /** Device.capabilities; null/undefined hides the whole panel. */
  capabilities?: DeviceCapabilities | null;
  disabled?: boolean;
  value: ComposeValue;
  onChange: (value: ComposeValue) => void;
}

export function DeviceCompose({ capabilities, disabled, value, onChange }: DeviceComposeProps) {
  const { t } = useTranslation();
  const fileInput = useRef<HTMLInputElement>(null);
  const [goalOpen, setGoalOpen] = useState(false);
  const [attachError, setAttachError] = useState<string | null>(null);

  if (!capabilities) return null;
  const projects = capabilities.projects ?? [];
  const models = capabilities.models ?? [];
  const efforts = capabilities.efforts ?? [];

  const patch = (part: Partial<ComposeValue>) => onChange({ ...value, ...part });

  const providers = [...new Set(models.map((m) => m.provider))];

  const pickFiles = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = [...(event.target.files ?? [])];
    event.target.value = '';
    if (files.length === 0) return;
    setAttachError(null);
    for (const file of files) {
      if (file.type.startsWith('image/')) {
        setAttachError(t('device.compose.attachmentImage', { name: file.name }));
        return;
      }
      if (file.size > COMPOSE_MAX_ATTACHMENT_BYTES) {
        setAttachError(t('device.compose.attachmentTooBig', { name: file.name }));
        return;
      }
    }
    const room = COMPOSE_MAX_ATTACHMENTS - value.attachments.length;
    if (files.length > room) {
      setAttachError(t('device.compose.attachmentTooMany', { max: COMPOSE_MAX_ATTACHMENTS }));
      return;
    }
    const added: ComposeAttachment[] = [];
    for (const file of files) {
      added.push({ name: file.name, mime: file.type || 'application/octet-stream', data_b64: await readFileB64(file) });
    }
    patch({ attachments: [...value.attachments, ...added] });
  };

  return (
    <div className={styles.compose} data-testid="device-compose">
      {projects.length > 0 && (
        <label className={styles.field}>
          <span className={styles.label}>{t('device.compose.project')}</span>
          <select
            className={styles.select}
            aria-label={t('device.compose.project')}
            value={value.projectPath}
            disabled={disabled}
            onChange={(e) => patch({ projectPath: e.target.value })}
          >
            <option value="">{t('device.compose.deviceDefault')}</option>
            {projects.map((p) => (
              <option key={p.path} value={p.path}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
      )}

      {models.length > 0 && (
        <label className={styles.field}>
          <span className={styles.label}>{t('device.compose.model')}</span>
          <select
            className={styles.select}
            aria-label={t('device.compose.model')}
            value={value.model ? modelOptionValue(value.model) : ''}
            disabled={disabled}
            onChange={(e) => {
              const m = models.find((x) => modelOptionValue(x) === e.target.value);
              patch({ model: m ? { provider: m.provider, id: m.id } : null });
            }}
          >
            <option value="">{t('device.compose.deviceDefault')}</option>
            {providers.map((provider) => (
              <optgroup key={provider} label={provider}>
                {models
                  .filter((m) => m.provider === provider)
                  .map((m) => (
                    <option key={modelOptionValue(m)} value={modelOptionValue(m)}>
                      {m.label}
                    </option>
                  ))}
              </optgroup>
            ))}
          </select>
        </label>
      )}

      {efforts.length > 0 && (
        <div className={styles.field}>
          <span className={styles.label} id="device-compose-effort-label">
            {t('device.compose.effort')}
          </span>
          <div className={styles.segmented} role="group" aria-labelledby="device-compose-effort-label">
            <button
              type="button"
              data-active={value.effort === ''}
              disabled={disabled}
              onClick={() => patch({ effort: '' })}
            >
              {t('device.compose.deviceDefault')}
            </button>
            {efforts.map((effort) => (
              <button
                key={effort}
                type="button"
                data-active={value.effort === effort}
                disabled={disabled}
                onClick={() => patch({ effort })}
              >
                {effort}
              </button>
            ))}
          </div>
        </div>
      )}

      <div className={styles.field}>
        <button
          type="button"
          className={styles.goalToggle}
          aria-expanded={goalOpen || !!value.goal}
          disabled={disabled}
          onClick={() => setGoalOpen((open) => !open)}
        >
          {t('device.compose.goal')}
        </button>
        {(goalOpen || value.goal) && (
          <input
            className={styles.goalInput}
            type="text"
            aria-label={t('device.compose.goal')}
            placeholder={t('device.compose.goalPlaceholder')}
            value={value.goal}
            disabled={disabled}
            onChange={(e) => patch({ goal: e.target.value })}
          />
        )}
      </div>

      <div className={styles.field}>
        <span className={styles.label}>{t('device.compose.attachments')}</span>
        <div className={styles.attachments}>
          {value.attachments.map((a, i) => (
            <span key={`${a.name}-${i}`} className={styles.attachment}>
              <span className={styles.attachmentName}>{a.name}</span>
              <button
                type="button"
                aria-label={t('device.compose.removeAttachment', { name: a.name })}
                disabled={disabled}
                onClick={() => patch({ attachments: value.attachments.filter((_, j) => j !== i) })}
              >
                <X size={12} aria-hidden="true" />
              </button>
            </span>
          ))}
          <button
            type="button"
            className={styles.attachButton}
            disabled={disabled || value.attachments.length >= COMPOSE_MAX_ATTACHMENTS}
            onClick={() => fileInput.current?.click()}
          >
            <Paperclip size={13} aria-hidden="true" />
            {t('device.compose.attachAdd')}
          </button>
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            data-testid="device-compose-files"
            onChange={(e) => void pickFiles(e)}
          />
        </div>
        {attachError && (
          <p className={styles.attachError} role="alert">
            {attachError}
          </p>
        )}
      </div>
    </div>
  );
}
