/*
 * channels.test.ts — source → i18n key normalization for the channel badge:
 * known channels translate, unknown/empty degrade (raw fallback / no badge).
 */
import { describe, expect, it } from 'vitest';
import { KNOWN_MESSAGE_SOURCES, channelLabelKey } from './channels';

describe('channelLabelKey', () => {
  it('maps every known channel to its device.session.source.* key', () => {
    for (const source of KNOWN_MESSAGE_SOURCES) {
      expect(channelLabelKey(source)).toBe(`device.session.source.${source}`);
    }
  });

  it('trims and case-folds before matching', () => {
    expect(channelLabelKey(' Console ')).toBe('device.session.source.console');
    expect(channelLabelKey('WECHAT')).toBe('device.session.source.wechat');
  });

  it('returns undefined for unknown channels (raw fallback) and empty input (no badge)', () => {
    expect(channelLabelKey('pager')).toBeUndefined();
    expect(channelLabelKey('')).toBeUndefined();
    expect(channelLabelKey('   ')).toBeUndefined();
    expect(channelLabelKey(undefined)).toBeUndefined();
  });
});
