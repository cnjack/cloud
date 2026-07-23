#!/usr/bin/env node
/*
 * drive-m15-ios.mjs — M15 mobile-parity screenshot pass on the booted iPhone
 * sim (agent-driven, NOT a test). Drives the Tauri webview over the vite HMR
 * bridge (hmr-eval.mjs + src/devDrive.ts) against the M14 gray-rollout rig
 * (/tmp/m14-rig/m14-setup.sh). Captures every composer/Thread surface at the
 * real device width plus a horizontal-overflow probe after each step.
 *
 * Prereqs: rig up (orchestrator :18080, jcode web :18088 logged in),
 * `pnpm dev` (:5174) running, tauri app launched on the sim and the ONLY
 * connected HMR client (terminate + relaunch to flush stale clients).
 *
 * Env: DEVICE_ID, SESSION_TOKEN, ASSETS_DIR.
 */
import { execSync, spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ASSETS = process.env.ASSETS_DIR ?? '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M15';
const BASE = process.env.BASE ?? 'http://127.0.0.1:18080';
const TOKEN = process.env.SESSION_TOKEN ?? 'm14-shots-session-token';
const DEVICE_ID = process.env.DEVICE_ID ?? '449adc9da6257c7e9eaec003768de5cc';
const RIG = '/tmp/m14-rig';

mkdirSync(ASSETS, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function evaluate(expr) {
  const res = spawnSync('node', [join(HERE, 'hmr-eval.mjs'), expr], { encoding: 'utf8', timeout: 40_000 });
  if (res.status !== 0) throw new Error(`hmr eval failed: ${res.stderr || res.stdout}\nexpr: ${expr.slice(0, 120)}`);
  return JSON.parse(res.stdout.trim());
}

async function waitFor(expr, { timeoutMs = 30_000, label = expr } = {}) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      if (evaluate(expr)) return;
    } catch {
      /* webview reloading */
    }
    if (Date.now() > deadline) throw new Error(`waitFor timed out: ${label}`);
    await sleep(500);
  }
}

function shot(name) {
  const out = join(ASSETS, `ios-${name}.png`);
  execSync(`xcrun simctl io booted screenshot "${out}"`);
  console.log('shot', out);
}

/** Horizontal-overflow probe: page-level scrollWidth + unclipped elements
    poking out of the viewport inside the chat surfaces. */
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

async function api(path, init = {}) {
  const res = await fetch(`${BASE}/api/v1${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${TOKEN}` },
  });
  if (!res.ok) throw new Error(`${path} -> ${res.status} ${await res.text()}`);
  return res.status === 204 ? null : res.json();
}

const esc = () => evaluate(`(document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })), 'esc')`);
const clickText = (scope, re) => evaluate(`(() => {
  const root = document.querySelector(${JSON.stringify(scope)}) ?? document;
  const b = [...root.querySelectorAll('button')].find((x) => ${re}.test(x.textContent.trim()) || ${re}.test(x.getAttribute('aria-label') ?? '') || ${re}.test(x.getAttribute('title') ?? ''));
  if (!b) return 'NO BTN ' + ${re};
  b.click();
  return 'clicked: ' + (b.textContent.trim() || b.getAttribute('aria-label') || b.getAttribute('title')).slice(0, 30);
})()`);

// -- 0. seed a session with markdown-heavy content ---------------------------
await api(`/devices/${DEVICE_ID}/sessions/new/messages`, {
  method: 'POST',
  body: JSON.stringify({ text: 'Reply with a short markdown answer: a ## heading, one `inline code`, a fenced ```js code block, and a bullet list.' }),
});
console.log('seeded session via relay');
await sleep(14_000);

// -- 1. welcome page: composer surfaces ---------------------------------------
await waitFor(`!!document.querySelector('[data-testid="new-session-composer"]')`, { label: 'welcome composer' });
await sleep(1200);
overflow('welcome');
shot('01-welcome-composer');

// workspace (path) chip → picker panel
console.log(await clickText('[data-testid="new-session-composer"]', '/设备默认|\\/tmp|\\/Users/'));
await sleep(600);
overflow('workspace-picker');
shot('02-workspace-picker');
await esc();

// "+" menu
console.log(await clickText('[data-testid="new-session-composer"]', '/^添加$/'));
await sleep(500);
overflow('add-menu');
shot('03-add-menu');

// Goal entry → armed chip
console.log(await clickText('body', '/^目标$|^Goal$/'));
await sleep(500);
overflow('goal-armed');
shot('04-goal-armed');
// disarm via the chip X
console.log(await clickText('[data-testid="new-session-composer"]', '/移除目标|Remove goal/'));
await sleep(300);

