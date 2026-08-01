/*
 * AppShell.test.tsx — identity + role gating in the header:
 *   - the identity chip names the trust level for both roles;
 *   - the "Cluster" nav link is shown only for cluster-admin (presentation-only
 *     gating of the cluster-admin vs project-admin difference).
 */
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Role } from '../api/config';
import type { Project } from '../api/types';
import { AppShell } from './AppShell';

function renderShell(role: Role, initialEntry = '/', projects: Project[] = []) {
  const qc = new QueryClient();
  const client = { listProjects: async () => projects } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <AppShell>
            <div>content</div>
          </AppShell>
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

function renderProjectShell(role: Role) {
  const qc = new QueryClient();
  const client = { listProjects: async () => [] } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={['/projects/project-1']}>
          <AppShell>
            <div>project content</div>
          </AppShell>
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

function renderRunShell(role: Role) {
  const qc = new QueryClient();
  const client = { listProjects: async () => [] } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={['/runs/run-1']}>
          <AppShell>
            <div>run content</div>
          </AppShell>
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

function renderDeviceAuthorizationShell(role: Role) {
  const qc = new QueryClient();
  const client = { listProjects: async () => [] } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={['/device?user_code=HTTP-1921']}>
          <AppShell>
            <div>device authorization</div>
          </AppShell>
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('AppShell — identity + role gating', () => {
  it('shows the Cluster nav and a cluster-admin identity chip for a cluster-admin', () => {
    renderShell('cluster-admin');
    expect(screen.getByTestId('cluster-nav')).toBeTruthy();
    const chip = screen.getByTestId('identity-chip');
    expect(chip.getAttribute('data-role')).toBe('cluster-admin');
    expect(chip.textContent).toContain('Cluster admin');
  });

  it('hides the Cluster nav and shows a project-admin chip for a project-admin', () => {
    renderShell('project-admin');
    expect(screen.queryByTestId('cluster-nav')).toBeNull();
    const chip = screen.getByTestId('identity-chip');
    expect(chip.getAttribute('data-role')).toBe('project-admin');
    expect(chip.textContent).toContain('Project admin');
  });

  it('removes the global topbar for a project workspace route', () => {
    const { container } = renderProjectShell('cluster-admin');
    expect(screen.queryByTestId('cluster-nav')).toBeNull();
    expect(screen.queryByTestId('identity-chip')).toBeNull();
    expect(container.querySelector('[data-project-workspace="true"]')).toBeTruthy();
  });

  it('removes the global topbar for a run workspace route', () => {
    const { container } = renderRunShell('cluster-admin');
    expect(screen.queryByTestId('cluster-nav')).toBeNull();
    expect(screen.queryByTestId('identity-chip')).toBeNull();
    expect(container.querySelector('[data-run-workspace="true"]')).toBeTruthy();
  });

  it('renders device authorization outside the workspace shell', () => {
    const { container } = renderDeviceAuthorizationShell('cluster-admin');
    expect(screen.getByText('device authorization')).toBeTruthy();
    expect(screen.queryByTestId('cluster-nav')).toBeNull();
    expect(screen.queryByTestId('identity-chip')).toBeNull();
    expect(container.querySelector('[data-device-authorization="true"]')).toBeTruthy();
  });

  it('keeps the new-project route in the global shell instead of treating "new" as a project id', () => {
    const { container } = renderShell('cluster-admin', '/projects/new');
    expect(container.querySelector('[data-project-workspace="true"]')).toBeNull();
    expect(screen.getByText('New Project')).toBeTruthy();
  });

  it.each([
    ['/projects/project-1/plugins/github', 'GitHub'],
    ['/projects/project-1/automations/new', 'Create Automation'],
    ['/projects/project-1/automations/new?preset=review', 'Review pull requests'],
    ['/projects/project-1/automations/automation-1', 'Automations'],
    ['/projects/project-1/automations/automation-1/edit', 'Edit Automation'],
    ['/devices/guide', 'Usage guide'],
    ['/devices/device-1', 'Device'],
    ['/devices/device-1/sessions/session-1', 'Session'],
    ['/cluster', 'Overview'],
    ['/cluster/models', 'Models'],
    ['/cluster/connections', 'Connections'],
  ])('shows a truthful breadcrumb for %s', (path, expectedLeaf) => {
    renderShell('cluster-admin', path);
    expect(screen.queryByText('Not found')).toBeNull();
    expect(screen.getByText(expectedLeaf)).toBeTruthy();
  });

  it('resolves the project name for project-scoped breadcrumbs', async () => {
    renderShell(
      'cluster-admin',
      '/projects/project-1/automations/new?preset=review',
      [{ id: 'project-1', name: 'Navigation Lab', created_at: '2026-08-01T00:00:00Z', services: [] }],
    );
    const projectLink = await screen.findByRole('link', { name: 'Navigation Lab' });
    expect(projectLink.getAttribute('href')).toBe('/projects/project-1');
    expect(projectLink.getAttribute('aria-current')).toBeNull();
  });

  it.each([
    '/cluster/unknown',
    '/devices/device-1/unknown',
    '/projects/project-1/unknown',
  ])('keeps the not-found breadcrumb for an unregistered route: %s', (path) => {
    renderShell('cluster-admin', path);
    expect(screen.getByText('Not found')).toBeTruthy();
  });
});
