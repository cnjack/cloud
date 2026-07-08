/*
 * eventModel.ts — narrows the loose RunViewEvent payload into typed view
 * models the Timeline renders. Keeps all the "what field is the tool name
 * really in" defensiveness in one place so components stay clean.
 */
import type { RunViewEvent, TimelineItem } from './types';

// Duplicated intentionally (not imported from the host's api/types.ts): this
// module stays decoupled from the console's Run domain so it can be lifted
// into a standalone package later. Keep in sync with the orchestrator's
// RunStatus lifecycle (succeeded/failed/canceled are terminal).
const TERMINAL_STATUSES = new Set(['succeeded', 'failed', 'canceled']);

function isTerminalStatus(status: string): boolean {
  return TERMINAL_STATUSES.has(status);
}

function pretty(value: unknown): string {
  if (value == null) return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function toTimelineItem(ev: RunViewEvent): TimelineItem {
  const base = { seq: ev.seq, ts: ev.ts };
  const p = ev.payload ?? {};

  switch (ev.type) {
    case 'agent.text':
      return { ...base, kind: 'text', text: String(p.text ?? '') };

    case 'agent.tool_call':
      return {
        ...base,
        kind: 'tool_call',
        tool: String(p.tool ?? p.tool_name ?? 'tool'),
        callId: p.call_id ? String(p.call_id) : undefined,
        args: pretty(p.args ?? p.input ?? {}),
      };

    case 'agent.tool_result': {
      // 11-api.md §4: result carries { call_id, ok, output, exit_code }.
      // ok === false OR a non-zero exit_code (OR legacy is_error) => error.
      const isError =
        p.ok === false ||
        p.is_error === true ||
        (typeof p.exit_code === 'number' && p.exit_code !== 0);
      return {
        ...base,
        kind: 'tool_result',
        tool: p.tool ? String(p.tool) : p.tool_name ? String(p.tool_name) : undefined,
        callId: p.call_id ? String(p.call_id) : undefined,
        output: pretty(p.output ?? p.result ?? ''),
        isError,
      };
    }

    case 'run.status':
      return { ...base, kind: 'status', status: String(p.status ?? '') };

    case 'run.failure':
      return {
        ...base,
        kind: 'failure',
        reason: p.reason ? String(p.reason) : undefined,
        message: String(p.message ?? 'Run failed.'),
      };

    case 'run.artifact':
      return { ...base, kind: 'artifact', artifact: String(p.kind ?? 'artifact') };

    case 'run.git':
      return {
        ...base,
        kind: 'git',
        branch: String(p.branch ?? ''),
        commitSha: p.commit_sha ? String(p.commit_sha) : undefined,
      };

    // D18/D26: run.result { outcome: "no_changes" } — a successful run that
    // produced no diff. Rendered as a one-line informational row. Tolerant of
    // an absent/unrecognized outcome (falls back to a generic label) since the
    // backend contract may grow more outcomes later.
    case 'run.result': {
      const outcome = String(p.outcome ?? '');
      return {
        ...base,
        kind: 'result',
        outcome,
        message: outcome === 'no_changes' ? 'No code changes' : outcome || 'Result',
      };
    }

    // D22 session: a follow-up prompt the user fed to the run — rendered as a
    // user chat bubble so the timeline reads as one continuous conversation.
    case 'user.message':
      return {
        ...base,
        kind: 'user_message',
        prompt: String(p.prompt ?? ''),
        by: p.by ? String(p.by) : '',
      };

    // F8b: a forwarded permission request (approval-mode session). Options are
    // narrowed defensively — an entry without an option_id could never be
    // decided, so it is dropped rather than rendered as a dead button.
    case 'agent.permission_request': {
      const rawOptions = Array.isArray(p.options) ? (p.options as unknown[]) : [];
      const options = rawOptions.flatMap((o) => {
        if (o == null || typeof o !== 'object') return [];
        const m = o as Record<string, unknown>;
        const optionId = m.option_id ? String(m.option_id) : '';
        if (!optionId) return [];
        return [
          {
            optionId,
            name: m.name ? String(m.name) : optionId,
            kind: m.kind ? String(m.kind) : '',
          },
        ];
      });
      return {
        ...base,
        kind: 'permission_request',
        requestId: String(p.request_id ?? ''),
        toolCallId: p.tool_call_id ? String(p.tool_call_id) : undefined,
        title: String(p.title ?? 'Permission required'),
        options,
      };
    }

    // F8b: the request's final outcome (user decision or timeout-deny).
    case 'agent.permission_resolved':
      return {
        ...base,
        kind: 'permission_resolved',
        requestId: String(p.request_id ?? ''),
        optionId: String(p.option_id ?? ''),
        resolution: String(p.resolution ?? ''),
      };

    // D22 session: the session was wound down (user Finish / idle timeout).
    case 'session.finish': {
      const reason = String(p.reason ?? '');
      return {
        ...base,
        kind: 'session_finish',
        reason,
        by: p.by ? String(p.by) : '',
        message:
          reason === 'idle_timeout'
            ? 'Session finished (idle timeout)'
            : 'Session finished',
      };
    }

    default:
      return { ...base, kind: 'unknown', type: ev.type, raw: pretty(p) };
  }
}

/**
 * F7 (timeline ordering): on a fast clone failure the red terminal `run.failure`
 * block and the terminal `run.status(failed)` frame can micro-order in either
 * sequence, so a human reading top-to-bottom could see "failed → running →
 * failed" and be unsure which row is the true end state. Events already render
 * in strict seq order (the reducer sorts + dedupes), but ordering alone doesn't
 * disambiguate the *end state*.
 *
 * This returns the seq of the run's terminal `run.status` event — the SINGLE
 * authoritative end-of-run marker — or undefined if the run has not ended. The
 * Timeline gives that one row a "final" treatment so the end state is
 * unambiguous regardless of how the failure/status frames interleaved. Because
 * it keys off status (not arrival), a duplicate or out-of-order status frame
 * never produces a second "final" row: only the highest-seq terminal status wins.
 */
export function terminalStatusSeq(events: RunViewEvent[]): number | undefined {
  let best: number | undefined;
  for (const ev of events) {
    if (
      ev.type === 'run.status' &&
      typeof ev.payload?.status === 'string' &&
      isTerminalStatus(ev.payload.status)
    ) {
      if (best === undefined || ev.seq > best) best = ev.seq;
    }
  }
  return best;
}
