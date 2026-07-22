import { invoke } from '@tauri-apps/api/core';

export const isNativeRuntime = (): boolean =>
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

export async function secureGet(key: string): Promise<string | null> {
  if (!isNativeRuntime()) return null;
  return invoke<string | null>('secure_get', { key });
}

export async function secureSet(key: string, value: string): Promise<void> {
  if (!isNativeRuntime()) return;
  await invoke('secure_set', { key, value });
}

export async function secureDelete(key: string): Promise<void> {
  if (!isNativeRuntime()) return;
  await invoke('secure_delete', { key });
}

