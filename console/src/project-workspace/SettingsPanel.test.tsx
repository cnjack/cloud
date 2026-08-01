import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { Service } from '../api/types';
import { SettingsPanel } from './SettingsPanel';

const service: Service = {
  id: 'svc',
  project_id: 'project',
  name: 'cloud',
  repo_kind: 'provider',
  provider: 'github',
  repo_owner_name: 'cnjack/cloud',
  default_branch: 'main',
  git_mode: 'draft_pr',
  pr_ready_policy: 'always_draft',
  created_at: '2026-07-31T00:00:00Z',
};

describe('SettingsPanel pull-request delivery policy', () => {
  it('shows Pull request as the capability and changes the completion policy explicitly', async () => {
    const change = vi.fn();
    render(
      <SettingsPanel
        service={service}
        models={[]}
        modelState="ready"
        updating={false}
        onDefaultModelChange={() => undefined}
        onPRReadyPolicyChange={change}
        runnerProfiles={['default', 'go-node']}
        onRunnerProfileChange={() => undefined}
        onRetryModels={() => undefined}
      />,
    );

    expect(screen.getByText('Pull request')).toBeTruthy();
    fireEvent.click(screen.getByTestId('service-pr-ready-policy-select'));
    fireEvent.click(await screen.findByText('Lifecycle aware'));
    expect(change).toHaveBeenCalledWith('lifecycle_aware');
  });
});
