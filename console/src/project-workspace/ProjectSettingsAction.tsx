import { Link } from 'react-router-dom';
import { Gear } from '@phosphor-icons/react';
import styles from './ProjectSettingsAction.module.css';

export function ProjectSettingsAction({
  to,
  active = false,
  onClick,
  label,
}: {
  to?: string;
  active?: boolean;
  onClick?: () => void;
  label?: string;
}) {
  const content = <><Gear size={16} weight="regular" aria-hidden="true" />{label && <span>{label}</span>}</>;
  const shared = {
    className: [styles.trigger, label && styles.labeled].filter(Boolean).join(' '),
    'aria-label': 'Project settings',
    title: 'Project settings',
    'data-testid': 'project-settings-trigger',
    'data-active': active || undefined,
  } as const;

  return to ? (
    <Link {...shared} to={to}>{content}</Link>
  ) : (
    <button {...shared} type="button" onClick={onClick}>{content}</button>
  );
}
