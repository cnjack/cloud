#!/usr/bin/env node
/*
 * drive-m15-android.mjs — M15 mobile-parity pass on the Android emulator
 * (agent-driven, NOT a test). Chrome on the booted emulator loads the vite
 * dev server through `adb reverse tcp:5174` and logs in with the dev server's
 * OWN origin as cloud URL (same-origin /api+/auth proxy, VITE_API_PROXY_TARGET)
 * because real browsers enforce CORS. Driven over chrome_devtools_remote CDP;
 * screencaps via adb.
 *
 * Prereqs: rig up (orchestrator :18080 + jcode web :18088 online),
 * `VITE_API_PROXY_TARGET=http://127.0.0.1:18080 pnpm dev` on :5174,
 * `adb reverse tcp:5174 tcp:5174`, Chrome open on http://localhost:5174.
 *
 * Env: DEVICE_ID, SESSION_TOKEN, ASSETS_DIR, ADB.
 */
import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ADB = process.env.ADB ?? join(homedir(), 'Library/Android/sdk/platform-tools/adb');
const ASSETS = process.env.ASSETS_DIR ?? '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M15';
const DEVICE_ID = process.env.DEVICE_ID ?? '449adc9da6257c7e9eaec003768de5cc';
const TOKEN = process.env.SESSION_TOKEN ?? 'm14-shots-session-token';
const CDP_PORT = 9223;

mkdirSync(ASSETS, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function evaluate(expr) {
  execFileSync(ADB, ['forward', `tcp:${CDP_PORT}`, 'localabstract:chrome_devtools_remote'], { stdio: 'ignore' });
  const targets = JSON.parse(execSync(`curl -sS http://localhost:${CDP_PORT}/json`, { encoding: 'utf8' }));
  const page = targets.find((t) => t.type === 'page' && t.url.includes('localhost:5174'));
  if (!page) throw new Error('no localhost:5174 page target');
  const res = spawnSync('node', [join(HERE, 'cdp.mjs'), page.webSocketDebuggerUrl, expr], {
    encoding: 'utf8',
    timeout: 40_000,
  });
  if (res.status !== 0) throw new Error(`cdp eval failed: ${res.stderr || res.stdout}\nexpr: ${expr.slice(0, 120)}`);
  return JSON.parse(res.stdout.trim());
}

async function waitFor(expr, { timeoutMs = 30_000, label = expr } = {}) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      if (evaluate(expr)) return;
    } catch {
      /* page navigating */
    }
    if (Date.now() > deadline) throw new Error(`waitFor timed out: ${label}`);
    await sleep(500);
  }
}

function shot(name) {
  const out = join(ASSETS, `android-${name}.png`);
  execSync(`${ADB} exec-out screencap -p > "${out}"`);
  console.log('shot', out);
}

function overflow(tag) {
  const r = evaluate(`(() => {
    const vw = innerWidth;
    const bad = [];
    for (const el of document.querySelectorAll('.app-shell *')) {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height) continue;
      if (getComputedStyle(el).visibility === 'hidden') continue;
      if (r.right > vw + 1 || r.left < -1) {
        let p = el.parentElement, clipped = false;
        while (p) {
          const pc = getComputedStyle(p);
          if (pc.overflowX !== 'visible') {
            const pr = p.getBoundingClientRect();
            if (r.right <= pr.right + 1 && r.left >= pr.left - 1) clipped = true;
            break;
          }
          p = p.parentElement;
        }
        if (!clipped) bad.push(el.tagName + '.' + String(el.className).split(' ').slice(0, 2).join('.') + ' L' + Math.round(r.left) + ' R' + Math.round(r.right));
      }
    }
    return { vw, docDelta: document.documentElement.scrollWidth - vw, bad: bad.slice(0, 8) };
  })()`);
  console.log(`overflow[${tag}]`, JSON.stringify(r));
  return r;
}

const esc = () => evaluate(`(document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })), 'esc')`);
const clickText = (scope, re) => evaluate(`(() => {
  const root = document.querySelector(${JSON.stringify(scope)}) ?? document;
  const b = [...root.querySelectorAll('button')].find((x) => ${re}.test(x.textContent.trim()) || ${re}.test(x.getAttribute('aria-label') ?? '') || ${re}.test(x.getAttribute('title') ?? ''));
  if (!b) return 'NO BTN ' + ${re};
  b.click();
  return 'clicked: ' + (b.textContent.trim() || b.getAttribute('aria-label') || b.getAttribute('title')).slice(0, 30);
})()`);
const fillTa = (scope, text) => evaluate(`(() => {
  const el = document.querySelector(${JSON.stringify(scope)} + ' textarea');
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  el.focus(); setter.call(el, ${JSON.stringify(text)}); el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`);

