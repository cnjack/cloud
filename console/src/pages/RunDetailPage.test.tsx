/*
 * RunDetailPage.test.tsx — page-level error-state coverage:
 *   - Finding #4: a failed *refetch* while run.data is cached must NOT swap the
 *     whole page to the ErrorBlock dead-end; the cached run stays rendered with
 *     a non-blocking notice.
 *   - Finding #3: a fatal stream error while the run is non-terminal surfaces a
 *     stream-error banner with a Reconnect action.
 */
import { describe, expect, it, vi } from 'vitest';

import { render, screen, waitFor, act, fireEvent } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import { ApiError, type ApiClient, type StreamCallbacks, type StreamHandle } from '../api/client';
import type { MemberRole, PrInfo, Project, ProjectModel, Run } from '../api/types';
import { qk } from '../api/queries';
import { RunDetailPage } from './RunDetailPage';

function baseRun(overrides: Partial<Run> = {}): Run {
  return {
    id: 'run1',
    project_id: 'proj1',
    prompt: 'Add a line Hello to README',
    status: 'running',
    attempt: 1,
    created_at: '2026-07-07T00:00:00Z',
    started_at: '2026-07-07T00:00:01Z',
    finished_at: null,
    ...overrides,
  };
}

interface Ctl {
  streamCalls: { cb: StreamCallbacks }[];
  getRun: ReturnType<typeof vi.fn>;
}

const SAMPLE_PR: PrInfo = {
  url: 'https://gitea.local/jcloud/seed/pulls/42',
  state: 'open',
  head_branch: 'jcode/run-run1',
  review_runs: [],
};

function makeClient(
  role?: MemberRole,
  opts: { modelConfigured?: boolean; models?: ProjectModel[] } = {},
): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { streamCalls: [], getRun: vi.fn() };
  const project: Project = {
    id: 'proj1',
    name: 'demo',
    created_at: '',
    role: role ?? 'owner',
    services: [
      {
        id: 'svc-1',
        project_id: 'proj1',
        name: 'orchestrator',
        repo_kind: 'provider',
        provider: 'gitea',
        repo_owner_name: 'jcloud/orchestrator',
        default_branch: 'main',
        git_mode: 'draft_pr',
        created_at: '',
      },
    ],
  };
  const client: Partial<ApiClient> = {
    getRun: ctl.getRun as ApiClient['getRun'],
    listEvents: async () => [],
    streamRun: (_id: string, _after: number, cb: StreamCallbacks): StreamHandle => {
      ctl.streamCalls.push({ cb });
      return { close: () => {} };
    },
    diffDownloadUrl: () => '',
    getPR: async () => SAMPLE_PR,
    requestReview: async () => baseRun({ id: 'rev_new', kind: 'review', status: 'queued' }),
    // D21: Retry keys enable/disable off the project's models. Default configured
    // via the env fallback.
    listProjectModels: async () => ({
      models: opts.models ?? [],
      env_fallback: opts.models ? false : opts.modelConfigured ?? true,
    }),
    // The page reads the run's project to learn the requesting principal's role.
    getProject: async () => project,
  };
  return { client: client as ApiClient, ctl };
}

function renderPage(client: ApiClient, seed?: Run) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  if (seed) qc.setQueryData(qk.run(seed.id), seed);
  const ui = (
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/runs/run1']}>
            <Routes>
              <Route path="/runs/:runId" element={<RunDetailPage />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>
  );
  return { qc, ...render(ui) };
}

