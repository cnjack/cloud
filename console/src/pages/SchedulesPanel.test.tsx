import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import { ApiError } from '../api/client';
import type { ApiClient } from '../api/client';
import type { Schedule, Service } from '../api/types';
import { SchedulesPanel } from './SchedulesPanel';

const service = { id: 'svc1', project_id: 'p1', name: 'default' } as Service;

function mkSchedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 'sc-1',
    service_id: 'svc1',
    cron_expr: '0 9 * * 1-5',
    prompt: 'morning digest',
    enabled: true,
    last_fired_at: null,
    last_error: '',
    created_at: '2026-07-09T00:00:00Z',
    updated_at: '2026-07-09T00:00:00Z',
    ...over,
  };
}

// A stub ApiClient recording schedule calls into a control object.
function makeClient(initial: Schedule[] = []) {
  const state = { schedules: [...initial] };
  const ctl = {
    creates: [] as { serviceId: string; input: unknown }[],
    updates: [] as { scheduleId: string; input: unknown }[],
    deletes: [] as string[],
    createError: null as ApiError | null,
  };
  const client = {
    listServiceSchedules: async () => [...state.schedules],
    createServiceSchedule: async (serviceId: string, input: Record<string, unknown>) => {
      ctl.creates.push({ serviceId, input });
      if (ctl.createError) throw ctl.createError;
      const sc = mkSchedule({
        id: 'sc-new',
        service_id: serviceId,
        cron_expr: String(input.cron_expr),
        prompt: String(input.prompt),
      });
      state.schedules.push(sc);
      return sc;
    },
    updateSchedule: async (scheduleId: string, input: Record<string, unknown>) => {
      ctl.updates.push({ scheduleId, input });
      const sc = state.schedules.find((s) => s.id === scheduleId)!;
      Object.assign(sc, input);
      return { ...sc };
    },
    deleteSchedule: async (scheduleId: string) => {
      ctl.deletes.push(scheduleId);
      state.schedules = state.schedules.filter((s) => s.id !== scheduleId);
    },
  } as unknown as ApiClient;
  return { client, ctl };
}

function renderPanel(client: ApiClient, canManage: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <SchedulesPanel service={service} canManage={canManage} />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('SchedulesPanel', () => {
  it('owner creates a schedule with cron + prompt', async () => {
    const { client, ctl } = makeClient();
    renderPanel(client, true);

    fireEvent.click(screen.getByTestId('schedule-add-open'));
    fireEvent.change(screen.getByTestId('schedule-cron'), { target: { value: '0 9 * * 1-5' } });
    fireEvent.change(screen.getByTestId('schedule-prompt'), { target: { value: 'morning digest' } });
    fireEvent.click(screen.getByTestId('schedule-create'));

    await waitFor(() => expect(ctl.creates).toHaveLength(1));
    expect(ctl.creates[0]).toEqual({
      serviceId: 'svc1',
      input: { cron_expr: '0 9 * * 1-5', prompt: 'morning digest' },
    });
    // The new row shows after the list refetch.
    await waitFor(() => expect(screen.getByTestId('schedules-list')).toBeTruthy());
  });

  it('surfaces a fail-visible cron validation error from the server as a toast', async () => {
    const { client, ctl } = makeClient();
    ctl.createError = new ApiError(400, 'cron fires too frequently: min interval is 5 minutes', {
      error: { code: 'cron_too_frequent', message: 'cron fires too frequently: min interval is 5 minutes' },
    });
    renderPanel(client, true);

    fireEvent.click(screen.getByTestId('schedule-add-open'));
    fireEvent.change(screen.getByTestId('schedule-cron'), { target: { value: '* * * * *' } });
    fireEvent.change(screen.getByTestId('schedule-prompt'), { target: { value: 'p' } });
    fireEvent.click(screen.getByTestId('schedule-create'));

    // The typed server message reaches the toast verbatim (fail-visible).
    await waitFor(() =>
      expect(screen.getByText('cron fires too frequently: min interval is 5 minutes')).toBeTruthy(),
    );
  });

  it('owner toggles enabled and deletes', async () => {
    const { client, ctl } = makeClient([mkSchedule({ enabled: true })]);
    renderPanel(client, true);

    await waitFor(() => expect(screen.getByTestId('schedule-row-sc-1')).toBeTruthy());
    fireEvent.click(screen.getByTestId('schedule-toggle-sc-1'));
    await waitFor(() => expect(ctl.updates).toHaveLength(1));
    expect(ctl.updates[0]).toEqual({ scheduleId: 'sc-1', input: { enabled: false } });

    fireEvent.click(screen.getByTestId('schedule-delete-sc-1'));
    await waitFor(() => expect(ctl.deletes).toEqual(['sc-1']));
  });

  it('owner edits cron + prompt inline', async () => {
    const { client, ctl } = makeClient([mkSchedule()]);
    renderPanel(client, true);

    await waitFor(() => expect(screen.getByTestId('schedule-row-sc-1')).toBeTruthy());
    fireEvent.click(screen.getByTestId('schedule-edit-sc-1'));
    fireEvent.change(screen.getByTestId('schedule-edit-cron-sc-1'), { target: { value: '0 0 * * *' } });
    fireEvent.change(screen.getByTestId('schedule-edit-prompt-sc-1'), { target: { value: 'nightly' } });
    fireEvent.click(screen.getByTestId('schedule-edit-save-sc-1'));

    await waitFor(() => expect(ctl.updates).toHaveLength(1));
    expect(ctl.updates[0]).toEqual({
      scheduleId: 'sc-1',
      input: { cron_expr: '0 0 * * *', prompt: 'nightly' },
    });
  });

  it('shows a fail-visible red badge when a schedule has last_error', async () => {
    const { client } = makeClient([mkSchedule({ last_error: 'no model is configured for this project' })]);
    renderPanel(client, true);

    await waitFor(() => expect(screen.getByTestId('schedule-error-sc-1')).toBeTruthy());
    const badge = screen.getByTestId('schedule-error-sc-1');
    expect(badge.getAttribute('title')).toBe('no model is configured for this project');
  });

  it('is read-only for a member: no add/toggle/edit/delete controls', async () => {
    const { client } = makeClient([mkSchedule()]);
    renderPanel(client, false);

    await waitFor(() => expect(screen.getByTestId('schedule-row-sc-1')).toBeTruthy());
    // The cron is visible (read access) …
    expect(screen.getByTestId('schedule-cron-sc-1').textContent).toBe('0 9 * * 1-5');
    // … but every mutating control is absent.
    expect(screen.queryByTestId('schedule-add-open')).toBeNull();
    expect(screen.queryByTestId('schedule-toggle-sc-1')).toBeNull();
    expect(screen.queryByTestId('schedule-edit-sc-1')).toBeNull();
    expect(screen.queryByTestId('schedule-delete-sc-1')).toBeNull();
  });

  it('shows an empty state when there are no schedules', async () => {
    const { client } = makeClient([]);
    renderPanel(client, true);
    await waitFor(() => expect(screen.getByTestId('schedules-empty')).toBeTruthy());
  });
});
