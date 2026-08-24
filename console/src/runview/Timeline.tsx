import { Thread } from 'jcode-ui';
import { useTranslation } from 'react-i18next';
import { timelineCss as styles } from '@jcloud/device-ui';

/**
 * Cloud's Run adapter ends at RuntimeState. Rendering belongs entirely to the
 * shared jcode-ui Thread so Desktop and Cloud cannot drift on Markdown, tools,
 * approvals, pending state, or follow-scroll behavior.
 */
export function Timeline() {
  const { t } = useTranslation();
  return (
    <div className={styles.wrap} data-testid="event-timeline">
      <Thread
        virtualize={false}
        className={styles.thread}
        pendingLabel={t('run.thinkingAria')}
        overscanBottom={24}
      />
    </div>
  );
}
