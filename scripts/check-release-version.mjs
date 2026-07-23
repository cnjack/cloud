import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const version = read('VERSION').trim();

if (!/^\d+\.\d+\.\d+$/.test(version)) {
  throw new Error(`VERSION must be X.Y.Z, got ${JSON.stringify(version)}`);
}

for (const file of [
  'console/package.json',
  'mobile/package.json',
  'mobile/src-tauri/tauri.conf.json',
]) {
  const actual = JSON.parse(read(file)).version;
  if (actual !== version) {
    throw new Error(`${file}: version ${JSON.stringify(actual)} != VERSION ${version}`);
  }
}

const escaped = version.replaceAll('.', '\\.');
const tagged = `v${version}`.replaceAll('.', '\\.');
const checks = [
  ['mobile/src-tauri/Cargo.toml', new RegExp(`^version = "${escaped}"$`, 'm')],
  [
    'mobile/src-tauri/Cargo.lock',
    new RegExp(`name = "jcloud-mobile"\\nversion = "${escaped}"`),
  ],
  [
    'deploy/overlays/company/kustomization.yaml',
    new RegExp(`newTag: v${escaped}`),
  ],
  [
    'deploy/overlays/company/kustomization.yaml',
    new RegExp(`jcloud-runner:${tagged}`),
  ],
  [
    'deploy/overlays/company/console.yaml',
    new RegExp(`jcloud-console:${tagged}`),
  ],
];

for (const [file, pattern] of checks) {
  if (!pattern.test(read(file))) {
    throw new Error(`${file}: release version does not match VERSION ${version}`);
  }
}

process.stdout.write(`${version}\n`);