describe('RunDetailPage — resilient error states', () => {
  it('uses the shared Project shell and the design task-detail hierarchy', async () => {
    const { client, ctl } = makeClient('member');
    const run = baseRun({ service_id: 'svc-1' });
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);

    expect(await screen.findByTestId('run-workspace')).toBeTruthy();
    expect(screen.getByTestId('project-workspace-shell')).toBeTruthy();
    expect(screen.getByTestId('service-rail-svc-1')).toBeTruthy();
    expect(screen.getByTestId('run-status-header')).toBeTruthy();
    expect(screen.getByTestId('run-initial-prompt').textContent).toContain('Add a line Hello');
    expect(screen.getByTestId('run-inspector').textContent).toContain('Run overview');
    expect(screen.getByTestId('run-inspector').textContent).toContain('Changes');
    expect(screen.getByTestId('run-inspector').textContent).toContain('Execution');
    expect(screen.getByTestId('run-back-to-project').getAttribute('href')).toBe('/projects/proj1');
    expect(screen.queryByTestId('tab-events')).toBeNull();
  });

  it('keeps Project navigation in the fixed rail on a run route', async () => {
    const { client, ctl } = makeClient('owner');
    const run = baseRun({ service_id: 'svc-1' });
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);

    await screen.findByTestId('run-workspace');
    const projectSettings = screen.getByTestId('project-settings-trigger');
    expect(projectSettings.closest('[data-testid="project-administration"]')).toBeTruthy();
    expect(projectSettings.closest('[data-testid="project-summary"]')).toBeTruthy();
    expect(projectSettings.getAttribute('href')).toBe(
      '/projects/proj1?service=svc-1&tab=tasks&view=project-settings',
    );
    expect(screen.getByTestId('project-workspace-scroll').getAttribute('data-scroll-owner')).toBe('detail');
  });

  it('keeps the cached run rendered when a refetch fails (no whole-page dead-end)', async () => {
    const { client, ctl } = makeClient();
    // First read (initial mount) succeeds; a later refetch rejects.
    ctl.getRun
      .mockResolvedValueOnce(baseRun())
      .mockRejectedValue(new ApiError(500, 'orchestrator hiccup'));

    const { qc } = renderPage(client, baseRun());

    // Page shows the run header (not the ErrorBlock).
    await waitFor(() =>
      expect(screen.getByTestId('run-status-header')).toBeTruthy(),
    );

    // Force a failing refetch (as useRunStream does on terminal status).
    await act(async () => {
      await qc.refetchQueries({ queryKey: qk.run('run1') });
    });

    // The query is in error, but data is still cached → page stays, no dead-end.
    expect(screen.queryByText("Couldn't load run")).toBeNull();
    expect(screen.getByTestId('run-status-header')).toBeTruthy();
  });

  it('surfaces a stream-error banner with a Reconnect action on fatal SSE error', async () => {
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(baseRun());
    renderPage(client, baseRun());

    await waitFor(() => expect(ctl.streamCalls.length).toBe(1));

    act(() => ctl.streamCalls[0]!.cb.onError?.(new ApiError(401, 'unauthorized')));

    await waitFor(() => expect(screen.getByTestId('stream-error')).toBeTruthy());
    expect(screen.getByTestId('stream-reconnect')).toBeTruthy();
  });

  // ST-1: the draft PR chip renders only when pr_url is present, shows the mono
  // "#N", and opens the Gitea PR in a new tab.
  it('renders the draft-PR chip with the PR number and opens in a new tab', async () => {
    const prRun = baseRun({
      status: 'succeeded',
      finished_at: '2026-07-07T00:05:00Z',
      pr_url: 'https://gitea.local/jcloud/seed/pulls/42',
      pr_number: 42,
    });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(prRun);
    renderPage(client, prRun);

    const link = (await screen.findByTestId('pr-link')) as HTMLAnchorElement;
    expect(link.textContent).toContain('Draft PR');
    expect(link.textContent).toContain('#42');
    expect(link.getAttribute('href')).toBe('https://gitea.local/jcloud/seed/pulls/42');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noreferrer');
  });

  // M7 (blueprint §8): a webhook-origin run links back to the triggering PR
  // comment via a "from PR comment ↗" chip; an api-origin run does not.
  it('renders the origin chip for a webhook-triggered run', async () => {
    const whRun = baseRun({
      origin: 'webhook',
      origin_comment_url: 'https://gitea.local/jcloud/seed/pulls/7#issuecomment-42',
    });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(whRun);
    renderPage(client, whRun);

    const chip = (await screen.findByTestId('origin-chip')) as HTMLAnchorElement;
    expect(chip.textContent).toContain('from PR comment');
    expect(chip.getAttribute('href')).toBe(
      'https://gitea.local/jcloud/seed/pulls/7#issuecomment-42',
    );
    expect(chip.getAttribute('target')).toBe('_blank');
    expect(chip.getAttribute('rel')).toContain('noreferrer');
  });

  it('does not render the origin chip for an api-origin run', async () => {
    const apiRun = baseRun({ origin: 'api' });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(apiRun);
    renderPage(client, apiRun);

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('origin-chip')).toBeNull();
  });

  // F11 / D24: a schedule-origin run shows a static "scheduled" chip (no link).
  it('renders a "scheduled" chip for a schedule-triggered run', async () => {
    const schedRun = baseRun({ origin: 'schedule' });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(schedRun);
    renderPage(client, schedRun);

    const chip = await screen.findByTestId('origin-chip-schedule');
    expect(chip.textContent).toContain('scheduled');
    // Not a link (no external target to open).
    expect(chip.tagName).toBe('SPAN');
    // And the webhook PR-comment chip is not shown.
    expect(screen.queryByTestId('origin-chip')).toBeNull();
  });

  // No PR link when the run has no pr_url (readonly / diff-only run).
  it('does not render the draft-PR chip when pr_url is absent', async () => {
    const noPr = baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(noPr);
    renderPage(client, noPr);

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('pr-link')).toBeNull();
  });
});

