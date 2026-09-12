import { describe, expect, it } from 'vitest';
import type { Run } from './types';
import { reconcileRunMutation } from './runCache';

const run = { id: 'one', project_id: 'project', prompt: 'test', created_at: '', status: 'running' } as Run;

describe('Run mutation projections', () => {
  it('retains authoritative detail projections until GET refreshes them', () => {
    const detailed = { ...run, provenance: { requested: 'actor' }, usage_summary: { requests: 1 }, scm_grant: { provider: 'github' } } as unknown as Run;
    const next = reconcileRunMutation(detailed, { ...run, status: 'awaiting_input' });
    expect(next.status).toBe('awaiting_input');
    expect(next.provenance).toBe(detailed.provenance);
    expect(next.usage_summary).toBe(detailed.usage_summary);
    expect(next.scm_grant).toBe(detailed.scm_grant);
  });
  it('does not carry another Run identity or regress a terminal outcome', () => {
    const finished = { ...run, status: 'succeeded', provenance: { requested: 'old' } } as unknown as Run;
    expect(reconcileRunMutation(finished, run)).toBe(finished);
    const retry = { ...run, id: 'two', status: 'queued' } as Run;
    expect(reconcileRunMutation(finished, retry)).toBe(retry);
  });
});
