#!/usr/bin/env node
/*
 * cdp.mjs — minimal Chrome DevTools Protocol driver for the mobile app's
 * webview (Tauri debug builds expose webview_devtools_remote_<pid> on
 * Android; on iOS we drive the Simulator via Safari's inspector instead —
 * see report). Used by the M6 verification flow to fill forms and click
 * through pages deterministically (adb/simctl taps are too brittle).
 *
 *   node scripts/cdp.mjs <webSocketDebuggerUrl> <js-expression>
 *
 * Prints the JSON-serialized result of Runtime.evaluate (awaitPromise on).
 */
const [wsUrl, expr] = process.argv.slice(2);
if (!wsUrl || !expr) {
  console.error('usage: cdp.mjs <ws-url> <js-expression>');
  process.exit(2);
}

const ws = new WebSocket(wsUrl);
let id = 0;
const pending = new Map();

function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const msgId = ++id;
    pending.set(msgId, { resolve, reject });
    ws.send(JSON.stringify({ id: msgId, method, params }));
  });
}

const timeout = setTimeout(() => {
  console.error('timeout waiting for CDP');
  process.exit(1);
}, 30_000);

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    if (msg.error) reject(new Error(msg.error.message));
    else resolve(msg.result);
  }
};

ws.onopen = async () => {
  try {
    const result = await send('Runtime.evaluate', {
      expression: expr,
      awaitPromise: true,
      returnByValue: true,
    });
    clearTimeout(timeout);
    if (result.exceptionDetails) {
      console.error(JSON.stringify(result.exceptionDetails, null, 2));
      process.exit(1);
    }
    console.log(JSON.stringify(result.result?.value ?? null));
    ws.close();
    process.exit(0);
  } catch (err) {
    clearTimeout(timeout);
    console.error(String(err));
    process.exit(1);
  }
};

ws.onerror = (err) => {
  clearTimeout(timeout);
  console.error('ws error', err.message ?? '');
  process.exit(1);
};
