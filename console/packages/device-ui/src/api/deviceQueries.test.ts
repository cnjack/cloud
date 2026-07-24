import { describe, expect, it } from 'vitest';
import { sortDeviceSessions } from './deviceQueries';

describe('sortDeviceSessions', () => {
  it('uses real activity, a stable id tie-break, and puts legacy rows last', () => {
    const sessions = sortDeviceSessions([
      { session_id: 'legacy', status: 'idle', meta: {}, updated_at: '2026-03-02T00:00:00Z' },
      { session_id: 'b', status: 'idle', meta: {}, last_activity_at: '2026-03-01T01:00:00Z', updated_at: '2026-03-02T00:00:00Z' },
      { session_id: 'a', status: 'idle', meta: {}, last_activity_at: '2026-03-01T01:00:00Z', updated_at: '2026-03-02T00:00:00Z' },
      { session_id: 'newest', status: 'idle', meta: {}, last_activity_at: '2026-03-01T02:00:00Z', updated_at: '2026-03-02T00:00:00Z' },
    ]);

    expect(sessions.map((session) => session.session_id)).toEqual(['newest', 'a', 'b', 'legacy']);
  });
});
