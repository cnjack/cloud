#!/usr/bin/env node
/*
 * drive.mjs — shared UI-driving helpers for the M6 verification flows.
 * Platform drivers (drive-android.mjs / drive-ios.mjs) supply an `evaluate`
 * primitive (CDP on Android, the vite HMR bridge on iOS) plus screenshot and
 * pairing-approval hooks; this module walks the app flow on top of them.
 */

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Wait until expr evaluates truthy (poll 500ms, up to timeoutMs). */
export async function waitFor(ev, expr, { timeoutMs = 30_000, label = expr } = {}) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      if (await ev(expr)) return;
    } catch {
      /* webview may be reloading */
    }
    if (Date.now() > deadline) throw new Error(`waitFor timed out: ${label}`);
    await sleep(500);
  }
}

/** Fill an input/textarea by selector with proper input events. */
export function fill(ev, selector, value) {
  return ev(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return 'NO ELEMENT ${selector}';
    const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
    el.focus();
    setter.call(el, ${JSON.stringify(value)});
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    return el.value;
  })()`);
}

export function click(ev, selector) {
  return ev(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return 'NO ELEMENT ${selector}';
    el.click();
    return 'clicked';
  })()`);
}

/**
 * The verification flow shared by both platforms. `ctx` provides:
 *   evaluate(expr)   → evaluate JS in the webview, returns the JSON value
 *   shot(name)       → capture a screenshot to the assets dir
 *   approvePairing(pid) → approve a pairing on the rig device
 *   loginUrl         → cloud URL to type (platform loopback)
 *   token            → seeded session token
 */
export async function runFlow(ctx) {
  const ev = ctx.evaluate;

  // -- 01 login page ---------------------------------------------------------
  await waitFor(ev, `!!document.querySelector('[data-testid="login-page"]')`, { label: 'login page' });
  await sleep(1000);
  await ctx.shot('01-login');

  fill(ev, '[data-testid="login-cloud-url"]', ctx.loginUrl);
  fill(ev, '[data-testid="login-token"]', ctx.token);
  await sleep(300);
  click(ev, 'button[type="submit"]');

  // -- 02 device list ---------------------------------------------------------
  await waitFor(ev, `!!document.querySelector('[data-testid="device-card"]')`, { label: 'device card' });
  await sleep(1500); // let online state settle
  await ctx.shot('02-devices');

  // -- 03 device welcome (+ pairing card) --------------------------------------
  click(ev, '[data-testid="device-card"]');
  await waitFor(ev, `!!document.querySelector('[data-testid="device-welcome"]')`, { label: 'welcome page' });
  await waitFor(
    ev,
    `!document.querySelector('[data-testid="device-pairing-card"]') || document.querySelector('[data-testid="device-pairing-card"]')?.dataset.phase !== 'loading'`,
    { label: 'pairing card resolved' },
  );
  await sleep(1000);
  await ctx.shot('03-welcome');

  // -- pairing: start + approve on the device -----------------------------------
  const phase = await ev(`document.querySelector('[data-testid="device-pairing-card"]')?.dataset.phase ?? 'ready'`);
  console.log('pairing phase:', phase);
  if (phase === 'idle' || phase === 'denied' || phase === 'expired' || phase === 'error') {
    click(ev, `[data-testid="device-pairing-card"] button`);
    await waitFor(
      ev,
      `document.querySelector('[data-testid="device-pairing-card"]')?.dataset.phase === 'pending'`,
      { label: 'pairing pending' },
    );
    await sleep(500);
    const code = await ev(`document.querySelector('[data-testid="device-pairing-card"] code')?.textContent ?? ''`);
    const pid = code.trim().split(/\s+/).pop();
    console.log('approving pairing on device:', pid);
    await ctx.approvePairing(pid);
    await waitFor(
      ev,
      `!document.querySelector('[data-testid="device-pairing-card"]')`,
      { timeoutMs: 30_000, label: 'pairing ready (card gone)' },
    );
    console.log('pairing complete — CEK stored');
  }

  // -- 04 session streaming ------------------------------------------------------
  fill(ev, '[data-testid="new-session-composer"] textarea', '用一句话回答：1+1 等于几？');
  await sleep(300);
  click(ev, '[data-testid="new-session-composer"] button[type="submit"]');

  // The new session appears in the list (10s polling backstop).
  await waitFor(ev, `!!document.querySelector('[data-testid="session-row"]')`, { timeoutMs: 30_000, label: 'session row' });
  await sleep(1000);
  click(ev, '[data-testid="session-row"]');
  await waitFor(ev, `!!document.querySelector('[data-testid="device-session"]')`, { label: 'session page' });

  // Streaming / agent activity: streaming bubble, thinking indicator or events.
  await waitFor(
    ev,
    `document.querySelectorAll('[data-testid="device-message-assistant"], [data-testid="device-event"], [data-testid="device-message-user"]').length > 0`,
    { timeoutMs: 60_000, label: 'timeline activity' },
  );
  await ctx.shot('04-session-streaming');

  // Wait for the agent to finish so the last screenshot shows the reply.
  await waitFor(
    ev,
    `!!document.querySelector('[data-testid="device-message-assistant"]')`,
    { timeoutMs: 90_000, label: 'assistant reply' },
  );
  await sleep(800);
  await ctx.shot('05-session-reply');
  console.log('flow complete');
}