describe('RunDetailPage — no_changes result (D18/D26)', () => {
  it('shows the "No changes" badge and the diff-tab empty state, without fetching the diff', async () => {
    const noChangesRun = baseRun({
      status: 'succeeded',
      finished_at: '2026-07-07T00:05:00Z',
      result: 'no_changes',
    });
    const { client, ctl } = makeClient();
    const getDiff = vi.fn().mockResolvedValue({ run_id: 'run1', kind: 'diff', content: '', created_at: '' });
    (client as { getDiff?: unknown }).getDiff = getDiff;
    ctl.getRun.mockResolvedValue(noChangesRun);
    renderPage(client, noChangesRun);

    expect(await screen.findByTestId('no-changes-badge')).toBeTruthy();

    const diffTab = await screen.findByTestId('tab-diff');
    act(() => diffTab.click());
    expect(await screen.findByTestId('diff-no-changes')).toBeTruthy();
    expect(screen.getByText('This run made no code changes.')).toBeTruthy();
    // The diff endpoint is never hit — result: no_changes already tells us
    // there's nothing to fetch (fail-visible / no wasted round trip).
    expect(getDiff).not.toHaveBeenCalled();
  });

  it('does not show the "No changes" badge for an ordinary succeeded run', async () => {
    const normalRun = baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(normalRun);
    renderPage(client, normalRun);

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('no-changes-badge')).toBeNull();
  });
});

describe('RunDetailPage — viewer gating (blueprint §2)', () => {
  it('hides Cancel on a running run for a viewer', async () => {
    const { client, ctl } = makeClient('viewer');
    ctl.getRun.mockResolvedValue(baseRun({ status: 'running' }));
    renderPage(client, baseRun({ status: 'running' }));

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    await waitFor(() => expect(screen.queryByTestId('cancel-btn')).toBeNull());
    expect(screen.queryByTestId('retry-btn')).toBeNull();
  });

  it('shows Retry on a finished run for a member', async () => {
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(baseRun({ status: 'failed', finished_at: '2026-07-07T00:05:00Z' }));
    renderPage(client, baseRun({ status: 'failed', finished_at: '2026-07-07T00:05:00Z' }));

    await waitFor(() => expect(screen.getByTestId('retry-btn')).toBeTruthy());
  });
});

