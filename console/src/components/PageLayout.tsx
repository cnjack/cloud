import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import styles from './PageLayout.module.css';

export function SurfaceInner({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`${styles.inner} ${className}`}>{children}</div>;
}

export function PageHeader({
  eyebrow,
  title,
  description,
  meta,
  actions,
}: {
  eyebrow: string;
  title: string;
  description: string;
  meta?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className={styles.pageHeader}>
      <div className={styles.heading}>
        <span className={styles.eyebrow}>{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
        {meta && <div className={styles.meta}>{meta}</div>}
      </div>
      {actions && <div className={styles.actions}>{actions}</div>}
    </header>
  );
}

export function ClusterSubnav() {
  const { t } = useTranslation();
  return (
    <nav className={styles.subnav} aria-label={t('shell.clusterSections')}>
      <NavLink to="/cluster" end>{t('shell.crumbOverview')}</NavLink>
      <NavLink to="/cluster/models">{t('shell.crumbModels')}</NavLink>
      <NavLink to="/cluster/connections">{t('shell.crumbConnections')}</NavLink>
    </nav>
  );
}

export function StatusLabel({
  children,
  tone = 'neutral',
}: {
  children: ReactNode;
  tone?: 'neutral' | 'success' | 'warning' | 'danger';
}) {
  return <span className={styles.status} data-tone={tone}>{children}</span>;
}

export function ActionLink({
  to,
  children,
  variant = 'secondary',
}: {
  to: string;
  children: ReactNode;
  variant?: 'primary' | 'secondary' | 'ghost';
}) {
  return <Link to={to} className={`${styles.actionLink} ${styles[variant]}`}>{children}</Link>;
}

export function SectionPanel({
  title,
  aside,
  children,
  className = '',
}: {
  title: string;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`${styles.panel} ${className}`}>
      <header className={styles.panelHead}><h2>{title}</h2>{aside}</header>
      {children}
    </section>
  );
}

export function DefinitionList({ items }: { items: Array<{ label: string; value: ReactNode }> }) {
  return (
    <dl className={styles.definitions}>
      {items.map((item) => <div className={styles.definition} key={item.label}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}
    </dl>
  );
}

export { styles as pageLayoutStyles };