// -- 0. clean slate + login -----------------------------------------------------
evaluate(`(localStorage.clear(), location.reload(), 'reset')`);
await waitFor(`!!document.querySelector('[data-testid="login-page"]')`, { label: 'login page' });
evaluate(`(() => {
  const set = (sel, v) => { const el = document.querySelector(sel); if (!el) return 'NO ' + sel;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    el.focus(); setter.call(el, v); el.dispatchEvent(new Event('input', { bubbles: true })); return el.value; };
  const r = [set('[data-testid=login-cloud-url]', 'http://localhost:5174'), set('[data-testid=login-token]', '${TOKEN}')];
  document.querySelector('button[type=submit]')?.click();
  return r;
})()`);
await waitFor(`!!document.querySelector('[data-testid="device-card"]')`, { label: 'device card' });
await sleep(1500);
shot('01-devices');

// -- 1. welcome page ------------------------------------------------------------
evaluate(`(document.querySelector('[data-testid="device-card"]').click(), 'open device')`);
await waitFor(`!!document.querySelector('[data-testid="new-session-composer"]')`, { label: 'welcome composer' });
await sleep(1500);
overflow('welcome');
shot('02-welcome-composer');

console.log(await clickText('[data-testid="new-session-composer"]', '/设备默认|\\/tmp|\\/Users/'));
await sleep(700);
console.log('ws panel:', JSON.stringify(evaluate(`(() => { const p = document.querySelector('.ws-panel'); if (!p) return null; const r = p.getBoundingClientRect(); return { cls: p.className, onscreen: r.bottom <= innerHeight && r.top >= 0 }; })()`)));
shot('03-workspace-picker');
await esc();

console.log(await clickText('[data-testid="new-session-composer"]', '/^添加$/'));
await sleep(500);
shot('04-add-menu');

console.log(await clickText('body', '/^目标$|^Goal$/'));
await sleep(500);
overflow('goal-armed');
console.log('toolbar wrap:', evaluate(`getComputedStyle(document.querySelector('.jcode-product div[class~="justify-between"][class~="py-2"]')).flexWrap`));
shot('05-goal-armed');
console.log(await clickText('[data-testid="new-session-composer"]', '/移除目标|Remove goal/'));
await sleep(300);

console.log(await clickText('[data-testid="new-session-composer"]', '/逐次确认|计划|自动|完全放行/'));
await sleep(500);
shot('06-mode-panel');
await esc();

console.log(await clickText('[data-testid="new-session-composer"]', '/^model$|mock-model/'));
await sleep(600);
console.log('model panel:', JSON.stringify(evaluate(`(() => { const p = [...document.querySelectorAll('.jcode-product div')].find((d) => d.className.includes('w-[290px]')); if (!p) return null; const r = p.getBoundingClientRect(); return { left: Math.round(r.left), right: Math.round(r.right), vw: innerWidth, fits: r.left >= 0 && r.right <= innerWidth }; })()`)));
overflow('model-panel');
shot('07-model-panel');
await esc();

await fillTa('[data-testid="new-session-composer"]', '/');
await sleep(700);
shot('08-slash-menu');
await esc();
await fillTa('[data-testid="new-session-composer"]', '');

// -- 2. session page --------------------------------------------------------------
await waitFor(`!!document.querySelector('[data-testid="session-row"]')`, { label: 'session row' });
await sleep(400);
shot('09-session-list');
evaluate(`(document.querySelector('[data-testid="session-row"]').click(), 'open session')`);
await waitFor(`!!document.querySelector('[data-testid="device-session"]')`, { label: 'session page' });
await sleep(2500);
overflow('session-thread');
shot('10-session-thread');

await fillTa('[data-testid="session-composer"]', '用三行要点回答：移动端适配要注意什么？');
await sleep(300);
console.log(await clickText('[data-testid="session-composer"]', '/^发送$|^Send$/'));
// The composer owns the only stop button (mockllm window is ~200ms; sample fast).
console.log('stop window:', JSON.stringify(evaluate(`(async () => {
  const t0 = Date.now(); let enabledAt = null, disabledAgainAt = null;
  while (Date.now() - t0 < 5000) {
    const stop = [...document.querySelectorAll('[data-testid="session-composer"] button')]
      .find((button) => /^(停止|Stop)$/.test(button.textContent.trim()));
    if (stop && enabledAt === null) enabledAt = Date.now() - t0;
    if (!stop && enabledAt !== null) { disabledAgainAt = Date.now() - t0; break; }
    await new Promise((r) => setTimeout(r, 40));
  }
  return { enabledAt, disabledAgainAt };
})()`)));
await sleep(2500);
shot('11-session-streaming');
await waitFor(`![...document.querySelectorAll('[data-testid="session-composer"] button')].some((button) => /^(停止|Stop)$/.test(button.textContent.trim()))`, { timeoutMs: 60_000, label: 'turn finished' });
await sleep(1000);
overflow('session-done');
shot('12-session-done');

console.log('M15 Android pass done');
