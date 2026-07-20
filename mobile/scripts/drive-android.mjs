#!/usr/bin/env node
/*
 * drive-android.mjs — M6 Android verification flow. Drives the app installed
 * on the booted emulator through login → devices → welcome(+pairing) →
 * session streaming, capturing adb screencaps into reports/assets/M6/.
 *
 * Prereqs: emulator booted with the debug apk installed and the app in the
 * foreground; the rig (scripts/rig.sh up) running on the host.
 */
import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runFlow, sleep } from './drive.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const ADB = join(homedir(), 'Library/Android/sdk/platform-tools/adb');
const ASSETS = '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M6';
const PKG = 'net.j_code.mobile';
const HOST_HTTP_PORT = 9223;

function sh(cmd, args) {
  return execFileSync(cmd, args, { encoding: 'utf8' }).trim();
}

function evaluate(expr) {
  const pid = sh(ADB, ['shell', 'pidof', PKG]);
  execFileSync(ADB, ['forward', `tcp:${HOST_HTTP_PORT}`, `localabstract:webview_devtools_remote_${pid}`], { stdio: 'ignore' });
  const targets = JSON.parse(execSync(`curl -sS http://localhost:${HOST_HTTP_PORT}/json`, { encoding: 'utf8' }));
  const page = targets.find((t) => t.type === 'page');
  if (!page) throw new Error('no page target');
  const res = spawnSync('node', [join(HERE, 'cdp.mjs'), page.webSocketDebuggerUrl, expr], {
    encoding: 'utf8',
    timeout: 40_000,
  });
  if (res.status !== 0) throw new Error(`cdp eval failed: ${res.stderr || res.stdout}`);
  return JSON.parse(res.stdout.trim());
}

async function shot(name) {
  const out = join(ASSETS, `android-${name}.png`);
  execSync(`${ADB} exec-out screencap -p > "${out}"`);
  console.log('screenshot:', out);
}

async function approvePairing(pid) {
  execFileSync('/Users/jack/workpath/jjj/jcode/jcode', ['cloud', 'approve', pid], {
    env: { ...process.env, HOME: '/tmp/jmobile-rig' },
    encoding: 'utf8',
  });
}

// Make sure the app is foregrounded, then give the webview a moment.
sh(ADB, ['shell', 'am', 'start', '-n', `${PKG}/.MainActivity`]);
await sleep(4000);

await runFlow({
  evaluate,
  shot,
  approvePairing,
  loginUrl: 'http://10.0.2.2:18080',
  token: 'jmobile-dev-session-token',
});
