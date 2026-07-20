#!/usr/bin/env node
/*
 * drive-ios.mjs — M6 iOS verification flow. Drives the app running in the
 * booted iPhone simulator (via `pnpm tauri ios dev`, vite dev on :5174)
 * through login → devices → welcome(+pairing) → session streaming, capturing
 * simctl screenshots into reports/assets/M6/.
 *
 * The webview is driven over the vite HMR channel (scripts/hmr-eval.mjs +
 * src/devDrive.ts) because the Simulator's WKWebView has no headless
 * inspector. Prereq: scripts/rig.sh up; `pnpm tauri ios dev "iPhone 17 Pro"`.
 */
import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runFlow, sleep } from './drive.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const ASSETS = '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M6';

function evaluate(expr) {
  const res = spawnSync('node', [join(HERE, 'hmr-eval.mjs'), expr], { encoding: 'utf8', timeout: 40_000 });
  if (res.status !== 0) throw new Error(`hmr eval failed: ${res.stderr || res.stdout}`);
  return JSON.parse(res.stdout.trim());
}

async function shot(name) {
  const out = join(ASSETS, `ios-${name}.png`);
  execSync(`xcrun simctl io booted screenshot "${out}"`);
  console.log('screenshot:', out);
}

async function approvePairing(pid) {
  execFileSync('/Users/jack/workpath/jjj/jcode/jcode', ['cloud', 'approve', pid], {
    env: { ...process.env, HOME: '/tmp/jmobile-rig' },
    encoding: 'utf8',
  });
}

// Sanity: the dev server and the app's eval hook are reachable.
const probe = await evaluate('({hash: location.hash, tauri: "__TAURI_INTERNALS__" in window})');
console.log('webview probe:', probe);

await runFlow({
  evaluate,
  shot,
  approvePairing,
  loginUrl: 'http://127.0.0.1:18080',
  token: 'jmobile-dev-session-token',
});
await sleep(500);
