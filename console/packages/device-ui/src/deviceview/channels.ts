/*
 * channels.ts — normalize a user_message `source` (the channel a prompt came
 * in through) to a stable i18n label key. Known channels get translated
 * badges; unknown non-empty values render raw as a fallback; empty renders
 * no badge at all (callers check the source before asking for a key).
 */

/** Channel sources jcode is known to stamp on user_message events. */
export const KNOWN_MESSAGE_SOURCES = ['console', 'mobile', 'desktop', 'wechat', 'local'] as const;

/**
 * Map a source string to its `device.session.source.*` i18n key, or undefined
 * when the source is empty or not a known channel (caller falls back to the
 * raw value / no badge). Matching is case-insensitive and trims whitespace.
 */
export function channelLabelKey(source: string | undefined): string | undefined {
  const normalized = (source ?? '').trim().toLowerCase();
  return (KNOWN_MESSAGE_SOURCES as readonly string[]).includes(normalized)
    ? `device.session.source.${normalized}`
    : undefined;
}
