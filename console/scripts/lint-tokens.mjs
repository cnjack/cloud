#!/usr/bin/env node
/*
 * lint:tokens — enforces the design-system rule that colors live ONLY in
 * src/styles/tokens.css. Greps src/ for hex color literals (#abc / #aabbcc /
 * #aabbccdd) and raw rgb()/rgba()/hsl() calls anywhere else and fails.
 *
 * This keeps the re-skin a single-file swap: if this passes, every color in the
 * app is a var(--…) that resolves in tokens.css.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, extname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { dirname } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const srcDir = join(root, 'src');

// Only tokens.css is allowed to contain raw color values.
const ALLOWLIST = new Set(['src/styles/tokens.css']);

const SCAN_EXT = new Set(['.css', '.ts', '.tsx', '.js', '.jsx']);

const HEX = /#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g;
const FUNC = /\b(?:rgb|rgba|hsl|hsla)\s*\(/g;

/** @param {string} dir */
function walk(dir) {
  /** @type {string[]} */
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...walk(full));
    } else if (SCAN_EXT.has(extname(full))) {
      out.push(full);
    }
  }
  return out;
}

const violations = [];

for (const file of walk(srcDir)) {
  const rel = relative(root, file).split('\\').join('/');
  if (ALLOWLIST.has(rel)) continue;

  const lines = readFileSync(file, 'utf8').split('\n');
  lines.forEach((line, i) => {
    // Ignore comment-only lines to reduce noise (still catches inline styles).
    for (const re of [HEX, FUNC]) {
      re.lastIndex = 0;
      let m;
      while ((m = re.exec(line)) !== null) {
        violations.push({ rel, line: i + 1, text: line.trim(), match: m[0] });
      }
    }
  });
}

if (violations.length > 0) {
  console.error(
    `\n✗ lint:tokens found ${violations.length} raw color value(s) outside tokens.css:\n`,
  );
  for (const v of violations) {
    console.error(`  ${v.rel}:${v.line}  ${v.match}   →  ${v.text}`);
  }
  console.error(
    '\nMove the color into src/styles/tokens.css as a --token and reference var(--token).\n',
  );
  process.exit(1);
}

console.log('✓ lint:tokens: no raw colors outside tokens.css');
