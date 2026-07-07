/*
 * eventModel.ts — narrows the loose RunEvent payload into typed view models the
 * Timeline renders. Keeps all the "what field is the tool name really in"
 * defensiveness in one place so components stay clean.
 */
import type { RunEvent } from './types';
import { isTerminal, type RunStatus } from './types';

export interface TextItem {
  seq: number;
  ts: string;
  kind: 'text';
  text: string;
}

export interface ToolCallItem {
  seq: number;
  ts: string;
  kind: 'tool_call';
  tool: string;
  callId?: string;
  args: string; // pretty-printed
}

export interface ToolResultItem {
  seq: number;
  ts: string;
  kind: 'tool_result';
  tool?: string;
  callId?: string;
  output: string;
  isError: boolean;
}

export interface StatusItem {
  seq: number;
  ts: string;
  kind: 'status';
  status: string;
}

export interface FailureItem {
  seq: number;
  ts: string;
  kind: 'failure';
  reason?: string;
  message: string;
}

export interface ArtifactItem {
  seq: number;
  ts: string;
  kind: 'artifact';
  artifact: string;
}

export interface GitItem {
  seq: number;
  ts: string;
  kind: 'git';
  branch: string;
  commitSha?: string;
}

export interface UnknownItem {
  seq: number;
  ts: string;
  kind: 'unknown';
  type: string;
  raw: string;
}

export type TimelineItem =
  | TextItem
  | ToolCallItem
  | ToolResultItem
  | StatusItem
  | FailureItem
  | ArtifactItem
  | GitItem
  | UnknownItem;

function pretty(value: unknown): string {
  if (value == null) return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function toTimelineItem(ev: RunEvent): TimelineItem {
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
export function terminalStatusSeq(events: RunEvent[]): number | undefined {
  let best: number | undefined;
  for (const ev of events) {
    if (
      ev.type === 'run.status' &&
      typeof ev.payload?.status === 'string' &&
      isTerminal(ev.payload.status as RunStatus)
    ) {
      if (best === undefined || ev.seq > best) best = ev.seq;
    }
  }
  return best;
}
