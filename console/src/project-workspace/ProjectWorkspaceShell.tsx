import { useRef, type KeyboardEvent, type ReactNode } from 'react';
import type { Service } from '../api/types';
import { WORKSPACE_TABS, type WorkspaceTab } from './location';
import { serviceMark, serviceProviderLabel } from './presentation';
import styles from './ProjectWorkspaceShell.module.css';

export function ProjectWorkspaceShell({
  mode = 'workspace',
  projectName,
  services,
  activeServiceId,
  activeTab,
  canManage,
  onSelectService,
  onSelectTab,
  railTop,
  railFooter,
  railAction,
  projectAction,
  utility,
  mobileActions,
  header,
  children,
}: {
  mode?: 'workspace' | 'detail' | 'settings';
  projectName: string;
  services: readonly Service[];
  activeServiceId: string;
  activeTab: WorkspaceTab;
  canManage: boolean;
  onSelectService: (serviceId: string) => void;
  onSelectTab: (tab: WorkspaceTab) => void;
  railTop: ReactNode;
  railFooter?: ReactNode;
  railAction?: ReactNode;
  projectAction?: ReactNode;
  utility?: ReactNode;
  mobileActions?: ReactNode;
  header?: ReactNode;
  children: ReactNode;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const tabs: readonly WorkspaceTab[] = canManage
    ? WORKSPACE_TABS
    : WORKSPACE_TABS.filter((tab) => tab !== 'settings');

  const selectTab = (next: WorkspaceTab) => {
    if (next !== activeTab && scrollRef.current) {
      scrollRef.current.scrollTop = 0;
    }
    onSelectTab(next);
    window.requestAnimationFrame(() => document.getElementById(`workspace-tab-${next}`)?.focus());
  };

  const onTabsKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = tabs.indexOf(activeTab);
    const nextIndex =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? tabs.length - 1
          : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    const next = tabs[nextIndex];
    if (next) selectTab(next);
  };

  return (
    <div className={styles.shell} data-testid="project-workspace-shell">
      <aside className={styles.rail} aria-label="Project services">
        <div className={styles.railTop}>{railTop}</div>
        <div className={styles.projectSummary} data-testid="project-summary">
          <div className={styles.projectSummaryCopy}>
            <span className={styles.eyebrow}>Project</span>
            <strong title={projectName}>{projectName}</strong>
            <small>{services.length === 1 ? '1 service' : `${services.length} services`}</small>
          </div>
          {projectAction && (
            <div className={styles.projectAction} data-testid="project-administration">
              {projectAction}
            </div>
          )}
        </div>

        <div className={styles.serviceArea}>
          <div className={styles.sectionHead}>
            <span>Services</span>
            <span>{services.length}</span>
          </div>
          {services.length > 0 ? (
            <nav className={styles.serviceList} aria-label="Services">
              {services.map((service) => {
                const selected = service.id === activeServiceId;
                return (
                  <button
                    key={service.id}
                    type="button"
                    className={styles.service}
                    data-active={selected || undefined}
                    aria-current={selected ? 'page' : undefined}
                    aria-pressed={selected}
                    data-testid={`service-rail-${service.id}`}
                    onClick={() => onSelectService(service.id)}
                  >
                    <span className={styles.serviceMark} aria-hidden>
                      {serviceMark(service)}
                    </span>
                    <span className={styles.serviceCopy}>
                      <strong>{service.name}</strong>
                      <small>{serviceProviderLabel(service)} · {service.default_branch}</small>
                    </span>
                    <span className={styles.serviceState} aria-hidden />
                  </button>
                );
              })}
            </nav>
          ) : (
            <p className={styles.railEmpty}>No service connected yet.</p>
          )}
          {railAction && <div className={styles.railAction}>{railAction}</div>}
        </div>

        {railFooter && <div className={styles.railFooter}>{railFooter}</div>}
      </aside>

      <section className={styles.surface} aria-label={`${projectName} workspace`}>
        <div className={styles.utility}>
          <div className={styles.utilityContent}>{utility}</div>
          {mobileActions && <div className={styles.mobileActions}>{mobileActions}</div>}
        </div>
        {mode === 'workspace' && (
          <>
            <header className={styles.header}>{header}</header>
            <div className={styles.tabs} role="tablist" aria-label="Project workspace sections" onKeyDown={onTabsKeyDown}>
              {tabs.map((tab) => (
                <button
                  key={tab}
                  id={`workspace-tab-${tab}`}
                  type="button"
                  role="tab"
                  aria-selected={activeTab === tab}
                  aria-controls={`workspace-panel-${tab}`}
                  tabIndex={activeTab === tab ? 0 : -1}
                  className={styles.tab}
                  data-active={activeTab === tab || undefined}
                  onClick={() => selectTab(tab)}
                >
                  {tab === 'tasks' ? 'Tasks' : tab === 'automations' ? 'Automations' : 'Service settings'}
                </button>
              ))}
            </div>
          </>
        )}
        <div
          ref={scrollRef}
          className={`${styles.scroll} ${mode === 'detail' ? styles.detailScroll : mode === 'settings' ? styles.settingsScroll : ''}`}
          data-testid="project-workspace-scroll"
          data-scroll-owner={mode}
        >
          <div
            {...(mode === 'workspace'
              ? {
                  id: `workspace-panel-${activeTab}`,
                  role: 'tabpanel',
                  'aria-labelledby': `workspace-tab-${activeTab}`,
                }
              : {})}
            className={`${styles.panel} ${mode === 'detail' ? styles.detailPanel : mode === 'settings' ? styles.settingsPanel : ''}`}
          >
            {children}
          </div>
        </div>
      </section>
    </div>
  );
}
