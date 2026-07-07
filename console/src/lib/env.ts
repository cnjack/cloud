/*
 * env.ts — the injected_env reserved-key + validity contract, mirrored from the
 * orchestrator (orchestrator/internal/domain/env.go). The console validates
 * inline so an owner sees the problem before saving; the server enforces the same
 * rules and returns a typed 400 as the backstop.
 *
 * SOURCE OF TRUTH lives in Go (domain/env.go). These two arrays MUST stay in sync
 * with ReservedEnvPrefixes / ReservedEnvKeys there. That is enforced, not just
 * asked for: env.test.ts compares these arrays against the Go golden fixture
 * (orchestrator/internal/domain/testdata/reserved_env.txt) — change one side
 * without the other and env.test.ts (and the Go golden test) go red. When you
 * edit these, run `UPDATE_GOLDEN=1 go test ./internal/domain/` in the orchestrator
 * and mirror the change here.
 */

/**
 * Reserved namespaces (prefixes): the orchestrator↔runner contract families plus
 * the dynamic-linker hijack vectors (LD_*, DYLD_*).
 */
export const RESERVED_ENV_PREFIXES = [
  'RUN_',
  'MODEL_',
  'GIT_',
  'PR_',
  'REPO_',
  'MOCK_',
  'LD_',
  'DYLD_',
];

/**
 * Reserved exact keys outside the prefixes: the contract keys, everything
 * entrypoint.sh consumes, and the interpreter/shell execution-hijack vectors (the
 * runner invokes git/jcode/orchclient by name; orchclient holds the RUN_TOKEN).
 */
export const RESERVED_ENV_KEYS = [
  // orchestrator↔runner contract
  'ORCH_BASE_URL',
  'TASK_PROMPT',
  'TASK',
  'SOURCE_MODE',
  'BASE_BRANCH',
  'BRANCH_NAME',
  'START_MOCKLLM',
  'WORKSPACE',
  'OUT_DIR',
  'HOME',
  // execution-hijack vectors
  'PATH',
  'NODE_OPTIONS',
  'PYTHONPATH',
  'PYTHONSTARTUP',
  'BASH_ENV',
  'ENV',
  'SHELLOPTS',
  'BASHOPTS',
  'IFS',
  'PERL5LIB',
  'RUBYOPT',
];

const RESERVED_ENV_KEY_SET = new Set(RESERVED_ENV_KEYS);

/**
 * isReservedEnvKey reports whether key is part of the system env contract (or an
 * execution-hijack vector) and so may not be set via a project's injected_env.
 * Case-insensitive (matches the Go side), so "run_token"/"path" are caught too.
 */
export function isReservedEnvKey(key: string): boolean {
  const k = key.trim().toUpperCase();
  if (RESERVED_ENV_KEY_SET.has(k)) return true;
  return RESERVED_ENV_PREFIXES.some((p) => k.startsWith(p));
}

/**
 * isValidEnvKey reports whether key is a syntactically valid env-var name:
 * non-empty, [A-Za-z_][A-Za-z0-9_]* (no leading digit).
 */
export function isValidEnvKey(key: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(key);
}
