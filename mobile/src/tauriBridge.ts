/*
 * tauriBridge.ts — composition-root shims for the Tauri webview.
 *
 * The webview enforces browser CORS, so cross-origin calls to the
 * orchestrator (fetch/XHR/EventSource from origin http://tauri.localhost)
 * would all fail. Inside Tauri we therefore patch two globals BEFORE any app
 * code runs, routing over the app's own native commands (src-tauri/src/lib.rs):
 *
 *   window.fetch       → the `device_fetch` command (reqwest — no CORS, no
 *                        ATS/cleartext webview policy)
 *   window.EventSource → TauriEventSource below (the device_stream_* commands
 *                        pump parsed SSE frames over an ipc Channel)
 *
 * Outside Tauri (plain `vite dev` in a desktop browser) nothing is patched;
 * the app then works against a same-origin dev proxy or a CORS-permissive
 * target only.
 */
import { invoke, Channel } from '@tauri-apps/api/core';

interface FetchResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
}

/** fetch(2) over the native `device_fetch` command. */
async function tauriFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  const headers: Record<string, string> = {};
  new Headers(init?.headers).forEach((value, key) => {
    headers[key] = value;
  });
  const body = typeof init?.body === 'string' ? init.body : undefined;
  const res = await invoke<FetchResponse>('device_fetch', {
    req: { method: init?.method ?? 'GET', url, headers, body },
  });
  // Response forbids a body on null-body statuses.
  const nullBody = res.status === 204 || res.status === 304;
  return new Response(nullBody ? null : res.body, { status: res.status, headers: res.headers });
}

interface SseMsg {
  kind: 'open' | 'frame' | 'error';
  event?: string;
  data?: string;
  message?: string;
}

type FrameListener = (e: MessageEvent) => void;

/** Minimal EventSource surface used by @jcloud/device-ui's streamDevice. */
export class TauriEventSource {
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  readyState = 0;
  private listeners = new Map<string, Set<FrameListener>>();
  private id: string | null = null;

  constructor(url: string) {
    const channel = new Channel<SseMsg>();
    channel.onmessage = (msg) => {
      if (msg.kind === 'open') {
        this.readyState = 1;
        this.onopen?.(new Event('open'));
      } else if (msg.kind === 'frame' && msg.event) {
        const frame = new MessageEvent(msg.event, { data: msg.data ?? '' });
        this.listeners.get(msg.event)?.forEach((cb) => cb(frame));
      } else if (msg.kind === 'error') {
        this.readyState = 2;
        this.onerror?.(new Event('error'));
      }
    };
    // The device stream URL already carries ?access_token= (see devices.ts),
    // so the Rust side needs nothing but the URL.
    invoke<string>('device_stream_start', { url, onMsg: channel })
      .then((id) => {
        this.id = id;
      })
      .catch(() => {
        this.readyState = 2;
        this.onerror?.(new Event('error'));
      });
  }

  addEventListener(type: string, cb: FrameListener): void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(cb);
  }

  removeEventListener(type: string, cb: FrameListener): void {
    this.listeners.get(type)?.delete(cb);
  }

  close(): void {
    this.readyState = 2;
    if (this.id) void invoke('device_stream_close', { id: this.id });
  }
}

export function patchTauriGlobals(): void {
  if (!('__TAURI_INTERNALS__' in window)) return;
  window.fetch = tauriFetch as unknown as typeof fetch;
  window.EventSource = TauriEventSource as unknown as typeof EventSource;
}

// Module side effect: main.tsx imports this module FIRST, so the patch is in
// place before ./i18n / ./App (and anything they pull in) evaluate.
patchTauriGlobals();
