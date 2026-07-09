package k8s

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ProcessLauncher is a JobLauncher that runs each runner as a local `docker run`
// container instead of a Kubernetes Job. It exists for local development and the
// full-loop integration test (runner/test-integration.sh): the whole pipeline —
// REST create → runner executes → events stream → artifact lands → run
// succeeds — can be exercised on a laptop with no cluster.
//
// It deliberately reuses the SAME JobSpec (name, env, timeout) the reconciler
// builds for Kubernetes, so the process path stays faithful to production env
// injection. The container is named after the deterministic Job name, which
// makes CreateJob idempotent (a re-create of an existing container is a no-op)
// exactly like the Kubernetes launcher's AlreadyExists handling.
type ProcessLauncher struct {
	image       string   // runner image to run
	network     string   // optional docker network to attach (e.g. for a mockllm sidecar)
	extraArgs   []string // optional extra `docker run` args (e.g. --add-host)
	docker      string   // docker binary (default "docker")
	labelPrefix string   // container label key prefix for run id
}

// ProcessConfig configures a ProcessLauncher.
type ProcessConfig struct {
	Image     string
	Network   string
	ExtraArgs []string
	Docker    string
}

// NewProcessLauncher builds a ProcessLauncher.
func NewProcessLauncher(cfg ProcessConfig) *ProcessLauncher {
	docker := cfg.Docker
	if docker == "" {
		docker = "docker"
	}
	return &ProcessLauncher{
		image:       cfg.Image,
		network:     cfg.Network,
		extraArgs:   cfg.ExtraArgs,
		docker:      docker,
		labelPrefix: "jcloud.run-id",
	}
}

// CreateJob starts a detached runner container named spec.Name. Idempotent: if a
// container with that name already exists (running or exited) it returns nil.
func (p *ProcessLauncher) CreateJob(ctx context.Context, spec JobSpec) error {
	// Idempotency: skip if a container with this name already exists. Propagate a
	// transient inspect error instead of swallowing it — otherwise a docker
	// hiccup (JobUnknown, err) would be treated as "exists" and we'd return nil
	// WITHOUT starting a container, and the reconciler would persist scheduling
	// with a Job that never came up and then permanently fail the run. Returning
	// the error makes the reconciler retry the same run next tick.
	state, err := p.inspectState(ctx, spec.Name)
	if err != nil {
		return fmt.Errorf("inspect before create %s: %w", spec.Name, err)
	}
	if state != JobMissing {
		return nil
	}

	args := []string{"run", "-d", "--name", spec.Name, "--label", p.labelPrefix + "=" + spec.RunID}
	if p.network != "" {
		args = append(args, "--network", p.network)
	}
	args = append(args, p.extraArgs...)
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, p.image)

	cmd := exec.CommandContext(ctx, p.docker, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run %s: %v: %s", spec.Name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// GetJobState maps the container's docker state to a JobState.
func (p *ProcessLauncher) GetJobState(ctx context.Context, name string) (JobState, error) {
	return p.inspectState(ctx, name)
}

// EnsureWorkspacePVC is a no-op for the process launcher: PVCs are a Kubernetes
// concept. A local `docker run` container has no persistent per-service volume,
// so with PERSISTENT_WORKSPACE on the process path simply clones fresh each run
// (the entrypoint falls back when /workspace is empty). Persistence is exercised
// on the real k8s launcher.
func (p *ProcessLauncher) EnsureWorkspacePVC(_ context.Context, _, _ string) error {
	return nil
}

// DeleteWorkspacePVC is a no-op for the process launcher (see EnsureWorkspacePVC).
func (p *ProcessLauncher) DeleteWorkspacePVC(_ context.Context, _ string) error {
	return nil
}

// WorkspacePVCExists is always false for the process launcher: it has no PVCs,
// so a service is never an archive candidate on the local/process path (F10).
func (p *ProcessLauncher) WorkspacePVCExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// DeleteJob force-removes the container. Removing a missing container is not an
// error.
func (p *ProcessLauncher) DeleteJob(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, p.docker, "rm", "-f", name)
	// Ignore errors: a missing container is fine.
	_ = cmd.Run()
	return nil
}

// inspectState runs `docker inspect` and classifies the container. Returns
// JobMissing when the container does not exist.
func (p *ProcessLauncher) inspectState(ctx context.Context, name string) (JobState, error) {
	// {{.State.Status}} => created|running|exited|dead ; {{.State.ExitCode}}
	cmd := exec.CommandContext(ctx, p.docker, "inspect",
		"-f", "{{.State.Status}} {{.State.ExitCode}}", name)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// "No such object" => container doesn't exist.
		if strings.Contains(stderr.String(), "No such object") ||
			strings.Contains(stderr.String(), "no such") {
			return JobMissing, nil
		}
		return JobUnknown, fmt.Errorf("docker inspect %s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(strings.TrimSpace(out.String()))
	if len(fields) < 1 {
		return JobUnknown, nil
	}
	status := fields[0]
	exitCode := "0"
	if len(fields) >= 2 {
		exitCode = fields[1]
	}
	switch status {
	case "created":
		return JobPending, nil
	case "running", "paused", "restarting":
		return JobRunning, nil
	case "exited", "dead":
		if exitCode == "0" {
			return JobSucceeded, nil
		}
		return JobFailed, nil
	default:
		return JobUnknown, nil
	}
}

var _ JobLauncher = (*ProcessLauncher)(nil)
