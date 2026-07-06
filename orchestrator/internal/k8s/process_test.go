package k8s

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeDocker writes a stub `docker` script that emulates just enough of the CLI
// for ProcessLauncher: it records `run`/`rm` invocations and answers `inspect`
// from a state file the test controls.
func fakeDocker(t *testing.T) (dockerPath, stateFile, logFile string) {
	t.Helper()
	dir := t.TempDir()
	stateFile = filepath.Join(dir, "state")   // holds "status exitcode" or "MISSING"
	logFile = filepath.Join(dir, "calls.log") // records subcommands
	dockerPath = filepath.Join(dir, "docker")

	script := `#!/usr/bin/env bash
echo "$1" >> "` + logFile + `"
case "$1" in
  run)  echo "container-id-123"; exit 0 ;;
  rm)   exit 0 ;;
  inspect)
    st="$(cat "` + stateFile + `" 2>/dev/null || echo MISSING)"
    if [ "$st" = "MISSING" ]; then
      echo "Error: No such object: x" >&2
      exit 1
    fi
    echo "$st"
    exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Start MISSING.
	if err := os.WriteFile(stateFile, []byte("MISSING"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dockerPath, stateFile, logFile
}

func setState(t *testing.T, stateFile, v string) {
	t.Helper()
	if err := os.WriteFile(stateFile, []byte(v), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLauncherLifecycle(t *testing.T) {
	ctx := context.Background()
	dockerPath, stateFile, logFile := fakeDocker(t)
	p := NewProcessLauncher(ProcessConfig{Image: "runner:test", Docker: dockerPath})

	spec := JobSpec{Name: "jcloud-run-abc", RunID: "abc", Env: map[string]string{"RUN_ID": "abc"}}

	// Missing before create.
	if st, _ := p.GetJobState(ctx, spec.Name); st != JobMissing {
		t.Fatalf("pre-create state = %v want missing", st)
	}

	// Create -> docker run.
	if err := p.CreateJob(ctx, spec); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate running.
	setState(t, stateFile, "running 0")
	if st, _ := p.GetJobState(ctx, spec.Name); st != JobRunning {
		t.Fatalf("running state = %v want running", st)
	}

	// Idempotency: create again while it exists must NOT docker run a second time.
	if err := p.CreateJob(ctx, spec); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	logBytes, _ := os.ReadFile(logFile)
	if got := countLines(string(logBytes), "run"); got != 1 {
		t.Fatalf("docker run invoked %d times want 1 (idempotent)", got)
	}

	// Exited 0 -> succeeded.
	setState(t, stateFile, "exited 0")
	if st, _ := p.GetJobState(ctx, spec.Name); st != JobSucceeded {
		t.Fatalf("exit0 state = %v want succeeded", st)
	}

	// Exited non-zero -> failed.
	setState(t, stateFile, "exited 1")
	if st, _ := p.GetJobState(ctx, spec.Name); st != JobFailed {
		t.Fatalf("exit1 state = %v want failed", st)
	}

	// Delete.
	if err := p.DeleteJob(ctx, spec.Name); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func countLines(s, want string) int {
	n := 0
	for _, line := range splitLines(s) {
		if line == want {
			n++
		}
	}
	return n
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
