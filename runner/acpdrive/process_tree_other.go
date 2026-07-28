//go:build !linux && !darwin

package main

import "os/exec"

// Non-Unix fallback. Production runners are Linux; this keeps local builds on
// other platforms working while retaining the direct-child behavior there.
func configureProcessTree(_ *exec.Cmd) {}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
