import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountProfile } from '../api/types';
import { AccountProfileForm } from './AccountProfileForm';

vi.mock('../auth/AuthProvider', () => ({ useOptionalAuth: () => ({ me: { user: { id: 'account-1' } } }) }));
const initial: AccountProfile = { display_name: 'Jack', preferences: { default_model_id: '', permission_mode: 'approval', effort: 'medium', send_key: 'enter', language: 'en', theme: 'light' } };
function show(section: 'profile' | 'preferences', update = vi.fn(async (value: AccountProfile) => value)) {
  const client = { getAccountProfile: async () => initial, updateAccountProfile: update } as unknown as ApiClient;
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><ApiProvider client={client}><MemoryRouter><AccountProfileForm section={section} /></MemoryRouter></ApiProvider></QueryClientProvider>);
  return update;
}

describe('Account profile form persistence', () => {
  it('saves profile changes with existing preferences and reports server success', async () => {
    const update = show('profile');
    fireEvent.change(await screen.findByRole('textbox', { name: 'Display name' }), { target: { value: 'Jack Updated' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(update).toHaveBeenCalledWith({ ...initial, display_name: 'Jack Updated' }));
    expect((await screen.findByRole('status')).textContent).toContain('Saved');
  });
  it('persists task and keyboard defaults and surfaces a rejected save', async () => {
    const update = vi.fn(async () => { throw new Error('Model access was revoked'); });
    show('preferences', update);
    fireEvent.change(await screen.findByRole('combobox', { name: 'Default execution permission' }), { target: { value: 'auto' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Send messages with' }), { target: { value: 'mod_enter' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(update).toHaveBeenCalledWith({ ...initial, preferences: { ...initial.preferences, permission_mode: 'auto', send_key: 'mod_enter' } }));
    expect((await screen.findByRole('alert')).textContent).toContain('Model access was revoked');
    expect(screen.queryByText('Saved')).toBeNull();
    expect(screen.getByRole('link', { name: 'Models' }).getAttribute('href')).toBe('/account/settings?section=models');
  });
});