describe('RunDetailPage — model gate on Retry (Feature A)', () => {
  it('disables Retry with a notice when no LLM is configured', async () => {
    const failed = baseRun({ status: 'failed', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient('member', { modelConfigured: false });
    ctl.getRun.mockResolvedValue(failed);
    renderPage(client, failed);

    await waitFor(() => expect(screen.getByTestId('retry-btn')).toBeTruthy());
    await waitFor(() =>
      expect((screen.getByTestId('retry-btn') as HTMLButtonElement).disabled).toBe(true),
    );
    expect(screen.getByTestId('model-not-configured')).toBeTruthy();
  });
});

describe('RunDetailPage — multi-turn session (D22)', () => {
  it('shows the follow-up composer + Finish button while awaiting input, and sends a message', async () => {
    const sessionRun = baseRun({ status: 'awaiting_input', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(sessionRun);
    const sendMessage = vi
      .fn()
      .mockResolvedValue({ id: 'm1', run_id: 'run1', seq: 1, prompt: 'more', created_at: '' });
    (client as { sendMessage?: unknown }).sendMessage = sendMessage;
    renderPage(client, sessionRun);

    const panel = await screen.findByTestId('session-panel');
    expect(panel).toBeTruthy();
    const conversationScroll = screen.getByTestId('conversation-scroll');
    expect(conversationScroll.contains(panel)).toBe(false);
    expect(screen.getByTestId('conversation-column').contains(panel)).toBe(true);
    expect(panel.querySelector('.jcode-chat-input')).toBeNull();
    expect(panel.querySelector('[data-testid="conversation-composer"]')).toBeTruthy();
    const input = (await screen.findByLabelText('Message input')) as HTMLTextAreaElement;
    const send = (await screen.findByLabelText('Send message')) as HTMLButtonElement;
    expect(screen.getByTestId('session-finish-btn')).toBeTruthy();
    expect((screen.getByTestId('conversation-model-select') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId('conversation-permission-select') as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('Model and access apply when you resume.')).toBeTruthy();

    // Empty input keeps Send disabled; typing enables it and submit calls the API.
    expect(send.disabled).toBe(true);
    fireEvent.change(input, { target: { value: 'do the next step' } });
    await waitFor(() => expect(send.disabled).toBe(false));
    fireEvent.click(send);
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith('run1', 'do the next step'));
    // The composer clears after a successful send.
    await waitFor(() => expect(input.value).toBe(''));
  });

  it('Finish session calls the finish endpoint', async () => {
    const sessionRun = baseRun({ status: 'awaiting_input', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(sessionRun);
    const finishSession = vi.fn().mockResolvedValue(baseRun({ status: 'awaiting_input', session: true }));
    (client as { finishSession?: unknown }).finishSession = finishSession;
    renderPage(client, sessionRun);

    const finish = await screen.findByTestId('session-finish-btn');
    fireEvent.click(finish);
    await waitFor(() => expect(finishSession).toHaveBeenCalledWith('run1'));
  });

  it('hides the session panel for a non-session run and for a viewer', async () => {
    // Non-session run in awaiting-adjacent state: no panel.
    const plain = baseRun({ status: 'running' });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(plain);
    const first = renderPage(client, plain);
    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('session-panel')).toBeNull();
    first.unmount();

    // Session run but the principal is a viewer: no panel (backend 403s anyway).
    const sessionRun = baseRun({ status: 'awaiting_input', session: true });
    const viewer = makeClient('viewer');
    viewer.ctl.getRun.mockResolvedValue(sessionRun);
    renderPage(viewer.client, sessionRun);
    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    await waitFor(() => expect(screen.queryByTestId('session-panel')).toBeNull());
  });

  it('renders the composer while a session turn is RUNNING (message queues) with the after-turn placeholder', async () => {
    const busy = baseRun({ status: 'running', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(busy);
    const sendMessage = vi
      .fn()
      .mockResolvedValue({ id: 'm1', run_id: 'run1', seq: 1, prompt: 'q', created_at: '' });
    const cancelRun = vi.fn().mockResolvedValue(baseRun({ status: 'canceled', session: true }));
    (client as { sendMessage?: unknown }).sendMessage = sendMessage;
    (client as { cancelRun?: unknown }).cancelRun = cancelRun;
    renderPage(client, busy);

    await screen.findByTestId('session-panel');
    // The backend queues messages while running — the composer stays available,
    // with a placeholder saying the message is handled after this turn.
    const input = (await screen.findByLabelText('Message input')) as HTMLTextAreaElement;
    expect(input.placeholder).toContain('after the current turn');
    const stop = (await screen.findByLabelText('Stop')) as HTMLButtonElement;
    expect(stop).toBeTruthy();
    expect(screen.getByTestId('session-finish-btn')).toBeTruthy();
    // The Finish-vs-Cancel semantics hint is present where both actions coexist.
    expect(screen.getByTestId('session-actions-hint').textContent).toContain('Cancel');

    fireEvent.change(input, { target: { value: 'queue me' } });
    // Enter sends the native Console composer and Cloud maps it to its durable
    // follow-up queue.
    fireEvent.keyDown(input, { key: 'Enter', code: 'Enter' });
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith('run1', 'queue me'));

    // Package Stop is the immediate/discard path; graceful completion remains
    // the explicit Finish session button beside the composer.
    fireEvent.click(stop);
    await waitFor(() => expect(cancelRun).toHaveBeenCalledWith('run1'));
  });

  it('shows a neutral waiting note (no composer, no "agent working") for a QUEUED session', async () => {
    const queued = baseRun({ status: 'queued', session: true, started_at: null });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(queued);
    renderPage(client, queued);

    await screen.findByTestId('session-panel');
    expect(screen.queryByLabelText('Message input')).toBeNull();
    const note = screen.getByTestId('session-pending-note');
    expect(note.textContent).toContain('waiting for a free session slot');
    expect(note.textContent).not.toContain('working');
  });

  it('does not discard text typed while a send is in flight', async () => {
    const sessionRun = baseRun({ status: 'awaiting_input', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(sessionRun);
    // A send we resolve by hand, so we can type DURING the request.
    let resolveSend: (v: unknown) => void = () => {};
    const sendMessage = vi.fn().mockImplementation(
      () => new Promise((res) => (resolveSend = res)),
    );
    (client as { sendMessage?: unknown }).sendMessage = sendMessage;
    renderPage(client, sessionRun);

    const input = (await screen.findByLabelText('Message input')) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: 'first message' } });
    fireEvent.click(screen.getByLabelText('Send message'));
    await waitFor(() => expect(sendMessage).toHaveBeenCalledWith('run1', 'first message'));

    // The user keeps typing while the request is in flight…
    fireEvent.change(input, { target: { value: 'second thought' } });
    // …then the send succeeds: the NEW text must survive (only an unchanged
    // box is cleared).
    resolveSend({ id: 'm1', run_id: 'run1', seq: 1, prompt: 'first message', created_at: '' });
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1));
    expect(input.value).toBe('second thought');
  });

  it('preserves a failed package submission as an explicit retryable draft', async () => {
    const sessionRun = baseRun({ status: 'awaiting_input', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(sessionRun);
    const sendMessage = vi
      .fn()
      .mockRejectedValue(new ApiError(500, 'message queue unavailable'));
    (client as { sendMessage?: unknown }).sendMessage = sendMessage;
    renderPage(client, sessionRun);

    const input = (await screen.findByLabelText('Message input')) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: 'do not lose this draft' } });
    fireEvent.click(screen.getByLabelText('Send message'));

    const failed = await screen.findByTestId('failed-submission');
    expect(failed.textContent).toContain('do not lose this draft');
    fireEvent.click(screen.getByRole('button', { name: 'Retry unsent message' }));
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2));
  });
});

