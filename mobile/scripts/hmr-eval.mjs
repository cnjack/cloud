#!/usr/bin/env node
/*
 * hmr-eval.mjs — evaluate a JS expression inside the vite-dev-served webview
 * via the m6-drive-bridge HMR channel (see vite.config.ts + src/devDrive.ts).
 *
 *   node scripts/hmr-eval.mjs <js-expression>
 *
 * Prints the JSON result. Used by drive-ios.mjs (no external tooling needed —
 * the iOS Simulator's WKWebView has no headless inspector).
 */
const [expr] = process.argv.slice(2);
if (!expr) {
  console.error('usage: hmr-eval.mjs <js-expression>');
  process.exit(2);
}

const WS_URL = process.env.M6_HMR_WS ?? 'ws://localhost:5174/';
const id = Math.floor(Math.random() * 1e9);

const timeout = setTimeout(() => {
  console.error('timeout waiting for m6:result (is the app open and dev server up?)');
  process.exit(1);
}, 30_000);

const ws = new WebSocket(WS_URL, 'vite-hmr');

ws.onmessage = (event) => {
  let msg;
  try {
    msg = JSON.parse(String(event.data));
  } catch {
    return;
  }
  if (msg.type !== 'custom' || msg.event !== 'm6:result') return;
  const { id: rid, value, error } = msg.data ?? {};
  if (rid !== id) return;
  clearTimeout(timeout);
  if (error) {
    console.error(error);
    process.exit(1);
  }
  console.log(JSON.stringify(value ?? null));
  process.exit(0);
};

ws.onopen = () => {
  ws.send(JSON.stringify({ type: 'custom', event: 'm6:eval', data: { id, expr } }));
};

ws.onerror = () => {
  clearTimeout(timeout);
  console.error('cannot connect to vite HMR ws at', WS_URL);
  process.exit(1);
};
