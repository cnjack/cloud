import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import en from '../i18n/locales/en';
import zhHans from '../i18n/locales/zh-Hans';
import zhHant from '../i18n/locales/zh-Hant';
import ja from '../i18n/locales/ja';
import ko from '../i18n/locales/ko';
import { DeviceGuidePage } from './DeviceGuidePage';

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/devices/guide']}>
      <DeviceGuidePage />
    </MemoryRouter>,
  );
}

/** Flattens a nested locale subtree to its leaf key paths ("commands.rows.login"). */
function leafPaths(node: unknown, prefix = ''): string[] {
  if (typeof node === 'string') return [prefix];
  if (node && typeof node === 'object') {
    return Object.entries(node as Record<string, unknown>).flatMap(([key, value]) =>
      leafPaths(value, prefix ? `${prefix}.${key}` : key),
    );
  }
  return [prefix];
}

function lookup(node: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((acc, part) => {
    if (acc && typeof acc === 'object') return (acc as Record<string, unknown>)[part];
    return undefined;
  }, node);
}

describe('DeviceGuidePage (M7 user guide)', () => {
  it('renders every section, the command table and the screenshots', () => {
    renderPage();
    expect(screen.getByTestId('device-guide')).toBeTruthy();
    // Six content sections from the M7 outline.
    for (const title of [
      'What is a "device"?',
      'Quick start',
      'Using a device remotely',
      'Pairing & end-to-end encryption',
      'Keys & recovery',
      'Command reference',
    ]) {
      expect(screen.getByRole('heading', { name: title })).toBeTruthy();
    }
    // Command table covers the verified CLI surface.
    for (const cmd of [
      'jcode login',
      'jcode login --status',
      'jcode logout',
      'jcode cloud status',
      'jcode cloud pairings',
      'jcode cloud approve <pairing_id>',
      'jcode cloud deny <pairing_id>',
      'jcode cloud key show-phrase',
      'jcode cloud key recover',
      'jcode cloud rotate-key',
    ]) {
      expect(screen.getAllByText(cmd).length).toBeGreaterThan(0);
    }
    // Screenshots render with localized captions.
    expect(screen.getByAltText('Device list in the cloud console')).toBeTruthy();
    expect(screen.getByAltText('An offline device still shows its history')).toBeTruthy();
    // Back link returns to the device list.
    expect(screen.getByRole('link', { name: /Back to devices/i }).getAttribute('href')).toBe('/devices');
  });

  it('keeps device.guide.* keys complete and non-empty in all five locales', () => {
    const enGuide = (en as Record<string, unknown>).device as Record<string, unknown>;
    const paths = leafPaths(enGuide.guide).map((p) => `guide.${p}`);
    expect(paths.length).toBeGreaterThan(30);
    for (const locale of [zhHans, zhHant, ja, ko]) {
      const device = (locale as Record<string, unknown>).device;
      for (const path of paths) {
        const value = lookup(device, path);
        expect(typeof value, `${path} missing in a locale`).toBe('string');
        expect((value as string).length, `${path} empty in a locale`).toBeGreaterThan(0);
      }
    }
  });
});