describe('RunDetailPage — session resume (F9b / D23 ①②)', () => {
  const terminalSession = (overrides: Partial<Run> = {}) =>
    baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z', session: true, ...overrides });

  it('shows the Continue-session composer on a terminal session run and resumes', async () => {
    const run = terminalSession({ model_id: 'm_gpt', model_name: 'GPT-4o', permission_mode: 'approval' });
    const { client, ctl } = makeClient('member', {
      models: [
        { id: 'm_gpt', name: 'GPT-4o', model_name: 'openai/gpt-4o' },
        { id: 'm_claude', name: 'Claude', model_name: 'anthropic/claude' },
      ],
    });
    ctl.getRun.mockResolvedValue(run);
    const resumeSession = vi
      .fn()
      .mockResolvedValue(baseRun({ id: 'run2', session: true, status: 'queued', resumed_from: 'run1' }));
    (client as { resumeSession?: unknown }).resumeSession = resumeSession;
    renderPage(client, run);

    const panel = await screen.findByTestId('resume-session-panel');
    const conversationScroll = screen.getByTestId('conversation-scroll');
    expect(conversationScroll.contains(panel)).toBe(false);
    expect(screen.getByTestId('conversation-column').contains(panel)).toBe(true);
    expect(panel.querySelector('.jcode-chat-input')).toBeNull();
    expect(panel.querySelector('[data-testid="conversation-composer"]')).toBeTruthy();
    const input = panel.querySelector('textarea[aria-label="Message input"]') as HTMLTextAreaElement;
    expect(input).toBeTruthy();
    const send = (await screen.findByLabelText('Send message')) as HTMLButtonElement;
    // Empty keeps Continue disabled; typing enables it and submit calls resume.
    expect(send.disabled).toBe(true);
    fireEvent.click(screen.getByTestId('conversation-model-select'));
    expect(await screen.findByLabelText('Filter models')).toBeTruthy();
    expect(screen.getByRole('option', { name: /Claude.*anthropic\/claude/ })).toBeTruthy();
    fireEvent.click(screen.getByRole('option', { name: /Claude.*anthropic\/claude/ }));
    fireEvent.click(screen.getByTestId('conversation-permission-select'));
    const planMode = screen.getByRole('option', { name: /Plan.*not available/i }) as HTMLButtonElement;
    expect(planMode.disabled).toBe(true);
    fireEvent.click(screen.getByRole('option', { name: 'Ask for approval' }));
    fireEvent.change(input, { target: { value: 'pick up where we left off' } });
    await waitFor(() => expect(send.disabled).toBe(false));
    fireEvent.click(send);
    await waitFor(() =>
      expect(resumeSession).toHaveBeenCalledWith('run1', 'pick up where we left off', {
        model_id: 'm_claude',
        permission_mode: 'approval',
      }),
    );
  });

  it('does not show the Continue-session composer on a NON-session terminal run', async () => {
    const run = baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);
    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('resume-session-panel')).toBeNull();
  });

  it('does not show the Continue-session composer while a session is still active', async () => {
    // A live (awaiting_input) session offers the message box, not a fresh resume.
    const run = baseRun({ status: 'awaiting_input', session: true });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);
    await screen.findByTestId('session-panel');
    expect(screen.queryByTestId('resume-session-panel')).toBeNull();
  });

  it('hides the Continue-session composer from a viewer', async () => {
    const run = terminalSession();
    const { client, ctl } = makeClient('viewer');
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);
    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    await waitFor(() => expect(screen.queryByTestId('resume-session-panel')).toBeNull());
  });

  it('surfaces each 409 code\'s readable message when resume is refused', async () => {
    const cases: { code: string; message: string }[] = [
      {
        code: 'run_not_resumable',
        message: 'the session is still active — use the message box to continue it instead of starting a new one',
      },
      {
        code: 'session_not_recorded',
        message: 'this session never recorded an agent session id, so it cannot be resumed',
      },
      {
        code: 'workspace_not_persistent',
        message:
          'resuming a session needs a persistent workspace (the transcript lives on the service\'s PVC), which is not enabled on this cluster',
      },
    ];
    for (const { code, message } of cases) {
      const run = terminalSession();
      const { client, ctl } = makeClient('member');
      ctl.getRun.mockResolvedValue(run);
      const resumeSession = vi
        .fn()
        .mockRejectedValue(new ApiError(409, message, { error: { code, message } }));
      (client as { resumeSession?: unknown }).resumeSession = resumeSession;
      const view = renderPage(client, run);

      const panel = await screen.findByTestId('resume-session-panel');
      const input = panel.querySelector('textarea[aria-label="Message input"]') as HTMLTextAreaElement;
      expect(input).toBeTruthy();
      fireEvent.change(input, { target: { value: 'go' } });
      fireEvent.click(await screen.findByLabelText('Send message'));
      // The server's typed message is shown verbatim (the readable failure reason).
      expect(await screen.findByText(message)).toBeTruthy();
      expect((await screen.findByTestId('failed-submission')).textContent).toContain('go');
      view.unmount();
    }
  });

  it('renders a "resumed from" link back to the original run', async () => {
    const run = baseRun({ status: 'running', session: true, resumed_from: 'origrun9' });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(run);
    renderPage(client, run);
    const link = (await screen.findByTestId('resumed-from')) as HTMLAnchorElement;
    expect(link.textContent).toContain('resumed from');
    expect(link.getAttribute('href')).toContain('/runs/origrun9');
  });
});

