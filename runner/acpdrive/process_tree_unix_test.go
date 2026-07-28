//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextCancellationTerminatesProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 60 & echo $! > "$1"; wait`, "sh", pidFile)
	configureProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process tree: %v", err)
	}
	err := cmd.Wait()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded (wait err %v)", ctx.Err(), err)
	}

	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read child pid: %v", readErr)
	}
	childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil {
		t.Fatalf("parse child pid: %v", parseErr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		probeErr := syscall.Kill(childPID, 0)
		if errors.Is(probeErr, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived context cancellation (probe error %v)", childPID, probeErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
