import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../../api/ApiProvider';
import type { ApiClient } from '../../api/client';
import type { ModelProvider } from '../../api/types';
import { ModelAuthorizationDialog } from './ModelAuthorizationDialog';

const pending = { state: 'pending', user_code: 'QA-CODE', verification_uri: 'https://auth.openai.com/codex/device', interval_seconds: 1 };
const provider = { id: 'owned-provider', name: 'Personal ChatGPT' } as ModelProvider;
function show(client: Partial<ApiClient>, existing: ModelProvider | null = null) {
 const onClose = vi.fn();
 const view = render(<QueryClientProvider client={new QueryClient()}><ApiProvider client={client as ApiClient}><ModelAuthorizationDialog open provider={existing} onClose={onClose} /></ApiProvider></QueryClientProvider>);
 return { onClose, ...view };
}
afterEach(() => vi.useRealTimers());
describe('Personal model authorization', () => {
 it('retains a newly created provider when start fails and retries without duplicating it', async () => {
  const create = vi.fn().mockResolvedValue(provider);
  const authorize = vi.fn().mockRejectedValueOnce(new Error('Authorization service unavailable')).mockResolvedValue(pending);
  show({ createChatGPTProvider: create, modelAuthorization: authorize });
  fireEvent.click(screen.getByRole('button', { name: 'Connect ChatGPT' }));
  expect((await screen.findByRole('alert')).textContent).toContain('Authorization service unavailable');
  fireEvent.click(screen.getByRole('button', { name: 'Reauthorize' }));
  expect(await screen.findByText('QA-CODE')).toBeTruthy();
  expect(create).toHaveBeenCalledTimes(1);
  expect(authorize).toHaveBeenLastCalledWith(provider.id, 'start');
  expect(screen.getByRole('link', { name: /Open ChatGPT authorization/ }).getAttribute('href')).toBe(pending.verification_uri);
 });
 it('resumes persisted authorization, polls and shows ready only after upstream completion', async () => {
  const authorize = vi.fn().mockResolvedValueOnce(pending).mockResolvedValueOnce({ state: 'ready', login: 'fixture@example.test' });
  show({ modelAuthorization: authorize }, provider);
  expect(await screen.findByText('QA-CODE')).toBeTruthy();
  expect(screen.queryByText('Connected')).toBeNull();
  await waitFor(() => expect(screen.getByText('Connected')).toBeTruthy(), { timeout: 2000 });
  expect(authorize).toHaveBeenLastCalledWith(provider.id, 'poll');
  expect(screen.getByText('fixture@example.test')).toBeTruthy();
 });
 it('cancels a pending flow through the owner endpoint before closing', async () => {
  const cancel = vi.fn().mockResolvedValue(undefined);
  const { onClose } = show({ modelAuthorization: vi.fn().mockResolvedValue(pending), cancelModelAuthorization: cancel }, provider);
  fireEvent.click(await screen.findByRole('button', { name: 'Cancel authorization' }));
  await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  expect(cancel).toHaveBeenCalledWith(provider.id);
 });
 it('stops polling when unmounted and never renders an unexpected provider URL', async () => {
  const authorize = vi.fn().mockResolvedValue({ ...pending, verification_uri: 'https://untrusted.example' });
  const { unmount } = show({ modelAuthorization: authorize }, provider);
  await screen.findByText('QA-CODE');
  expect(screen.queryByRole('link')).toBeNull();
  vi.useFakeTimers(); unmount(); await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(authorize).toHaveBeenCalledTimes(1);
 });
});
