#!/usr/bin/env node
/**
 * browser-device-auth.mjs — browser-driven e2e for the jcode device-code
 * login flow, against the local rig:
 *
 *   orbstack orchestrator (port-forward :18080) + console vite dev (:5173)
 *
 * What it proves (with a REAL browser, headed by default):
 *   1. `jcode login --cloud http://127.0.0.1:18080` prints a user_code
 *   2. the console /device authorize page (served by vite, proxying the
 *      orchestrator) accepts that code for a seeded user session
 *   3. clicking 批准 lets the CLI complete login (cloud.json written)
 *
 * Prereqs: port-forward on :18080, vite dev on :5173, kubectl context
 * orbstack, jcode binary built. Run from the cloud repo or anywhere:
 *   node browser-device-auth.mjs [--headless]
 */
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, existsSync, statSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';

const HEADLESS = process.argv.includes('--headless');
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const WEB = process.env.WEB || 'http://localhost:5173';
const JCODE = process.env.JCODE_BIN || '/Users/jack/workpath/jjj/jcode/jcode';
const SHOTS = process.env.SHOTS_DIR || '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/browser-rig';
const KCTX = 'orbstack', NS = 'jcloud';

const USER_NAME = 'browser-rig-tester';
const SESSION_TOKEN = 'browser-rig-session-token';

const psql = (sql) =>
  execSync(
    `kubectl --context ${KCTX} -n ${NS} exec -i $(kubectl --context ${KCTX} -n ${NS} get pods --no-headers | awk '/postgres/{print $1; exit}') -- sh -c 'psql "$POSTGRES_DB" -U "$POSTGRES_USER" -t -A'`,
    { input: sql, encoding: 'utf8', shell: '/bin/bash' }
  ).trim();

const pass = (msg) => console.log(`PASS  ${msg}`);
const fail = (msg) => { console.error(`FAIL  ${msg}`); process.exitCode = 1; };

let home, browser;
async function cleanup() {
  try { browser?.close(); } catch {}
  try { psql(`DELETE FROM users WHERE display_name=$$${USER_NAME}$$`); } catch {}
  try { home && rmSync(home, { recursive: true, force: true }); } catch {}
}

async function main() {
  execSync(`mkdir -p "${SHOTS}"`);
  // 1. seed user + session (HashToken = sha256 hex). Dollar-quote all string
  // literals: the SQL travels through nested shell single-quoting.
  const hash = createHash('sha256').update(SESSION_TOKEN).digest('hex');
  const uid = psql(`INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
                    VALUES ($$usr-browser-rig$$, $$${USER_NAME}$$, $$$$, false, now())
                    ON CONFLICT (id) DO UPDATE SET display_name=EXCLUDED.display_name
                    RETURNING id`);
  const userId = uid.split('\n')[0].trim(); // psql appends the command tag
  if (!userId) throw new Error('seed user failed (empty uid)');
  psql(`INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at)
        VALUES ($$ses-browser-rig$$, $$${userId}$$, $$${hash}$$, now(), now() + interval '1 hour')
        ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at`);
  pass(`seeded user session (${userId})`);

  // 2. start jcode login (browser auto-open disabled; WE drive the browser)
  home = mkdtempSync(join(tmpdir(), 'jcode-browser-rig-'));
  const cli = spawn(JCODE, ['login', '--cloud', BASE, '--name', 'browser-rig-device'], {
    env: { ...process.env, HOME: home, JCODE_NO_BROWSER: '1' },
  });
  let out = '';
  const codePromise = new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('user_code not printed in 15s')), 15000);
    cli.stdout.on('data', (d) => {
      out += d;
      const m = out.match(/\b([A-Z0-9]{4}-[A-Z0-9]{4})\b/);
      if (m) { clearTimeout(t); resolve(m[1]); }
    });
  });
  const userCode = await codePromise;
  pass(`CLI printed user_code ${userCode}`);

  // 3. drive a real browser through the authorize page
  browser = await chromium.launch({ headless: HEADLESS });
  const ctx = await browser.newContext();
  await ctx.addCookies([{ name: 'jcloud_session', value: SESSION_TOKEN, url: WEB }]);
  const page = await ctx.newPage();
  await page.goto(`${WEB}/device?user_code=${encodeURIComponent(userCode)}`);
  await page.screenshot({ path: join(SHOTS, '01-device-authorize.png'), fullPage: true });

  // step 1: the code is pre-filled from ?user_code= — confirm it ("Continue"),
  // step 2: the confirm page offers Approve / Deny.
  const cont = page.getByRole('button', { name: /继续|continue/i });
  await cont.first().waitFor({ timeout: 10000 });
  await cont.first().click();
  const approve = page.getByRole('button', { name: /批准|approve|authorize/i });
  await approve.first().waitFor({ timeout: 10000 });
  await page.screenshot({ path: join(SHOTS, '02-device-confirm.png'), fullPage: true });
  await approve.first().click();
  await page.screenshot({ path: join(SHOTS, '03-device-approved.png'), fullPage: true });
  pass('clicked approve in the browser');

  // 4. CLI must now complete login
  const exit = await new Promise((r) => cli.on('exit', r));
  if (exit !== 0) { fail(`jcode login exited ${exit}\n${out}`); return; }
  const credsPath = join(home, '.jcode', 'cloud.json');
  if (!existsSync(credsPath)) { fail('cloud.json missing after login'); return; }
  const mode = statSync(credsPath).mode & 0o777;
  const creds = JSON.parse(readFileSync(credsPath, 'utf8'));
  if (mode !== 0o600) { fail(`cloud.json mode ${mode.toString(8)} != 600`); return; }
  if (!creds.device_token?.startsWith('jcd_')) { fail('device_token missing jcd_ prefix'); return; }
  pass(`CLI login completed; cloud.json 0600, device_token jcd_…, device ${creds.device_id}`);
  console.log('\nRESULT: browser-driven device auth flow OK');
}

main().catch((e) => { console.error(e); process.exitCode = 1; }).finally(cleanup);
