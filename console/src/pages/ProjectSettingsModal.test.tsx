/*
 * ProjectSettingsModal — M4. Covers:
 *  - General tab PATCH (only changed default_branch + git_mode; draft_pr flip
 *    sends just git_mode — no provider fields, the server parses the repo)
 *  - the Delete flow behind a confirm step
 *  - the Members tab: roster render, add-by-search, role change, remove
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type {
  AddMemberInput,
  Member,
  Project,
  UpdateProjectInput,
  UserSearchResult,
} from '../api/types';
import { ProjectSettingsModal } from './ProjectSettingsModal';

function baseProject(overrides: Partial<Project> = {}): Project {
  return {
    id: 'p1',
    name: 'demo',
    repo_url: 'https://gitea.local/acme/demo.git',
    default_branch: 'main',
    created_at: '2026-07-07T00:00:00Z',
    git_mode: 'readonly',
    role: 'owner',
    ...overrides,
  };
}

interface Ctl {
  patches: { id: string; input: UpdateProjectInput }[];
  deletes: string[];
  added: AddMemberInput[];
  removed: string[];
}

const USERS: UserSearchResult[] = [
  { id: 'u_ada', display_name: 'Ada Lovelace', is_cluster_admin: true },
  { id: 'u_grace', display_name: 'Grace Hopper', is_cluster_admin: false },
];

function makeClient(project: Project): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { patches: [], deletes: [], added: [], removed: [] };
  const members: Member[] = [
    { user_id: 'u_ada', role: 'owner', display_name: 'Ada Lovelace', is_cluster_admin: true },
  ];
  const client: Partial<ApiClient> = {
    updateProject: async (id, input) => {
      ctl.patches.push({ id, input });
      return { ...project, ...input } as Project;
    },
    deleteProject: async (id) => {
      ctl.deletes.push(id);
    },
    listMembers: async () => [...members],
    searchUsers: async (q) =>
      USERS.filter((u) => u.display_name.toLowerCase().includes(q.toLowerCase())),
    addMember: async (_pid, input: AddMemberInput) => {
      ctl.added.push(input);
      const u = USERS.find((x) => x.id === input.user_id)!;
      const m: Member = {
        user_id: u.id,
        role: input.role,
        display_name: u.display_name,
        is_cluster_admin: u.is_cluster_admin,
      };
      const i = members.findIndex((x) => x.user_id === u.id);
      if (i >= 0) members[i] = m;
      else members.push(m);
      return m;
    },
    removeMember: async (_pid, userId) => {
      ctl.removed.push(userId);
    },
  };
  return { client: client as ApiClient, ctl };
}

function renderModal(client: ApiClient, project: Project, onDeleted = vi.fn(), onClose = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <ProjectSettingsModal open project={project} onClose={onClose} onDeleted={onDeleted} />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onDeleted, onClose };
}

describe('ProjectSettingsModal — General (PATCH)', () => {
  it('sends only the changed default_branch plus the git_mode', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onClose } = renderModal(client, project);

    fireEvent.change(screen.getByTestId('settings-branch-input'), { target: { value: 'dev' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.id).toBe('p1');
    expect(ctl.patches[0]!.input).toEqual({ git_mode: 'readonly', default_branch: 'dev' });
    expect(onClose).toHaveBeenCalled();
  });

  it('flips readonly → draft_pr sending only git_mode (no provider fields)', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toEqual({ git_mode: 'draft_pr' });
  });

  it('pre-selects the draft_pr mode for an existing draft_pr project', () => {
    const project = baseProject({ git_mode: 'draft_pr', provider: 'gitea', provider_repo: 'jcloud/seed' });
    const { client } = makeClient(project);
    renderModal(client, project);
    expect(screen.getByTestId('git-mode-draft_pr').getAttribute('aria-checked')).toBe('true');
  });
});

describe('ProjectSettingsModal — Delete', () => {
  it('requires a confirm step, then deletes and fires onDeleted', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onDeleted } = renderModal(client, project);

    fireEvent.click(screen.getByTestId('project-delete'));
    expect(screen.getByTestId('delete-confirm')).toBeTruthy();
    expect(ctl.deletes).toHaveLength(0);

    fireEvent.click(screen.getByTestId('project-delete-confirm'));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(ctl.deletes).toEqual(['p1']);
  });
});

describe('ProjectSettingsModal — Members tab', () => {
  it('lists members and adds one via search', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeTruthy());

    fireEvent.change(screen.getByTestId('member-search-input'), { target: { value: 'grace' } });
    await waitFor(() => expect(screen.getByTestId('member-search-result')).toBeTruthy());
    fireEvent.click(screen.getByTestId('member-search-result'));

    await waitFor(() => expect(ctl.added).toHaveLength(1));
    expect(ctl.added[0]).toMatchObject({ user_id: 'u_grace', role: 'member' });
  });

  it('changes a member role and removes a member', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByTestId('member-role-select')).toBeTruthy());

    fireEvent.change(screen.getByTestId('member-role-select'), { target: { value: 'viewer' } });
    await waitFor(() => expect(ctl.added).toHaveLength(1));
    expect(ctl.added[0]).toMatchObject({ user_id: 'u_ada', role: 'viewer' });

    fireEvent.click(screen.getByTestId('member-remove'));
    await waitFor(() => expect(ctl.removed).toEqual(['u_ada']));
  });
});
