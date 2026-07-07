package domain

import (
	"sort"
	"strings"
)

// The runner Job environment is a contract between the reconciler (jobEnv, which
// constructs the system variables) and runner/entrypoint.sh (which reads them).
// A project's injected_env guardrail lets an owner add EXTRA variables to that
// environment (e.g. a CI flag, a proxy host), but it must never be able to
// override a system variable OR hijack how the runner executes its tools. So the
// keys below are RESERVED: the PATCH /projects API rejects them up-front (a typed
// 400 naming the key) and jobEnv drops (and log.Warns) any that slipped through a
// stale/legacy row — double insurance (CLAUDE.md red line #1).
//
// Two threat classes are reserved:
//
//  1. Contract overrides — swapping RUN_TOKEN / MODEL_NAME would break auth or the
//     fail-visible model gate.
//  2. Execution hijack — the runner invokes git / jcode / orchclient BY NAME
//     (orchclient carries the per-run RUN_TOKEN). PATH, the dynamic-linker
//     preload/library vars (LD_*, DYLD_*), and interpreter bootstrap vars
//     (BASH_ENV, NODE_OPTIONS, PYTHONSTARTUP, …) could redirect or wrap those
//     binaries, exfiltrating the token or tampering with the diff/bundle.
//
// Whole NAMESPACES (prefixes) are reserved, not just the exact keys in use today,
// so a future variable in one of these families is automatically protected.
//
// SOURCE OF TRUTH: ReservedEnvPrefixes + ReservedEnvKeys below. The console
// mirrors this in console/src/lib/env.ts; the two are pinned together by a golden
// file (orchestrator/internal/domain/testdata/reserved_env.txt) that BOTH test
// suites compare against — change one side without the other and a test goes red.

// ReservedEnvPrefixes are the namespaces the orchestrator owns. Any injected_env
// key starting with one of these is refused.
//
//	RUN_    RUN_ID, RUN_TOKEN, RUN_KIND, RUN_TIMEOUT
//	MODEL_  MODEL_BASE_URL, MODEL_API_KEY, MODEL_NAME, MODEL_PROVIDER
//	GIT_    GIT_MODE, GIT_SSH, GIT_SSH_COMMAND (+ any future GIT_*)
//	PR_     PR_HEAD, PR_BASE
//	REPO_   REPO_URL, REPO_BRANCH
//	MOCK_   MOCK_SCENARIO, MOCK_ADDR (the bundled test rig)
//	LD_     LD_PRELOAD, LD_LIBRARY_PATH, … (ELF dynamic-linker hijack)
//	DYLD_   DYLD_INSERT_LIBRARIES, … (macOS dynamic-linker hijack)
var ReservedEnvPrefixes = []string{"RUN_", "MODEL_", "GIT_", "PR_", "REPO_", "MOCK_", "LD_", "DYLD_"}

// ReservedEnvKeys are the exact reserved variables outside the prefixes above:
// the orchestrator↔runner contract keys, everything runner/entrypoint.sh
// consumes, and the interpreter/shell execution-hijack vectors.
var ReservedEnvKeys = []string{
	// orchestrator↔runner contract (jobEnv + entrypoint.sh)
	"ORCH_BASE_URL", "TASK_PROMPT", "TASK", "SOURCE_MODE", "BASE_BRANCH",
	"BRANCH_NAME", "START_MOCKLLM", "WORKSPACE", "OUT_DIR", "HOME",
	// execution-hijack vectors — the runner calls git/jcode/orchclient by name.
	"PATH", "NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP",
	"BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "IFS", "PERL5LIB", "RUBYOPT",
}

// reservedEnvKeySet is the O(1) lookup derived from ReservedEnvKeys.
var reservedEnvKeySet = func() map[string]bool {
	m := make(map[string]bool, len(ReservedEnvKeys))
	for _, k := range ReservedEnvKeys {
		m[k] = true
	}
	return m
}()

// IsReservedEnvKey reports whether key belongs to the orchestrator↔runner env
// contract (or is an execution-hijack vector) and therefore may NOT be set via a
// project's injected_env guardrail. The comparison is case-insensitive (and trims
// surrounding space) so a variant like "run_token" / "Path" cannot sneak past —
// shells are case-sensitive, but reserving broadly is the fail-visible choice.
func IsReservedEnvKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if reservedEnvKeySet[k] {
		return true
	}
	for _, p := range ReservedEnvPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// ReservedEnvGolden is the canonical, sorted, data-only serialization of the
// reserved set: one line per entry, all "prefix\tX" lines (sorted) then all
// "key\tY" lines (sorted). It is the parity fixture the Go golden test and the
// console's env.test.ts both compare against — keeping it a pure function lets
// both a test and a generator reproduce it byte-for-byte.
func ReservedEnvGolden() string {
	prefixes := append([]string(nil), ReservedEnvPrefixes...)
	keys := append([]string(nil), ReservedEnvKeys...)
	sort.Strings(prefixes)
	sort.Strings(keys)
	var b strings.Builder
	for _, p := range prefixes {
		b.WriteString("prefix\t")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	for _, k := range keys {
		b.WriteString("key\t")
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return b.String()
}

// ValidEnvKey reports whether key is a syntactically valid environment-variable
// name: non-empty, and every rune is A-Z / a-z / 0-9 / underscore with a
// non-digit first rune. A malformed key would otherwise be rejected by
// Kubernetes at Job creation and fail the run in a confusing way, so injected_env
// keys are validated at the PATCH API where the message is actionable.
func ValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			// always allowed
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // must not start with a digit
			}
		default:
			return false
		}
	}
	return true
}
