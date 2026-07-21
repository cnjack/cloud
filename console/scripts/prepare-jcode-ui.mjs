#!/usr/bin/env node
// M14 transition helper — make the console image build work while
// jcode-ui/jcode-ui-core are consumed as unpublished `file:` deps pointing at
// the sibling jcode repo checkout.
//
// What `node scripts/prepare-jcode-ui.mjs` does (idempotent):
//   1. Locates the jcode repo (env JCODE_REPO, else ../../jcode relative to
//      console/) and makes sure packages/jcode-ui-core and packages/jcode-ui
//      have a built dist/ (builds them if missing).
//   2. `pnpm pack`s both packages into console/.pkg/ (gitignored).
//   3. Temporarily rewrites every `file:...jcode/packages/...` reference —
//      console/package.json, console/packages/device-ui/package.json,
//      mobile/package.json and the overrides in both pnpm-workspace.yaml
//      files — to `file:` tarballs under console/.pkg/ (paths relative to
//      each manifest).
//   4. Adds a temporary `COPY .pkg ./.pkg` line to console/Dockerfile (the
//      committed Dockerfile is untouched; without it the tarballs would not
//      exist in the image at the early `pnpm install --frozen-lockfile`
//      layer).
//   5. Re-runs `pnpm install` in console/ and mobile/ so both lockfiles match
//      the rewritten manifests.
//
// `node scripts/prepare-jcode-ui.mjs --restore` puts every file back from the
// backups in console/.pkg/backup/, deletes .pkg/, and reinstalls so the
// workspace is back in the plain `file:../../jcode/...` state.
//
// Delete this script (and .pkg/) once jcode-ui is published and the manifests
// point at the registry again.

import { execFileSync } from 'node:child_process';
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const consoleRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const cloudRoot = resolve(consoleRoot, '..');
const mobileRoot = resolve(cloudRoot, 'mobile');
const pkgDir = resolve(consoleRoot, '.pkg');
const backupDir = resolve(pkgDir, 'backup');
const restore = process.argv.includes('--restore');

const log = (msg) => console.log(`[prepare-jcode-ui] ${msg}`);
const run = (cmd, args, cwd) => {
  log(`$ ${cmd} ${args.join(' ')}  (cwd: ${cwd})`);
  execFileSync(cmd, args, { cwd, stdio: 'inherit' });
};

// Files that get rewritten, with their location relative to cloudRoot.
const REWRITTEN_FILES = [
  'console/package.json',
  'console/packages/device-ui/package.json',
  'console/pnpm-workspace.yaml',
  'console/pnpm-lock.yaml',
  'console/Dockerfile',
  'mobile/package.json',
  'mobile/pnpm-workspace.yaml',
  'mobile/pnpm-lock.yaml',
];
const backupName = (rel) => rel.replaceAll('/', '__');

if (restore) {
  if (!existsSync(backupDir)) {
    log('no backup found — nothing to restore (already in file: state?)');
    process.exit(0);
  }
  for (const rel of REWRITTEN_FILES) {
    const backup = resolve(backupDir, backupName(rel));
    if (existsSync(backup)) cpSync(backup, resolve(cloudRoot, rel));
  }
  rmSync(pkgDir, { recursive: true, force: true });
  log('restored package.json / pnpm-workspace.yaml / lockfiles / Dockerfile');
  // node_modules still points at the .pkg tarballs — reinstall to flip back.
  run('pnpm', ['install', '--frozen-lockfile'], consoleRoot);
  run('pnpm', ['install', '--frozen-lockfile'], mobileRoot);
  log('done — workspace is back in the file:../../jcode state');
  process.exit(0);
}

// --- 1. locate the jcode repo ------------------------------------------------
const jcodeRepo = process.env.JCODE_REPO
  ? resolve(process.env.JCODE_REPO)
  : resolve(consoleRoot, '../../jcode');
const pkgDirs = {
  'jcode-ui-core': resolve(jcodeRepo, 'packages/jcode-ui-core'),
  'jcode-ui': resolve(jcodeRepo, 'packages/jcode-ui'),
};
for (const [name, dir] of Object.entries(pkgDirs)) {
  if (!existsSync(resolve(dir, 'package.json'))) {
    console.error(
      `[prepare-jcode-ui] jcode repo not found at ${jcodeRepo} ` +
        `(missing packages/${name}/package.json). ` +
        'Set JCODE_REPO or check out cnjack/jcode next to cloud/.',
    );
    process.exit(1);
  }
}

// --- 2. make sure both packages have a built dist/ ---------------------------
const needsBuild = Object.values(pkgDirs).some(
  (dir) => !existsSync(resolve(dir, 'dist/index.js')),
);
if (needsBuild) {
  log('dist/ missing — installing + building jcode packages');
  run('pnpm', ['install', '--frozen-lockfile'], jcodeRepo);
  run('pnpm', ['build'], pkgDirs['jcode-ui-core']); // core first: jcode-ui depends on it
  run('pnpm', ['build'], pkgDirs['jcode-ui']);
} else {
  log('jcode packages already built (dist/ present)');
}

