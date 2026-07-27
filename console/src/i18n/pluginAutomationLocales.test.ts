import { describe, expect, it } from 'vitest';
import en from './locales/en';
import ja from './locales/ja';
import ko from './locales/ko';
import zhHans from './locales/zh-Hans';
import zhHant from './locales/zh-Hant';

function leaves(value: unknown, prefix = ''): Map<string, string> {
  const out = new Map<string, string>();
  if (typeof value === 'string') {
    out.set(prefix, value);
    return out;
  }
  if (!value || typeof value !== 'object') return out;
  for (const [key, child] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${key}` : key;
    for (const [leaf, text] of leaves(child, path)) out.set(leaf, text);
  }
  return out;
}

describe('Plugin and Automation locale completeness', () => {
  for (const section of ['plugins', 'automationEditor', 'projectAutomations', 'projectSettings'] as const) {
    it(`keeps ${section} keys complete in all supported locales`, () => {
      const expected = leaves(en[section]);
      for (const [locale, resource] of Object.entries({ 'zh-Hans': zhHans, 'zh-Hant': zhHant, ja, ko })) {
        const actual = leaves(resource[section]);
        expect([...actual.keys()].sort(), `${locale}.${section} key shape`).toEqual([...expected.keys()].sort());
        for (const [key, text] of actual) {
          expect(text.trim().length, `${locale}.${section}.${key} is empty`).toBeGreaterThan(0);
        }
      }
    });
  }

  it('keeps Cluster Provider connection keys complete in all supported locales', () => {
    const expected = leaves(en.cluster.connections);
    for (const [locale, resource] of Object.entries({ 'zh-Hans': zhHans, 'zh-Hant': zhHant, ja, ko })) {
      const actual = leaves(resource.cluster.connections);
      expect([...actual.keys()].sort(), `${locale}.cluster.connections key shape`).toEqual([...expected.keys()].sort());
      for (const [key, text] of actual) {
        expect(text.trim().length, `${locale}.cluster.connections.${key} is empty`).toBeGreaterThan(0);
      }
    }
  });
});
