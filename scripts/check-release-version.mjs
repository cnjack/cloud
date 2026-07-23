import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const version = read('VERSION').trim();

if (!/^\d+\.\d+\.\d+$/.test(version)) {
  throw new Error(`VERSION must be X.Y.Z, got ${JSON.stringify(version)}`);
}

const [major, minor, patch] = version.split('.').map(Number);
// Android previously shipped 0.1.0 with the semver-derived code 1000. Keep
// human-facing versionName aligned with the Git tag while guaranteeing every
// release remains a normal upgrade from that historical build.
const androidVersionCode = 1_000_000 + major * 1_000_000 + minor * 1_000 + patch;

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

const tauriConfig = JSON.parse(read('mobile/src-tauri/tauri.conf.json'));
if (tauriConfig.bundle?.android?.versionCode !== androidVersionCode) {
  throw new Error(
    `mobile/src-tauri/tauri.conf.json: Android versionCode ` +
      `${JSON.stringify(tauriConfig.bundle?.android?.versionCode)} != ${androidVersionCode}`,
  );
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
