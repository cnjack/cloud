import { describe, expect, it } from 'vitest';
import en from './locales/en';
import ja from './locales/ja';
import ko from './locales/ko';
import zhHans from './locales/zh-Hans';
import zhHant from './locales/zh-Hant';

const locales = { 'zh-Hans': zhHans, 'zh-Hant': zhHant, ja, ko } as const;
const sections = ['accountHeader', 'accountSettings', 'repositories'] as const;

describe('Account and Repository workspace locale completeness', () => {
  for (const section of sections) {
    it(`keeps ${section} complete in every supported locale`, () => {
      const expected = Object.keys(en[section]).sort();
      for (const [locale, resource] of Object.entries(locales)) {
        const actual = resource[section] as Record<string, string>;
        expect(Object.keys(actual).sort(), `${locale}.${section} key shape`).toEqual(expected);
        for (const [key, value] of Object.entries(actual)) {
          expect(value.trim().length, `${locale}.${section}.${key} is empty`).toBeGreaterThan(0);
        }
      }
    });
  }
});
