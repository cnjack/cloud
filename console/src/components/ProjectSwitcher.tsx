/*
 * ProjectSwitcher — compact dropdown in the topbar to jump between projects
 * (PRD app-shell: "project switcher"). Reads the projects list from cache.
 */
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useProjects } from '../api/queries';
import { projectRepoSummary } from '../lib/repo';
import styles from './ProjectSwitcher.module.css';

export function ProjectSwitcher({
  activeProjectId,
  onSelect,
  onAll,
}: {
  activeProjectId?: string;
  onSelect: (id: string) => void;
  onAll: () => void;
}) {
  const { t } = useTranslation();
  const { data: projects } = useProjects();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const active = projects?.find((p) => p.id === activeProjectId);
  const label = active ? active.name : t('components.projectSwitcher.allProjects');

  return (
    <div className={styles.wrap} ref={ref}>
      <button
        className={styles.trigger}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        type="button"
      >
        <span className={styles.label}>{label}</span>
        <span className={styles.caret} aria-hidden>
          ▾
        </span>
      </button>
      {open && (
        <div className={styles.menu} role="listbox">
          <button
            className={styles.item}
            data-active={!activeProjectId || undefined}
            onClick={() => {
              onAll();
              setOpen(false);
            }}
            type="button"
          >
            {t('components.projectSwitcher.allProjects')}
          </button>
          {(projects ?? []).map((p) => (
            <button
              key={p.id}
              className={styles.item}
              data-active={p.id === activeProjectId || undefined}
              onClick={() => {
                onSelect(p.id);
                setOpen(false);
              }}
              type="button"
            >
              <span className={styles.itemName}>{p.name}</span>
              <span className={styles.itemRepo}>{projectRepoSummary(p.services)}</span>
            </button>
          ))}
          {projects && projects.length === 0 && (
            <div className={styles.emptyItem}>{t('components.projectSwitcher.noProjects')}</div>
          )}
        </div>
      )}
    </div>
  );
}
