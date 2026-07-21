#!/usr/bin/env node
/**
 * m14-shots.mjs — one-off M14 screenshot pass (agent-driven, NOT a test):
 * drives a real browser through the console device pages wired to a live
 * jcode device (gray-rollout rig from m14-setup.sh) and captures the product
 * composer + Thread surfaces for eyeball comparison against the desktop.
 *
 *   node m14-shots.mjs
 *
 * Env: WEB (console), BASE (orchestrator), DEVICE_ID, SESSION_TOKEN, SHOTS_DIR.
 */
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const WEB = process.env.WEB || 'http://localhost:5173';
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const DEVICE_ID = process.env.DEVICE_ID || '449adc9da6257c7e9eaec003768de5cc';
const SESSION_TOKEN = process.env.SESSION_TOKEN || 'm14-shots-session-token';
const SHOTS = process.env.SHOTS_DIR || '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M14';

const shot = async (page, name) => {
  await page.screenshot({ path: join(SHOTS, name), fullPage: false });
  console.log(`shot  ${name}`);
};

async function api(path, init = {}) {
  const res = await fetch(`${BASE}/api/v1${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${SESSION_TOKEN}` },
  });
  if (!res.ok) throw new Error(`${path} -> ${res.status} ${await res.text()}`);
  return res.status === 204 ? null : res.json();
}

async function main() {
  mkdirSync(SHOTS, { recursive: true });

  // Seed a session so capabilities.projects + the session list have content:
  // one plain message through the relay (mockllm answers fast).
  await api(`/devices/${DEVICE_ID}/sessions/new/messages`, {
    method: 'POST',
    body: JSON.stringify({ text: 'Introduce yourself in one short sentence, then list 3 bullet points about what you can do.' }),
  });
  console.log('seeded a session via relay send');
  await new Promise((r) => setTimeout(r, 12_000)); // let the agent finish + capabilities resync

  const browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  // Console "Advanced" token path: Bearer from localStorage (the seeded
  // orchestrator session token doubles as a console token).
  await ctx.addInitScript((tok) => {
    try { localStorage.setItem('jcloud.console.token', tok); } catch {}
  }, SESSION_TOKEN);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => console.log('[pageerror]', String(e).slice(0, 300)));

  // ── Welcome page: the product composer ──────────────────────────────────
  await page.goto(`${WEB}/devices/${DEVICE_ID}`);
  const composer = page.getByTestId('new-session-composer');
  await composer.waitFor({ timeout: 15000 });
  await page.waitForTimeout(1200);
  await shot(page, '01-welcome-composer.png');

  // Workspace (path) chip + picker.
  const wsChip = composer.locator('button', { hasText: /Device default|\/tmp|\/Users/ }).first();
  await wsChip.click();
  await page.waitForTimeout(500);
  await shot(page, '02-workspace-picker.png');
  await page.keyboard.press('Escape');

  // "+" menu.
  await composer.getByRole('button', { name: 'Add', exact: true }).click();
  await page.waitForTimeout(400);
  await shot(page, '03-add-menu.png');

  // Goal entry → armed chip.
  await page.getByRole('button', { name: 'Goal', exact: true }).first().click();
  await page.waitForTimeout(400);
  await shot(page, '04-goal-armed.png');
  // Disarm via the chip's X.
  await composer.getByRole('button', { name: 'Remove goal' }).click();

  // Model panel.
  await composer.locator('button', { hasText: /model/ }).first().click();
  await page.waitForTimeout(500);
  await shot(page, '05-model-panel.png');
  await page.keyboard.press('Escape');

  // Slash menu.
  const textarea = composer.getByRole('textbox');
  await textarea.click();
  await textarea.pressSequentially('/');
  await page.waitForTimeout(600);
  await shot(page, '06-slash-menu.png');
  await page.keyboard.press('Escape');

  // ── Session page: Thread + docked composer ──────────────────────────────
  const row = page.getByTestId('session-row').first();
  await row.waitFor({ timeout: 15000 });
  await row.click();
  await page.waitForTimeout(2500);
  await shot(page, '07-session-thread.png');

  // Send a markdown-heavy follow-up through the product composer.
  const docked = page.getByTestId('session-composer').getByRole('textbox');
  await docked.click();
  await docked.pressSequentially('Reply with a short markdown answer: a ## heading, one `inline code`, and a fenced ```js code block.');
  await shot(page, '08-session-composed.png');
  await docked.press('Enter');
  await page.waitForTimeout(4000);
  await shot(page, '09-session-streaming.png');
  await page.waitForTimeout(12_000);
  await shot(page, '10-session-done.png');

  await browser.close();
  console.log('done');
}

main().catch((e) => { console.error(e); process.exitCode = 1; });
