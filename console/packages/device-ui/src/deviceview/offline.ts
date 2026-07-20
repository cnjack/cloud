/*
 * offline.ts — online-resolution rule for the device session view. Pure so the
 * precedence is unit-testable without rendering a page.
 */

/**
 * The SSE stream's `device.status` edge is the freshest signal; the polled
 * device list is the fallback; before either reports, assume online so the
 * composer isn't needlessly disabled on first paint.
 */
export function resolveOnline(
  streamOnline: boolean | undefined,
  listedOnline: boolean | undefined,
): boolean {
  return streamOnline ?? listedOnline ?? true;
}
