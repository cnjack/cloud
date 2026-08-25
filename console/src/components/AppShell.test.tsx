import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Role } from '../api/config';
import { AppShell } from './AppShell';

function renderShell(role: Role, initialEntry = '/') {
  const qc = new QueryClient();
  const client = {} as ApiClient;
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <AppShell><div>content</div></AppShell>
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('AppShell — unified account information architecture', () => {
  it('puts Cluster settings in the account menu for a cluster-admin', () => {
    renderShell('cluster-admin', '/cluster');
    fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));
    expect(screen.getByRole('menuitem', { name: /Cluster settings/ })).toBeTruthy();
  });

  it('hides Cluster settings from a non-admin account menu', () => {
    renderShell('project-admin', '/code-reviews');
    fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));
    expect(screen.queryByRole('menuitem', { name: /Cluster settings/ })).toBeNull();
  });

  it.each(['/cluster', '/cluster/models', '/cluster/connections', '/code-reviews', '/not-found'])(
    'uses the unified account header for %s',
    (path) => {
      renderShell('cluster-admin', path);
      expect(screen.getByRole('button', { name: 'Account menu' })).toBeTruthy();
    },
  );

  it.each(['/', '/repositories'])(
    'lets Work Home own the header at %s',
    (path) => {
      const { container } = renderShell('cluster-admin', path);
      expect(container.querySelector('[data-work-home="true"]')).toBeTruthy();
      expect(screen.queryByRole('button', { name: 'Account menu' })).toBeNull();
    },
  );

  it.each(['/account/settings', '/devices/guide', '/setup'])(
    'does not add a duplicate header to %s',
    (path) => {
      renderShell('cluster-admin', path);
      expect(screen.queryByRole('button', { name: 'Account menu' })).toBeNull();
    },
  );

  it('renders runs as a full product workspace', () => {
    const { container } = renderShell('cluster-admin', '/runs/run-1');
    expect(container.querySelector('[data-run-workspace="true"]')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Account menu' })).toBeNull();
  });

  it('renders a Remote conversation as a full product workspace', () => {
    const { container } = renderShell('cluster-admin', '/devices/device-1/sessions/session-1');
    expect(container.querySelector('[data-device-session="true"]')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Account menu' })).toBeNull();
  });

  it('renders device authorization outside account navigation', () => {
    const { container } = renderShell('cluster-admin', '/device?user_code=HTTP-1921');
    expect(container.querySelector('[data-device-authorization="true"]')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Account menu' })).toBeNull();
  });
});
