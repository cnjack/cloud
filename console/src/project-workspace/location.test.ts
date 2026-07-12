import { describe, expect, it } from 'vitest';
import type { Service } from '../api/types';
import { resolveWorkspaceLocation } from './location';

function service(id: string, name: string): Service {
  return {
    id,
    project_id: 'project',
    name,
    repo_kind: 'provider',
    provider: 'gitea',
    repo_owner_name: `acme/${name}`,
    default_branch: 'main',
    git_mode: 'readonly',
    created_at: '',
  };
}

describe('resolveWorkspaceLocation', () => {
  const services = [service('svc-web', 'web'), service('svc-default', 'default')];

  it('keeps a valid service and tab from the URL', () => {
    expect(
      resolveWorkspaceLocation(services, new URLSearchParams('service=svc-web&tab=automations'), true),
    ).toEqual({ serviceId: 'svc-web', tab: 'automations', needsNormalization: false });
  });

  it('normalizes missing or stale route state to the default service and Tasks', () => {
    expect(resolveWorkspaceLocation(services, new URLSearchParams('service=gone&tab=unknown'), true)).toEqual({
      serviceId: 'svc-default',
      tab: 'tasks',
      needsNormalization: true,
    });
  });

  it('does not expose the Settings tab to a member or viewer', () => {
    expect(resolveWorkspaceLocation(services, new URLSearchParams('service=svc-web&tab=settings'), false)).toEqual({
      serviceId: 'svc-web',
      tab: 'tasks',
      needsNormalization: true,
    });
  });

  it('returns an empty service selection for an empty project', () => {
    expect(resolveWorkspaceLocation([], new URLSearchParams('tab=tasks'), true)).toEqual({
      serviceId: '',
      tab: 'tasks',
      needsNormalization: false,
    });
  });
});
