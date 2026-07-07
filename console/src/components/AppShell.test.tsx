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
import { AppShell } from './AppShell';

function renderShell(role: Role) {
  const qc = new QueryClient();
  const client = { listProjects: async () => [] } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={['/']}>
          <AppShell>
            <div>content</div>
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
});
