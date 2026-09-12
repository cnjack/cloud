export const WORKSPACE_TABS = ['tasks', 'board', 'reviews', 'automations', 'usage', 'settings'] as const;
export type WorkspaceTab = typeof WORKSPACE_TABS[number];

export function workspaceTab(value: string | null): WorkspaceTab {
  return WORKSPACE_TABS.includes(value as WorkspaceTab) ? value as WorkspaceTab : 'tasks';
}

export function repositoryWorkspacePath(repositoryId: string, tab: WorkspaceTab = 'tasks'): string {
  return `/repositories?${new URLSearchParams({ repository: repositoryId, tab })}`;
}
