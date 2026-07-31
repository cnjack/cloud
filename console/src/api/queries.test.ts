import { describe, expect, it } from 'vitest';
import type { AutomationExecution, KanbanCardExecution } from './types';
import {
  automationExecutionPollInterval,
  kanbanCardExecutionPollInterval,
} from './queries';

function execution(status: KanbanCardExecution['status']): KanbanCardExecution {
  return {
    id: `occ-${status}`,
    status,
    summary: status,
    reason: null,
    repair_role: null,
    requested_actor: null,
    run: null,
    receipt: { external: 'not_required', writeback: 'not_required' },
    created_at: '',
    updated_at: '',
  };
}

describe('kanbanCardExecutionPollInterval', () => {
  it('keeps polling an empty Card until its first receipt appears', () => {
    expect(kanbanCardExecutionPollInterval(undefined)).toBe(5_000);
    expect(kanbanCardExecutionPollInterval([])).toBe(5_000);
  });

  it('polls active or repairable receipts and stops for terminal-only history', () => {
    for (const status of ['received', 'blocked', 'queued', 'running'] as const) {
      expect(kanbanCardExecutionPollInterval([execution(status)])).toBe(5_000);
    }
    expect(kanbanCardExecutionPollInterval([execution('terminal')])).toBe(false);
  });
});

function automationExecution(
  state: AutomationExecution['state'],
  outputMode: AutomationExecution['output_mode'] = 'run_only',
): AutomationExecution {
  return {
    id: `execution-${state}`,
    automation_id: 'automation',
    automation_name: 'Nightly',
    trigger_kind: 'cron',
    state,
    output_mode: outputMode,
    requested_actor: null,
    accountable_actor: null,
    output: { kind: 'none', label: 'No output', available: false },
    run: null,
    card: null,
    writeback_state: '',
    usage_summary: {
      availability: 'unavailable',
      reason: 'no_requests',
      requests: 0,
      capture: { reported: 0, partial: 0, unavailable: 0, parse_error: 0 },
      tokens: { input: null, output: null, cache_read: null, cache_write: null },
      costs: { reported: [], estimated: [], uncosted: [] },
    },
    created_at: '',
    updated_at: '',
  };
}

describe('automationExecutionPollInterval', () => {
  it('polls active executions and blocked Card materialization that can recover', () => {
    for (const state of ['accepted', 'queued', 'running'] as const) {
      expect(automationExecutionPollInterval([automationExecution(state)])).toBe(5_000);
    }
    expect(automationExecutionPollInterval([
      automationExecution('blocked', 'create_card'),
    ])).toBe(5_000);
  });

  it('stops for terminal decisions and blocked direct Runs', () => {
    expect(automationExecutionPollInterval([
      automationExecution('terminal'),
      automationExecution('ignored'),
      automationExecution('blocked', 'run_only'),
    ])).toBe(false);
  });
});
