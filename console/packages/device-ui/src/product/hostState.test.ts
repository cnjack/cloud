/*
 * hostState.test.ts — capabilities → ProductComposerHost projections and
 * send-extras assembly (M14). Pure mapping rules, no React.
 */
import { describe, expect, it } from 'vitest';
import type { DeviceCapabilities } from '../api/devices';
import {
  buildProviders,
  buildSendExtras,
  buildSlashCommands,
  buildWorkspaceTasks,
  CLOUD_ALLOWED_MODES,
  initialDeviceComposerState,
} from './hostState';

const CAPS: DeviceCapabilities = {
  projects: [
    { path: '/home/jack/a', name: 'a' },
    { path: '/home/jack/b', name: 'b' },
  ],
  models: [
    { provider: 'anthropic', id: 'claude-opus-4', label: 'Claude Opus 4' },
    { provider: 'anthropic', id: 'claude-sonnet-4', label: 'Claude Sonnet 4' },
    { provider: 'openai', id: 'gpt-5', label: '' },
  ],
  efforts: ['low', 'medium', 'high'],
  slash_commands: [
    { slash: '/review', description: 'Review code', type: 'skill' },
    { slash: '/deploy', description: 'Deploy flow', type: 'flow' },
    { slash: '/mystery', description: 'unknown kind', type: 'weird' },
  ],
};

describe('buildProviders', () => {
  it('groups the flat model list by provider, preserving order', () => {
    const providers = buildProviders(CAPS);
    expect(providers.map((p) => p.id)).toEqual(['anthropic', 'openai']);
    expect(providers[0]!.models.map((m) => m.id)).toEqual(['claude-opus-4', 'claude-sonnet-4']);
    // label falls back to the raw id when empty.
    expect(providers[1]!.models[0]!.name).toBe('gpt-5');
  });

  it('derives reasoning options from the device-wide effort ladder', () => {
    const providers = buildProviders(CAPS);
    expect(providers[0]!.models[0]!.reasoning).toBe(true);
    expect(providers[0]!.models[0]!.reasoning_options).toEqual([
      { type: 'effort', values: ['low', 'medium', 'high'] },
    ]);
    // No efforts → no reasoning metadata → the effort control hides.
    const bare = buildProviders({ models: CAPS.models });
    expect(bare[0]!.models[0]!.reasoning).toBe(false);
    expect(bare[0]!.models[0]!.reasoning_options).toBeUndefined();
  });

  it('applies the local disabled set to enabled flags', () => {
    const providers = buildProviders(CAPS, new Set(['anthropic/claude-opus-4']));
    const [opus, sonnet] = providers[0]!.models;
    expect(opus!.enabled).toBe(false);
    expect(sonnet!.enabled).toBe(true);
  });

  it('degrades to an empty catalog without capabilities', () => {
    expect(buildProviders(null)).toEqual([]);
    expect(buildProviders(undefined)).toEqual([]);
    expect(buildProviders({})).toEqual([]);
  });
});

describe('buildSlashCommands', () => {
  it('maps slash_commands and coerces unknown types to skill', () => {
    expect(buildSlashCommands(CAPS)).toEqual([
      { slash: '/review', description: 'Review code', type: 'skill' },
      { slash: '/deploy', description: 'Deploy flow', type: 'flow' },
      { slash: '/mystery', description: 'unknown kind', type: 'skill' },
    ]);
  });

  it('degrades to an empty menu on pre-M14 connectors', () => {
    expect(buildSlashCommands(null)).toEqual([]);
    expect(buildSlashCommands({})).toEqual([]);
  });
});

describe('buildWorkspaceTasks', () => {
  it('projects capabilities.projects onto picker tasks', () => {
    expect(buildWorkspaceTasks(CAPS)).toEqual([
      { uuid: '/home/jack/a', project: '/home/jack/a' },
      { uuid: '/home/jack/b', project: '/home/jack/b' },
    ]);
    expect(buildWorkspaceTasks(null)).toEqual([]);
  });
});

describe('buildSendExtras', () => {
  it('includes model/effort/project only while advertised', () => {
    const state = {
      ...initialDeviceComposerState(),
      model: { provider: 'anthropic', model: 'claude-opus-4' },
      modelTouched: true,
      effortOverrides: { 'anthropic/claude-opus-4': 'high' },
      projectPath: '/home/jack/a',
    };
    expect(buildSendExtras(state, CAPS)).toEqual({
      model: { provider: 'anthropic', id: 'claude-opus-4' },
      effort: 'high',
      project_path: '/home/jack/a',
    });
  });

  it('drops an unadvertised model but keeps a device-browsed project path', () => {
    const state = {
      ...initialDeviceComposerState(),
      model: { provider: 'gone', model: 'x' },
      effortOverrides: { 'gone/x': 'high' },
      projectPath: '/elsewhere',
    };
    expect(buildSendExtras(state, CAPS)).toEqual({ project_path: '/elsewhere' });
  });

  it('passes images through untouched', () => {
    const images = [{ data: 'aGk=', media_type: 'image/png', name: 'hi.png' }];
    expect(buildSendExtras(initialDeviceComposerState(), CAPS, images)).toEqual({ images });
  });

  it('returns undefined for a plain text send (payload stays byte-identical)', () => {
    expect(buildSendExtras(initialDeviceComposerState(), CAPS)).toBeUndefined();
    expect(buildSendExtras(initialDeviceComposerState(), null)).toBeUndefined();
  });
});

describe('CLOUD_ALLOWED_MODES (M20 mode ceiling)', () => {
  it('offers approval/plan/auto and never full_access', () => {
    expect(CLOUD_ALLOWED_MODES).toEqual(['approval', 'plan', 'auto']);
    expect(CLOUD_ALLOWED_MODES).not.toContain('full_access');
  });
});