describe('RunDetailPage — PR tab + review runs (blueprint §5)', () => {
  it('shows a PR tab for an agent run with a PR and renders the PR panel', async () => {
    const prRun = baseRun({
      status: 'succeeded',
      finished_at: '2026-07-07T00:05:00Z',
      pr_url: 'https://gitea.local/jcloud/seed/pulls/42',
      pr_number: 42,
    });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(prRun);
    renderPage(client, prRun);

    const prTab = await screen.findByTestId('tab-pr');
    act(() => prTab.click());
    await waitFor(() => expect(screen.getByTestId('pr-panel')).toBeTruthy());
    expect(screen.getByTestId('pr-external-link')).toBeTruthy();
  });

  it('does not show a PR tab for an agent run without a PR', async () => {
    const noPr = baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(noPr);
    renderPage(client, noPr);

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('tab-pr')).toBeNull();
  });

  it('renders a review run body as markdown with no Diff/PR tabs', async () => {
    const review = baseRun({
      kind: 'review',
      status: 'succeeded',
      finished_at: '2026-07-07T00:05:00Z',
      review_output: '## Review\n\nThis change is **safe**.',
    });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(review);
    renderPage(client, review);

    const body = await screen.findByTestId('review-output');
    expect(body.textContent).toContain('Review');
    expect(body.querySelector('strong')?.textContent).toBe('safe');
    // A review run has no Diff / PR tabs.
    expect(screen.queryByTestId('tab-diff')).toBeNull();
    expect(screen.queryByTestId('tab-pr')).toBeNull();
  });

  it('shows a review-in-progress state while a review run is still running', async () => {
    const running = baseRun({ kind: 'review', status: 'running' });
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(running);
    renderPage(client, running);

    await waitFor(() => expect(screen.getByTestId('review-in-progress')).toBeTruthy());
  });
});
