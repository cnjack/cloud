import { Link } from 'react-router-dom';
import styles from './ProjectSettingsAction.module.css';

export function ProjectSettingsAction({
  to,
  active = false,
  onClick,
}: {
  to?: string;
  active?: boolean;
  onClick?: () => void;
}) {
  const content = <SettingsIcon />;
  const shared = {
    className: styles.trigger,
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

function SettingsIcon() {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-1.86 1.86-.06-.06A1.7 1.7 0 0 0 16 18.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V20h-2.6v-.1a1.7 1.7 0 0 0-1.1-1.6 1.7 1.7 0 0 0-1.88.34l-.06.06-1.86-1.86.06-.06A1.7 1.7 0 0 0 7.6 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H5.8V11h.1A1.7 1.7 0 0 0 7.5 9.9a1.7 1.7 0 0 0-.34-1.88l-.06-.06L8.96 6.1l.06.06A1.7 1.7 0 0 0 10.9 6.5a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1V4.7h2.6v.1A1.7 1.7 0 0 0 16 6.4a1.7 1.7 0 0 0 1.88-.34l.06-.06 1.86 1.86-.06.06A1.7 1.7 0 0 0 19.4 9.8c.12.38.33.72.6 1 .3.28.68.42 1.1.42h.1v2.6h-.1A1.7 1.7 0 0 0 19.4 15Z" />
    </svg>
  );
}
