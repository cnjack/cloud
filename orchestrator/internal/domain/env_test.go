package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsReservedEnvKey(t *testing.T) {
	reserved := []string{
		// exact keys jobEnv sets
		"RUN_ID", "RUN_TOKEN", "RUN_KIND", "RUN_TIMEOUT",
		"ORCH_BASE_URL", "TASK_PROMPT", "TASK",
		"MODEL_BASE_URL", "MODEL_API_KEY", "MODEL_NAME", "MODEL_PROVIDER",
		"BASE_BRANCH", "SOURCE_MODE", "REPO_URL", "REPO_BRANCH",
		"GIT_MODE", "BRANCH_NAME", "PR_HEAD", "PR_BASE",
		// entrypoint-only / rig keys
		"START_MOCKLLM", "MOCK_SCENARIO", "WORKSPACE", "OUT_DIR", "HOME",
		// execution-hijack vectors (must be refused — the runner calls tools by name)
		"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP", "BASH_ENV", "ENV",
		"SHELLOPTS", "BASHOPTS", "IFS", "PERL5LIB", "RUBYOPT",
		"GIT_SSH", "GIT_SSH_COMMAND",
		// case-insensitive + whitespace variants must still be caught
		"run_token", "Model_Name", "  GIT_MODE  ", "path", "Ld_Preload",
		// future keys in a reserved namespace are covered by the prefix
		"GIT_ANYTHING", "PR_FUTURE", "MODEL_SOMETHING_NEW", "LD_FUTURE", "DYLD_FUTURE",
	}
	for _, k := range reserved {
		if !IsReservedEnvKey(k) {
			t.Errorf("IsReservedEnvKey(%q) = false, want true (reserved)", k)
		}
	}

	allowed := []string{
		"FOO", "MY_FLAG", "HTTP_PROXY", "NO_PROXY", "CI",
		"COMPANY_TOKEN", "FEATURE_X", "RUNNER",       // "RUNNER" is not "RUN_"
		"REPORT_DIR", "PROMETHEUS_URL", "PRETTY_FLAG", // do not match PR_ / REPO_
		"LDAP_URL", "DYLAN", // "LDAP_" is not "LD_"; "DYLAN" is not "DYLD_"
	}
	for _, k := range allowed {
		if IsReservedEnvKey(k) {
			t.Errorf("IsReservedEnvKey(%q) = true, want false (user key)", k)
		}
	}
}

func TestValidEnvKey(t *testing.T) {
	valid := []string{"FOO", "_x", "A1", "MY_FLAG_2", "lower_ok"}
	for _, k := range valid {
		if !ValidEnvKey(k) {
			t.Errorf("ValidEnvKey(%q) = false, want true", k)
		}
	}
	invalid := []string{"", "1FOO", "FOO BAR", "FOO=BAR", "FOO-BAR", "föö"}
	for _, k := range invalid {
		if ValidEnvKey(k) {
			t.Errorf("ValidEnvKey(%q) = true, want false", k)
		}
	}
}

// TestReservedEnvGolden pins the reserved set to a checked-in fixture that the
// console's env.test.ts reads too — so changing the Go source of truth without
// updating the fixture (and thereby the console) turns a test red on BOTH sides.
// Run `UPDATE_GOLDEN=1 go test ./internal/domain/` to regenerate after an
// intentional change (then mirror it in console/src/lib/env.ts).
func TestReservedEnvGolden(t *testing.T) {
	path := filepath.Join("testdata", "reserved_env.txt")
	want := ReservedEnvGolden()

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with UPDATE_GOLDEN=1)", path, err)
	}
	if string(got) != want {
		t.Fatalf("reserved-env golden is stale.\n--- want ---\n%s\n--- got ---\n%s\nregenerate with UPDATE_GOLDEN=1 and mirror console/src/lib/env.ts", want, got)
	}
}
