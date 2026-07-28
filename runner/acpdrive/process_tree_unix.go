//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const processTreeKillGrace = 2 * time.Second

// configureProcessTree gives the agent its own process group and replaces
// os/exec's direct-child-only cancellation. Tools spawned by the agent inherit
// this group, so a timeout or user cancellation reaches the entire runner tree.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := signalProcessTree(cmd, syscall.SIGTERM)
		if err != nil {
			return err
		}
		pid := cmd.Process.Pid
		go func() {
			time.Sleep(processTreeKillGrace)
			// Best-effort escalation for a tool that ignored SIGTERM.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
		return nil
	}
	cmd.WaitDelay = processTreeKillGrace + time.Second
}

// terminateProcessTree is the final reap path used by both single-turn and
// session runners. SIGKILL is intentional here: graceful stdin closure is
// registered after this defer and therefore runs first.
func terminateProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

func signalProcessTree(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
