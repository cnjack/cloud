/*
 * i18n/index.ts — the copy @jcloud/device-ui components render, as i18next
 * resource bundles for the console's five locales.
 *
 * The components call plain useTranslation(), so a host that already ships
 * these keys (the console) needs to do NOTHING — its own bundles win. A host
 * without them (the mobile app) either passes deviceUiResources to i18next
 * init, or calls registerDeviceUiResources(i18n) after init (addResourceBundle
 * without overwrite, so host keys always take precedence).
 *
 * The bundles are generated from the console locale files — run
 * console/scripts/extract-device-locales.mjs after editing device.* copy.
 */
import type { i18n as I18n } from 'i18next';
import en from './locales/en';
import zhHans from './locales/zh-Hans';
import zhHant from './locales/zh-Hant';
import ja from './locales/ja';
import ko from './locales/ko';

export const DEVICE_UI_LOCALES = ['en', 'zh-Hans', 'zh-Hant', 'ja', 'ko'] as const;

export const deviceUiResources = {
  en: { translation: en },
  'zh-Hans': { translation: zhHans },
  'zh-Hant': { translation: zhHant },
  ja: { translation: ja },
  ko: { translation: ko },
};

/**
 * Merge the device-ui bundles into an already-initialised i18next instance.
 * `overwrite: false` — the host's own copy of a key always wins.
 */
export function registerDeviceUiResources(i18n: I18n): void {
  for (const [lng, bundle] of Object.entries(deviceUiResources)) {
    i18n.addResourceBundle(lng, 'translation', bundle.translation, true, false);
  }
}

/**
 * The interpolation config the bundles are written for: `{name}` placeholders
 * (jcode convention), NOT i18next's default `{{name}}`. A host initialising
 * its own i18next must use the same prefix/suffix or placeholders render raw.
 */
export const deviceUiInterpolation = {
  escapeValue: false,
  prefix: '{',
  suffix: '}',
} as const;
