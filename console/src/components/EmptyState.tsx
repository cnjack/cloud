import type { ReactNode } from 'react';
import styles from './EmptyState.module.css';

interface EmptyStateProps {
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  icon?: ReactNode;
  'data-testid'?: string;
}

export function EmptyState({
  title,
  description,
  action,
  icon,
  'data-testid': testId,
}: EmptyStateProps) {
  return (
    <div className={styles.wrap} data-testid={testId}>
      {icon && <div className={styles.icon}>{icon}</div>}
      <h2 className={styles.title}>{title}</h2>
      {description && <p className={styles.desc}>{description}</p>}
      {action && <div className={styles.action}>{action}</div>}
    </div>
  );
}
