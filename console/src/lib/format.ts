/* format.ts — small presentation helpers. Relative-time / duration words resolve
 * through the shared i18n instance so they follow the active UI locale; callers
 * stay unchanged (components re-render on language change via useTranslation). */
import { i18n } from '../i18n';

export function formatTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/** Relative "3m ago" style, coarse. */
export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return '';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '';
  const secs = Math.round((Date.now() - then) / 1000);
  if (secs < 45) return i18n.t('format.justNow');
  const mins = Math.round(secs / 60);
  if (mins < 60) return i18n.t('format.minutesAgo', { n: mins });
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return i18n.t('format.hoursAgo', { n: hrs });
  const days = Math.round(hrs / 24);
  return i18n.t('format.daysAgo', { n: days });
}

/** Human duration between two ISO timestamps (or start→now). */
export function formatDuration(
  start: string | null | undefined,
  end: string | null | undefined,
): string {
  if (!start) return '';
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  if (Number.isNaN(s) || Number.isNaN(e) || e < s) return '';
  const secs = Math.round((e - s) / 1000);
  if (secs < 60) return i18n.t('format.durationSeconds', { n: secs });
  const mins = Math.floor(secs / 60);
  const rem = secs % 60;
  if (mins < 60) return rem ? i18n.t('format.durationMinutesSeconds', { m: mins, s: rem }) : i18n.t('format.durationMinutes', { m: mins });
  const hrs = Math.floor(mins / 60);
  return i18n.t('format.durationHoursMinutes', { h: hrs, m: mins % 60 });
}

/**
 * Whole days from now until `iso` (rounded up; negative once past). Returns null
 * for an empty/invalid timestamp — used to render a token-expiry badge ("expires
 * in N days" / "expired") only when the expiry is actually known (D28).
 */
export function daysUntil(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return null;
  return Math.ceil((t - Date.now()) / 86_400_000);
}

/** Short run id for display, e.g. "run_0001ab3f" -> "0001ab3f". */
export function shortId(id: string): string {
  const parts = id.split('_');
  return parts.length > 1 ? parts[parts.length - 1]! : id;
}

/** One-line prompt summary for lists. */
export function summarize(text: string, max = 80): string {
  const oneLine = text.replace(/\s+/g, ' ').trim();
  return oneLine.length > max ? `${oneLine.slice(0, max - 1)}…` : oneLine;
}
