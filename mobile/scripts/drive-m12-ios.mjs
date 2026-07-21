#!/usr/bin/env node
/*
 * drive-m12-ios.mjs — M12 compose screenshot flow on the booted iPhone sim.
 * Prereq: local M12 orchestrator on :18090 (seeded m12-compose-mac device),
 * `pnpm tauri ios dev "iPhone 17 Pro"` running (vite dev :5174).
 * Drives the webview over the vite HMR bridge (hmr-eval.mjs + src/devDrive.ts).
 */
import { execSync, spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runFlow, sleep, waitFor, fill, click } from './drive.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const ASSETS = '/Users/jack/workpath/jjj/jcode-cloud-relay/reports/assets/M12';

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

// Keep the seeded device "online".
execSync(`kubectl --context orbstack -n jcloud exec deploy/postgres -- psql -U jcloud -d jcloud -c "UPDATE devices SET last_seen_at=now() WHERE id='m12composedev00000000000000d1'"`, { stdio: 'pipe' });

// Sanity: the dev server and the app's eval hook are reachable.
const probe = await evaluate('({hash: location.hash, tauri: "__TAURI_INTERNALS__" in window})');
console.log('probe:', JSON.stringify(probe));

// -- login -------------------------------------------------------------------
await waitFor(evaluate, `!!document.querySelector('[data-testid="login-page"]')`, { label: 'login page' });
fill(evaluate, '[data-testid="login-cloud-url"]', 'http://127.0.0.1:18090');
fill(evaluate, '[data-testid="login-token"]', 'jmobile-dev-session-token');
await sleep(300);
click(evaluate, 'button[type="submit"]');

// -- devices → welcome ---------------------------------------------------------
await waitFor(evaluate, `!!document.querySelector('[data-testid="device-card"]')`, { label: 'device card' });
await sleep(1200);
click(evaluate, '[data-testid="device-card"]');
await waitFor(evaluate, `!!document.querySelector('[data-testid="new-session-composer"]')`, { label: 'welcome composer' });

// -- open the compose panel and exercise every element -------------------------
await click(evaluate, '.compose-toggle');
await waitFor(evaluate, `!!document.querySelector('[data-testid="device-compose"]')`, { label: 'compose panel' });

// Select project + model (native select: set value + dispatch change).
await evaluate(`(() => {
  const set = (label, value) => {
    const el = [...document.querySelectorAll('[data-testid="device-compose"] select')]
      .find((s) => s.getAttribute('aria-label') === label);
    if (!el) return 'NO SELECT ' + label;
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
    setter.call(el, value);
    el.dispatchEvent(new Event('change', { bubbles: true }));
    return el.value;
  };
  return [set('项目目录', '/Users/jack/workpath/jjj/cloud'), set('模型', 'anthropic::claude-sonnet-4-5')];
})()`);
// Effort "high" + goal toggle + goal text (zh-Hans UI copy on the sim).
await evaluate(`(() => {
  const btns = [...document.querySelectorAll('[data-testid="device-compose"] button')];
  btns.find((b) => b.textContent.trim() === 'high')?.click();
  btns.find((b) => b.textContent.trim() === '目标')?.click();
  return 'ok';
})()`);
await sleep(300);
fill(evaluate, '[data-testid="device-compose"] input[type="text"]', 'M12: 验证移动端 compose');
// Attach a non-image file through a synthetic DataTransfer change event.
await evaluate(`(() => {
  const input = document.querySelector('[data-testid="device-compose-files"]');
  const dt = new DataTransfer();
  dt.items.add(new File(['M12 compose contract'], 'relay-spec.txt', { type: 'text/plain' }));
  input.files = dt.files;
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return 'attached';
})()`);
await sleep(800);
await waitFor(evaluate, `[...document.querySelectorAll('[data-testid="device-compose"]')].some((d) => d.textContent.includes('relay-spec.txt'))`, { label: 'attachment chip' });
await shot('01-welcome-compose');
console.log('M12 iOS flow done');
