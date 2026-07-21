/*
 * deviceBadges.ts — label/tooltip resolution for the device platform and
 * E2EE badges shown on the device list and device detail pages. Pure helpers
 * so both pages (and tests) share one definition of "known platform" and the
 * key-generation tooltip rule.
 */
import type { TFunction } from 'i18next';
import type { Device } from '@jcloud/device-ui';

/**
 * Translated platform badge label, or '' when the platform is absent or not
 * one of the known kinds ("desktop" | "cli") — unknown values hide the badge
 * instead of leaking raw strings into the UI.
 */
export function platformBadgeLabel(platform: string | undefined, t: TFunction): string {
  switch (platform) {
    case 'desktop': return t('device.list.platformDesktop');
    case 'cli': return t('device.list.platformCli');
    default: return '';
  }
}

/**
 * Tooltip for the E2EE lock badge. '' when the device has no registered
 * pubkey (badge hidden); otherwise explains end-to-end encryption, noting the
 * key generation once it has been rotated past generation 1.
 */
export function e2eeBadgeTooltip(device: Pick<Device, 'pubkey' | 'key_gen'>, t: TFunction): string {
  if (!device.pubkey) return '';
  const gen = device.key_gen ?? 0;
  return gen > 1 ? t('device.list.e2eeWithKeyGen', { gen }) : t('device.list.e2ee');
}