// --- 3. pnpm pack both packages into console/.pkg/ ---------------------------
mkdirSync(pkgDir, { recursive: true });
const tarballs = {};
for (const [name, dir] of Object.entries(pkgDirs)) {
  const { version } = JSON.parse(
    readFileSync(resolve(dir, 'package.json'), 'utf8'),
  );
  run('pnpm', ['pack', '--pack-destination', pkgDir], dir);
  const tgz = `${name}-${version}.tgz`;
  if (!existsSync(resolve(pkgDir, tgz))) {
    console.error(`[prepare-jcode-ui] expected ${pkgDir}/${tgz} after pnpm pack`);
    process.exit(1);
  }
  tarballs[name] = tgz;
}

// --- 4. back up originals (first run only — keeps the script idempotent) -----
if (!existsSync(backupDir)) {
  mkdirSync(backupDir, { recursive: true });
  for (const rel of REWRITTEN_FILES) {
    cpSync(resolve(cloudRoot, rel), resolve(backupDir, backupName(rel)));
  }
  log(`backed up ${REWRITTEN_FILES.length} files into .pkg/backup/`);
} else {
  log('backup already present — reusing (idempotent re-run)');
}

// --- 5. rewrite the file: references to the tarballs -------------------------
// Path from each manifest's directory to console/.pkg/.
const pkgRef = {
  'console/package.json': '.pkg',
  'console/packages/device-ui/package.json': '../../.pkg',
  'console/pnpm-workspace.yaml': '.pkg',
  'mobile/package.json': '../console/.pkg',
  'mobile/pnpm-workspace.yaml': '../console/.pkg',
};
const rewriteJson = (rel) => {
  const path = resolve(cloudRoot, rel);
  const json = JSON.parse(readFileSync(path, 'utf8'));
  for (const section of ['dependencies', 'devDependencies', 'peerDependencies']) {
    for (const name of Object.keys(tarballs)) {
      const value = json[section]?.[name];
      if (typeof value === 'string' && value.startsWith('file:')) {
        json[section][name] = `file:${pkgRef[rel]}/${tarballs[name]}`;
      }
    }
  }
  writeFileSync(path, `${JSON.stringify(json, null, 2)}\n`);
  log(`rewrote ${rel}`);
};
const rewriteWorkspaceYaml = (rel) => {
  const path = resolve(cloudRoot, rel);
  let text = readFileSync(path, 'utf8');
  for (const name of Object.keys(tarballs)) {
    text = text.replace(
      new RegExp(`^(\\s*${name}:\\s*)file:\\S+$`, 'm'),
      `$1file:${pkgRef[rel]}/${tarballs[name]}`,
    );
  }
  writeFileSync(path, text);
  log(`rewrote ${rel}`);
};
rewriteJson('console/package.json');
rewriteJson('console/packages/device-ui/package.json');
rewriteJson('mobile/package.json');
rewriteWorkspaceYaml('console/pnpm-workspace.yaml');
rewriteWorkspaceYaml('mobile/pnpm-workspace.yaml');

// --- 6. temporary Dockerfile patch -------------------------------------------
// The committed Dockerfile stays untouched; this line is reverted by
// --restore. Without it the tarballs are not in the image when the early
// `pnpm install --frozen-lockfile` layer runs (that layer only COPYs
// package.json / pnpm-lock.yaml / pnpm-workspace.yaml).
const dockerfilePath = resolve(consoleRoot, 'Dockerfile');
const dockerfile = readFileSync(dockerfilePath, 'utf8');
if (!dockerfile.includes('.pkg')) {
  const anchor = 'COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./';
  if (!dockerfile.includes(anchor)) {
    console.error('[prepare-jcode-ui] Dockerfile COPY anchor not found');
    process.exit(1);
  }
  writeFileSync(
    dockerfilePath,
    dockerfile.replace(
      anchor,
      `${anchor}\n# Temporary (added by scripts/prepare-jcode-ui.mjs, reverted by --restore):\n# M14 transition — jcode-ui tarballs must exist for the install layer.\nCOPY .pkg ./.pkg`,
    ),
  );
  log('patched console/Dockerfile (temporary COPY .pkg line)');
}

// --- 7. regenerate both lockfiles --------------------------------------------
// NOTE: plain `pnpm install` becomes frozen automatically when CI=true, which
// would compare the stale lockfile against the just-rewritten overrides and
// bail. These two installs MUST regenerate the lockfile, so opt out explicitly.
run('pnpm', ['install', '--no-frozen-lockfile'], consoleRoot);
run('pnpm', ['install', '--no-frozen-lockfile'], mobileRoot);
log('done — .pkg state ready; build the image, then run with --restore');