// mode chip → mode panel
console.log(await clickText('[data-testid="new-session-composer"]', '/逐次确认|计划|自动|完全放行/'));
await sleep(500);
overflow('mode-panel');
shot('05-mode-panel');
await esc();

// model chip → model panel
console.log(await clickText('[data-testid="new-session-composer"]', '/^model$|mock-model/'));
await sleep(600);
overflow('model-panel');
shot('06-model-panel');
await esc();

// slash menu
await evaluate(`(() => {
  const el = document.querySelector('[data-testid="new-session-composer"] textarea');
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  el.focus(); setter.call(el, '/'); el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`);
await sleep(700);
overflow('slash-menu');
shot('07-slash-menu');
await esc();
await evaluate(`(() => {
  const el = document.querySelector('[data-testid="new-session-composer"] textarea');
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  setter.call(el, ''); el.dispatchEvent(new Event('input', { bubbles: true }));
  return 'cleared';
})()`);

// -- 2. session page: Thread + docked composer --------------------------------
await waitFor(`!!document.querySelector('[data-testid="session-row"]')`, { label: 'session row' });
await sleep(500);
shot('08-session-list');
await evaluate(`(document.querySelector('[data-testid="session-row"]').click(), 'open session')`);
await waitFor(`!!document.querySelector('[data-testid="device-session"]')`, { label: 'session page' });
await sleep(2500);
overflow('session-thread');
shot('09-session-thread');

// send a follow-up through the docked composer (streaming + stop button)
await evaluate(`(() => {
  const el = document.querySelector('[data-testid="session-composer"] textarea');
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  el.focus(); setter.call(el, '用三行要点回答：移动端适配要注意什么？'); el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`);
await sleep(400);
shot('10-session-composed');
console.log(await clickText('[data-testid="session-composer"]', '/^发送$|^Send$/'));
await sleep(3500);
console.log('stop enabled while running:', evaluate(`[...document.querySelectorAll('[data-testid="session-composer"] button')].some((button) => /^(停止|Stop)$/.test(button.textContent.trim()))`));
overflow('streaming');
shot('11-session-streaming');
await waitFor(`![...document.querySelectorAll('[data-testid="session-composer"] button')].some((button) => /^(停止|Stop)$/.test(button.textContent.trim()))`, { timeoutMs: 90_000, label: 'turn finished' });
await sleep(1000);
overflow('session-done');
shot('12-session-done');

// -- 3. approval card at device width ------------------------------------------
// switch the next message to approval mode, then ask for a file write.
console.log(await clickText('[data-testid="session-composer"]', '/逐次确认|计划|自动|完全放行/'));
await sleep(400);
console.log(await clickText('body', '/^逐次确认$/'));
await sleep(300);
await evaluate(`(() => {
  const el = document.querySelector('[data-testid="session-composer"] textarea');
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  el.focus(); setter.call(el, 'Create a file m15-note.txt with the content hello'); el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`);
console.log(await clickText('[data-testid="session-composer"]', '/^发送$|^Send$/'));
const sawApproval = await waitFor(
  `[...document.querySelectorAll('button')].some((b) => /允许|Approve|Allow|Deny|拒绝/.test(b.textContent))`,
  { timeoutMs: 45_000, label: 'approval card' },
).then(() => true).catch(() => false);
if (sawApproval) {
  await sleep(600);
  overflow('approval-card');
  shot('13-approval-card');
  console.log(await clickText('body', '/允许一次|Allow once|Approve/i'));
  await sleep(2500);
  shot('14-approval-resolved');
} else {
  console.log('NOTE: no approval card appeared (mockllm may not have requested one)');
}

// -- 4. offline banner ----------------------------------------------------------
const webPid = String(execSync(`cat ${RIG}/web.pid`)).trim();
try { process.kill(Number(webPid)); } catch { /* already gone */ }
console.log('killed jcode web, waiting for offline…');
let offline = false;
for (let i = 0; i < 40; i++) {
  const devs = await api('/devices');
  if (devs.devices.find((d) => d.id === DEVICE_ID)?.online === false) { offline = true; break; }
  await sleep(3000);
}
console.log('device offline:', offline);
await evaluate(`(location.hash = '#/devices/${DEVICE_ID}', location.reload(), 'reload')`);
await waitFor(`!!document.querySelector('[data-testid="new-session-composer"]')`, { timeoutMs: 60_000, label: 'welcome after reload' });
await sleep(1500);
console.log('offline banner:', evaluate(`!!document.querySelector('.banner')`));
overflow('offline');
shot('15-offline-banner');

console.log('M15 iOS pass done');
