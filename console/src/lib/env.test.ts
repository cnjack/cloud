import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  isReservedEnvKey,
  isValidEnvKey,
  RESERVED_ENV_KEYS,
  RESERVED_ENV_PREFIXES,
} from './env';

describe('isReservedEnvKey (mirrors orchestrator/internal/domain/env.go)', () => {
  it('reserves the system contract keys + namespaces (case-insensitive)', () => {
    for (const k of [
      'RUN_ID',
      'RUN_TOKEN',
      'RUN_TIMEOUT',
      'MODEL_NAME',
      'MODEL_API_KEY',
      'GIT_MODE',
      'PR_HEAD',
      'REPO_URL',
      'ORCH_BASE_URL',
      'TASK_PROMPT',
      'BASE_BRANCH',
      'BRANCH_NAME',
      'WORKSPACE',
      'HOME',
      // execution-hijack vectors
      'PATH',
      'LD_PRELOAD',
      'DYLD_INSERT_LIBRARIES',
      'NODE_OPTIONS',
      'IFS',
      'run_token', // case-insensitive
      'path',
      'GIT_FUTURE', // prefix covers future keys
    ]) {
      expect(isReservedEnvKey(k)).toBe(true);
    }
  });

  it('allows ordinary user keys', () => {
    for (const k of ['FOO', 'MY_FLAG', 'HTTP_PROXY', 'COMPANY_TOKEN', 'RUNNER', 'REPORT_DIR', 'LDAP_URL']) {
      expect(isReservedEnvKey(k)).toBe(false);
    }
  });
});

describe('isValidEnvKey', () => {
  it('accepts valid names and rejects malformed ones', () => {
    for (const k of ['FOO', '_x', 'A1', 'lower_ok']) expect(isValidEnvKey(k)).toBe(true);
    for (const k of ['', '1FOO', 'FOO BAR', 'FOO=BAR', 'FOO-BAR']) expect(isValidEnvKey(k)).toBe(false);
  });
});

// Cross-language parity: the console reserved set must byte-for-byte match the Go
// source of truth. Both build the same canonical text; this test reads the Go
// golden fixture. Editing one side without the other turns this test red (and the
// Go golden test too). Regenerate with `UPDATE_GOLDEN=1 go test ./internal/domain/`.
describe('reserved-env parity with the Go golden fixture', () => {
  it('matches orchestrator/internal/domain/testdata/reserved_env.txt', () => {
    // vitest runs with cwd = the console package root; the orchestrator is a
    // sibling directory in the monorepo.
    const goldenPath = resolve(
      process.cwd(),
      '../orchestrator/internal/domain/testdata/reserved_env.txt',
    );
    const golden = readFileSync(goldenPath, 'utf8');

    const lines: string[] = [];
    for (const p of [...RESERVED_ENV_PREFIXES].sort()) lines.push(`prefix\t${p}`);
    for (const k of [...RESERVED_ENV_KEYS].sort()) lines.push(`key\t${k}`);
    const canonical = lines.join('\n') + '\n';

    expect(canonical).toBe(golden);
  });
});
