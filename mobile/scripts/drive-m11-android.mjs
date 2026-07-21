#!/usr/bin/env node
/*
 * drive-m11-android.mjs — M11 Android verification: OAuth "Sign in with
 * cloud" (system browser → gitea → jcode://auth deep link back) and
 * scan-to-pair via the manual-paste fallback (emulator has no camera).
 *
 * Prereqs: emulator-5554 with the M11 debug apk installed, the M11 local
 * orchestrator on host :8901 (AUTH_GITEA_EXTERNAL_URL=http://10.0.2.2:3300)
 * and gitea on :3300 with the jcode-mobile-m11 OAuth app.
 *
 *   node scripts/drive-m11-android.mjs <step>
 *
 * Steps: oauth-start | browser-login | browser-authorize | after-oauth |
 *        scan-pair | shot <name>
 */
import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ADB = join(homedir(), 'Library/Android/sdk/platform-tools/adb');
const SERIAL = process.env.M11_SERIAL ?? 'emulator-5554';
const ASSETS = '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M11';
const PKG = 'net.j_code.mobile';
const HOST_HTTP_PORT = 9223;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function adb(args) {
  return execFileSync(ADB, ['-s', SERIAL, ...args], { encoding: 'utf8' }).trim();
}

function evaluate(expr) {
  const pid = adb(['shell', 'pidof', PKG]);
  execFileSync(ADB, ['-s', SERIAL, 'forward', `tcp:${HOST_HTTP_PORT}`, `localabstract:webview_devtools_remote_${pid}`], { stdio: 'ignore' });
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

function shot(name) {
  const out = join(ASSETS, `${name}.png`);
  execSync(`${ADB} -s ${SERIAL} exec-out screencap -p > "${out}"`);
  console.log('screenshot:', out);
}

// uiautomator helpers: dump the UI tree, find a node's bounds by predicate.
function uiDump() {
  adb(['shell', 'uiautomator', 'dump', '/sdcard/ui.xml']);
  return adb(['shell', 'cat', '/sdcard/ui.xml']);
}

function boundsOf(xml, pred) {
  const re = /<node[^>]*>/g;
  let m;
  while ((m = re.exec(xml))) {
    const tag = m[0];
    const attr = (name) => /${name}/.test('') ? null : (tag.match(new RegExp(`${name}="([^"]*)"`))?.[1] ?? '');
    const node = {
      text: tag.match(/text="([^"]*)"/)?.[1] ?? '',
      desc: tag.match(/content-desc="([^"]*)"/)?.[1] ?? '',
      rid: tag.match(/resource-id="([^"]*)"/)?.[1] ?? '',
      cls: tag.match(/class="([^"]*)"/)?.[1] ?? '',
      bounds: tag.match(/bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"/),
    };
    void attr;
    if (node.bounds && pred(node)) {
      const [, x1, y1, x2, y2] = node.bounds.map(Number);
      return { x: Math.round((x1 + x2) / 2), y: Math.round((y1 + y2) / 2), node };
    }
  }
  return null;
}

function tap(x, y) {
  adb(['shell', 'input', 'tap', String(x), String(y)]);
}

function typeText(text) {
  // input text escapes spaces as %s
  adb(['shell', 'input', 'text', text.replace(/ /g, '%s')]);
}

const [step, ...rest] = process.argv.slice(2);

switch (step) {
  case 'shot':
    shot(rest[0] ?? 'android-emu-shot');
    break;

  case 'oauth-start': {
    // Foreground the app, set the cloud URL, tap "Sign in with cloud".
    adb(['shell', 'am', 'start', '-n', `${PKG}/.MainActivity`]);
    await sleep(4000);
    const r = evaluate(`(() => {
      const input = document.querySelector('[data-testid=login-cloud-url]');
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(input, 'http://10.0.2.2:8901');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return input.value;
    })()`);
    console.log('cloud url set:', r);
    await sleep(300);
    evaluate(`document.querySelector('[data-testid=login-oauth]').click()`);
    await sleep(5000);
    shot('android-emu-02-gitea-login');
    break;
  }

  case 'browser-login': {
    // Gitea sign-in form in the system browser.
    let xml = uiDump();
    let user = boundsOf(xml, (n) => n.rid.endsWith('user_name') || n.cls.includes('EditText'));
    if (!user) throw new Error('no username field: ' + xml.slice(0, 400));
    tap(user.x, user.y);
    await sleep(500);
    typeText('jcloud-admin');
    await sleep(300);
    xml = uiDump();
    const pass = boundsOf(xml, (n) => n.rid.endsWith('password'));
    if (pass) {
      tap(pass.x, pass.y);
      await sleep(400);
      typeText('jcloud-admin-pass-123');
    }
    await sleep(300);
    xml = uiDump();
    const btn = boundsOf(xml, (n) => /sign in/i.test(n.text));
    if (!btn) throw new Error('no sign-in button');
    tap(btn.x, btn.y);
    await sleep(5000);
    shot('android-emu-03-gitea-authorize');
    break;
  }

  case 'browser-authorize': {
    const xml = uiDump();
    const btn = boundsOf(xml, (n) => /authorize/i.test(n.text) && n.cls.includes('Button'));
    if (!btn) throw new Error('no authorize button: ' + xml.slice(0, 400));
    tap(btn.x, btn.y);
    await sleep(5000);
    shot('android-emu-04-after-authorize');
    break;
  }

  case 'after-oauth': {
    adb(['shell', 'am', 'start', '-n', `${PKG}/.MainActivity`]);
    await sleep(4000);
    const state = evaluate(`(() => ({
      signedIn: !!localStorage.getItem('jmobile.token'),
      cloud: localStorage.getItem('jmobile.cloud_url'),
      hash: location.hash,
      devicesPage: !!document.querySelector('[data-testid=devices-page]'),
      scanEntry: !!document.querySelector('[data-testid=scan-entry]'),
    }))()`);
    console.log('app state:', JSON.stringify(state));
    shot('android-emu-05-devices');
    break;
  }

  case 'scan-pair': {
    const qr = rest[0];
    if (!qr) throw new Error('usage: scan-pair <jcode://pair?...>');
    evaluate(`(() => {
      const a = document.querySelector('[data-testid=scan-entry]');
      a.click();
      return true;
    })()`);
    await sleep(2500);
    shot('android-emu-06-scan');
    const r = evaluate(`(() => {
      const ta = document.querySelector('[data-testid=scan-manual-input]');
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
      setter.call(ta, ${JSON.stringify(qr)});
      ta.dispatchEvent(new Event('input', { bubbles: true }));
      document.querySelector('.scan-manual button[type=submit]').click();
      return ta.value;
    })()`);
    console.log('pasted qr:', r);
    await sleep(6000);
    const state = evaluate(`(() => ({
      hash: location.hash,
      err: document.querySelector('[data-testid=scan-error]')?.textContent ?? null,
      welcome: !!document.querySelector('[data-testid=device-welcome]'),
    }))()`);
    console.log('after claim:', JSON.stringify(state));
    shot('android-emu-07-after-claim');
    break;
  }

  default:
    console.error('unknown step', step);
    process.exit(2);
}
