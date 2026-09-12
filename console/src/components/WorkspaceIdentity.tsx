import { GitBranch, TerminalWindow } from '@phosphor-icons/react';
import styles from './WorkspaceIdentity.module.css';

/** One visual identity for the workspace in navigation and the persistent header. */
export function WorkspaceIdentity({ name, kind = 'repository' }: {
  name: string;
  kind?: 'repository' | 'remote';
}) {
  const Icon = kind === 'remote' ? TerminalWindow : GitBranch;
  return <span className={styles.identity} title={name}>
    <Icon size={16} weight="regular" aria-hidden="true" />
    <span>{name}</span>
  </span>;
}
