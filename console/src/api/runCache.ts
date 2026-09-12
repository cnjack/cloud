import { isTerminal, type Run, type RunStatus } from './types';

/**
 * A run ID has an immutable terminal outcome. Multiple UI data sources can
 * arrive out of order (list polling, detail GET, backlog replay, live SSE), so
 * an older non-terminal snapshot must never replace a terminal one.
 */
export function reconcileRunSnapshot(
  current: Run | undefined,
  incoming: Run,
): Run {
  if (
    current?.id === incoming.id
    && isTerminal(current.status)
    && !isTerminal(incoming.status)
  ) {
    return current;
  }
  return incoming;
}

export function reconcileRunStatus(
  current: Run | undefined,
  incomingStatus: RunStatus,
): Run | undefined {
  if (!current) return current;
  if (isTerminal(current.status) && !isTerminal(incomingStatus)) return current;
  return current.status === incomingStatus
    ? current
    : { ...current, status: incomingStatus };
}

/** Mutation responses contain the stored Run, while GET also supplies projections. */
export function reconcileRunMutation(current: Run | undefined, incoming: Run): Run {
  if (!current || current.id !== incoming.id) return incoming;
  return reconcileRunSnapshot(current, {
    ...incoming,
    provenance: incoming.provenance ?? current.provenance,
    usage_summary: incoming.usage_summary ?? current.usage_summary,
    scm_grant: incoming.scm_grant ?? current.scm_grant,
  });
}
