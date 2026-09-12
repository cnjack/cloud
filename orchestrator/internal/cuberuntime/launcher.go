// Package cuberuntime implements the Cloud runner contract on CubeSandbox.
package cuberuntime

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/k8s"
)

type Objects interface {
	PresignPut(string, time.Duration) (string, error)
	PresignGet(string, time.Duration) (string, error)
	Delete(context.Context, string) error
}
type Config struct {
	Owner, DefaultImage, PluginBinary, PluginAssets string
	HTTPProxy, HTTPSProxy, NoProxy                  string
	// Templates maps resolved administrator-owned image references to template IDs.
	Templates map[string]string
}
type Launcher struct {
	cfg      Config
	backend  Backend
	registry Registry
	objects  Objects
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func New(cfg Config, backend Backend, registry Registry, objects Objects) (*Launcher, error) {
	if !identifier.MatchString(cfg.Owner) || backend == nil || registry == nil {
		return nil, errors.New("CubeSandbox owner, backend and durable registry are required")
	}
	if cfg.Templates[cfg.DefaultImage] == "" {
		return nil, errors.New("CubeSandbox default runner template is not configured")
	}
	if cfg.PluginBinary == "" {
		cfg.PluginBinary = "/plugin-runtime"
	}
	if cfg.PluginAssets == "" {
		cfg.PluginAssets = "/opt/jcloud/plugin-runtime"
	}
	return &Launcher{cfg: cfg, backend: backend, registry: registry, objects: objects}, nil
}
func (l *Launcher) CreateJob(ctx context.Context, spec k8s.JobSpec) error {
	if !identifier.MatchString(spec.Name) || !identifier.MatchString(spec.ServiceID) {
		return errors.New("CubeSandbox job and service identities are required")
	}
	if spec.TimeoutSeconds <= 0 {
		return errors.New("CubeSandbox jobs require a positive hard timeout")
	}
	unlock, err := l.registry.Lock(ctx, spec.Name)
	if err != nil {
		return err
	}
	defer unlock()
	image := spec.Image
	if image == "" {
		image = l.cfg.DefaultImage
	}
	template := l.cfg.Templates[image]
	if template == "" {
		return errors.New("cubesandbox_template_not_configured: configure a template for this runner profile")
	}
	j, err := l.registry.Job(ctx, spec.Name)
	if err != nil {
		return err
	}
	if j == nil {
		j = &Job{Name: spec.Name, RunID: spec.RunID, ServiceID: spec.ServiceID, CreatedAt: time.Now().UTC(), TimeoutSeconds: spec.TimeoutSeconds}
		if spec.WorkspacePVC != "" && spec.Env["RUN_ARCHIVE"] != "1" {
			j.CheckpointKey = "cubesandbox/" + l.cfg.Owner + "/checkpoints/" + spec.ServiceID + "/" + spec.Name + ".tar.gz"
		}
		if err := l.registry.SaveJob(ctx, j); err != nil {
			return err
		}
	}
	if j.CleanedAt != nil {
		return errors.New("CubeSandbox job was already cleaned; create a new Run")
	}
	vm, err := l.findVM(ctx, j)
	if err != nil {
		return err
	}
	if vm == nil {
		if j.SandboxID != "" {
			return errors.New("CubeSandbox disappeared after dispatch; retry as a new Run")
		}
		vm, err = l.backend.Create(ctx, template, map[string]string{"jcloud.owner": l.cfg.Owner, "jcloud.job": spec.Name, "jcloud.service": spec.ServiceID, "jcloud.project": spec.ProjectID})
		if err != nil {
			return fmt.Errorf("create CubeSandbox (cleanup will reconcile ownership): %w", err)
		}
		j.SandboxID = vm.ID()
		if err := l.registry.SaveJob(ctx, j); err != nil {
			return err
		}
	}
	// Never bootstrap over an execution which already started, including a
	// response lost after starting the guest supervisor.
	state, err := guestState(ctx, vm)
	if err != nil {
		return err
	}
	if state.phase != "pending" {
		return nil
	}
	var restore string
	if spec.WorkspacePVC != "" {
		key, _, err := l.registry.Workspace(ctx, spec.ServiceID)
		if err != nil {
			return err
		}
		if key != "" {
			if l.objects == nil {
				return errors.New("CubeSandbox checkpoint storage is unavailable")
			}
			restore, err = l.objects.PresignGet(key, time.Hour)
			if err != nil {
				return err
			}
		}
	}
	return l.bootstrap(ctx, vm, spec, restore)
}

// findVM adopts an ambiguous create using metadata and never guesses between
// multiple resources. A persisted ID is authoritative after restarts.
func (l *Launcher) findVM(ctx context.Context, j *Job) (VM, error) {
	ids, err := l.backend.Find(ctx, l.cfg.Owner, j.Name)
	if err != nil {
		return nil, err
	}
	if len(ids) > 1 {
		return nil, errors.New("multiple CubeSandbox resources claim this Run; operator reconciliation required")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if j.SandboxID != "" && j.SandboxID != ids[0] {
		return nil, errors.New("CubeSandbox identity changed unexpectedly")
	}
	if j.SandboxID == "" {
		j.SandboxID = ids[0]
		if err := l.registry.SaveJob(ctx, j); err != nil {
			return nil, err
		}
	}
	return l.backend.Connect(ctx, ids[0])
}

type guestStatus struct {
	phase string
	exit  int
}

func guestState(ctx context.Context, vm VM) (guestStatus, error) {
	out, err := vm.Run(ctx, `if [ -f /run/jcloud/control/state ]; then cat /run/jcloud/control/state; elif [ -f /run/jcloud/control/started ]; then echo running; else echo pending; fi; if [ -f /run/jcloud/control/exit-code ]; then cat /run/jcloud/control/exit-code; else echo 0; fi`, 20*time.Second)
	if err != nil {
		return guestStatus{}, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return guestStatus{}, errors.New("invalid CubeSandbox execution receipt")
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return guestStatus{}, errors.New("invalid CubeSandbox exit receipt")
	}
	return guestStatus{fields[0], code}, nil
}
func (l *Launcher) GetJobState(ctx context.Context, name string) (k8s.JobState, error) {
	unlock, err := l.registry.Lock(ctx, name)
	if err != nil {
		return k8s.JobUnknown, err
	}
	defer unlock()
	j, err := l.registry.Job(ctx, name)
	if err != nil {
		return k8s.JobUnknown, err
	}
	if j == nil || j.CleanedAt != nil {
		return k8s.JobMissing, nil
	}
	vm, err := l.findVM(ctx, j)
	if err != nil {
		return k8s.JobUnknown, err
	}
	if vm == nil {
		return k8s.JobMissing, nil
	}
	s, err := guestState(ctx, vm)
	if err != nil {
		return k8s.JobUnknown, err
	}
	if s.phase == "checkpoint_failed" {
		return k8s.JobCheckpointFailed, nil
	}
	if s.phase == "running" && j.TimeoutSeconds > 0 && time.Now().After(j.CreatedAt.Add(time.Duration(j.TimeoutSeconds)*time.Second)) {
		_, err := vm.Run(ctx, `touch /run/jcloud/control/deadline-exceeded; if [ -f /run/jcloud/control/child-pid ]; then kill -TERM -- "-$(cat /run/jcloud/control/child-pid)" 2>/dev/null || true; fi`, 20*time.Second)
		return k8s.JobRunning, err
	}
	if s.phase == "exited" {
		if err := l.startCheckpoint(ctx, j, vm); err != nil {
			return k8s.JobUnknown, err
		}
		return k8s.JobRunning, nil
	}
	if s.phase == "complete" {
		if err := l.saveCheckpoint(ctx, j); err != nil {
			return k8s.JobUnknown, err
		}
		if s.exit == 124 || s.exit == 137 {
			return k8s.JobDeadlineExceeded, nil
		}
		if s.exit != 0 {
			return k8s.JobFailed, nil
		}
		return k8s.JobSucceeded, nil
	}
	switch s.phase {
	case "pending":
		return k8s.JobPending, nil
	case "running", "stopping", "checkpointing":
		return k8s.JobRunning, nil
	default:
		return k8s.JobUnknown, fmt.Errorf("unknown CubeSandbox phase %q", s.phase)
	}
}

func (l *Launcher) DeleteJob(ctx context.Context, name string) error {
	unlock, err := l.registry.Lock(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	j, err := l.registry.Job(ctx, name)
	if err != nil {
		return err
	}
	if j == nil {
		return nil
	}
	if j.CleanedAt != nil {
		return nil
	}
	vm, err := l.findVM(ctx, j)
	if err != nil {
		return err
	}
	if vm != nil {
		s, err := guestState(ctx, vm)
		if err != nil {
			return err
		}
		switch s.phase {
		case "running", "stopping":
			_, err := vm.Run(ctx, `if [ -f /run/jcloud/control/child-pid ]; then kill -TERM -- "-$(cat /run/jcloud/control/child-pid)" 2>/dev/null || true; fi`, 20*time.Second)
			if err != nil {
				return err
			}
			return errors.New("CubeSandbox is stopping; workspace checkpoint must finish before deletion")
		case "exited", "checkpoint_failed":
			if err := l.startCheckpoint(ctx, j, vm); err != nil {
				return err
			}
			return errors.New("CubeSandbox workspace checkpoint is pending; sandbox retained")
		case "checkpointing":
			return errors.New("CubeSandbox workspace checkpoint is uploading; sandbox retained")
		case "complete":
			if err := l.saveCheckpoint(ctx, j); err != nil {
				return err
			}
		case "pending": // Bootstrap never started execution; preserve the prior checkpoint.
		default:
			return fmt.Errorf("cannot delete CubeSandbox in phase %q", s.phase)
		}
		if err := l.backend.Delete(ctx, vm.ID()); err != nil {
			return err
		}
		ids, err := l.backend.Find(ctx, l.cfg.Owner, name)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			return errors.New("CubeSandbox deletion has not completed")
		}
	} else if j.CheckpointKey != "" && !j.CheckpointSaved && j.SandboxID != "" {
		return errors.New("CubeSandbox disappeared before its workspace was checkpointed; operator recovery required")
	}
	now := time.Now().UTC()
	j.CleanedAt = &now
	return l.registry.SaveJob(ctx, j)
}

func (l *Launcher) startCheckpoint(ctx context.Context, j *Job, vm VM) error {
	if j.CheckpointKey == "" {
		_, err := vm.Run(ctx, `echo complete > /run/jcloud/control/state`, 20*time.Second)
		return err
	}
	if l.objects == nil {
		return errors.New("CubeSandbox checkpoint storage is not configured; sandbox retained")
	}
	put, err := l.objects.PresignPut(j.CheckpointKey, time.Hour)
	if err != nil {
		return err
	}
	get, err := l.objects.PresignGet(j.CheckpointKey, time.Hour)
	if err != nil {
		return err
	}
	if err := vm.Write(ctx, "/run/jcloud/control/checkpoint.env", []byte("UPLOAD_URL="+shellQuote(put)+"\nVERIFY_URL="+shellQuote(get)+"\n")); err != nil {
		return err
	}
	_, err = vm.Run(ctx, `chmod 600 /run/jcloud/control/checkpoint.env; nohup /bin/bash /run/jcloud/control/checkpoint.sh >/run/jcloud/control/checkpoint.log 2>&1 </dev/null &`, 20*time.Second)
	return err
}
func (l *Launcher) saveCheckpoint(ctx context.Context, j *Job) error {
	if j.CheckpointKey == "" || j.CheckpointSaved {
		return nil
	}
	old, _, err := l.registry.Workspace(ctx, j.ServiceID)
	if err != nil {
		return err
	}
	if err := l.registry.SaveWorkspace(ctx, j.ServiceID, j.CheckpointKey); err != nil {
		return err
	}
	j.CheckpointSaved = true
	if err := l.registry.SaveJob(ctx, j); err != nil {
		return err
	}
	// Keep the new pointer durable before removing an obsolete checkpoint. A
	// failed removal is harmless retention; Service erasure enumerates all keys.
	if old != "" && old != j.CheckpointKey {
		_ = l.objects.Delete(ctx, old)
	}
	return nil
}

func (l *Launcher) EnsureWorkspacePVC(ctx context.Context, service, project string) error {
	if l.objects == nil {
		return errors.New("CubeSandbox persistent workspaces require object storage")
	}
	jobs, err := l.registry.PendingJobs(ctx, service)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		return errors.New("previous CubeSandbox workspace cleanup is pending")
	}
	_, exists, err := l.registry.Workspace(ctx, service)
	if err != nil || exists {
		return err
	}
	return l.registry.SaveWorkspace(ctx, service, "")
}
func (l *Launcher) WorkspacePVCExists(ctx context.Context, service string) (bool, error) {
	_, exists, err := l.registry.Workspace(ctx, service)
	return exists, err
}
func (l *Launcher) DeleteWorkspacePVC(ctx context.Context, service string) error {
	jobs, err := l.registry.AllJobs(ctx, service)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err := l.DeleteJob(ctx, j.Name); err != nil {
			return err
		}
		if j.CheckpointKey != "" {
			if l.objects == nil {
				return errors.New("checkpoint storage is unavailable")
			}
			if err := l.objects.Delete(ctx, j.CheckpointKey); err != nil {
				return err
			}
		}
	}
	key, _, err := l.registry.Workspace(ctx, service)
	if err != nil {
		return err
	}
	if key != "" {
		if l.objects == nil {
			return errors.New("checkpoint storage is unavailable")
		}
		if err := l.objects.Delete(ctx, key); err != nil {
			return err
		}
	}
	return l.registry.DeleteWorkspace(ctx, service)
}

var _ k8s.JobLauncher = (*Launcher)(nil)

func (l *Launcher) ReapOrphanedJobs(ctx context.Context) error {
	vms, err := l.backend.Owned(ctx, l.cfg.Owner)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if !identifier.MatchString(vm.JobName) || (!strings.HasPrefix(vm.JobName, "jcloud-run-") && !strings.HasPrefix(vm.JobName, "jcloud-archive-")) {
			continue
		}
		unlock, err := l.registry.Lock(ctx, vm.JobName)
		if err != nil {
			return err
		}
		job, err := l.registry.Job(ctx, vm.JobName)
		// Registry insertion precedes every create request. No live ownership
		// row means this allocation arrived after cleanup/tenant erasure.
		if err == nil && (job == nil || job.CleanedAt != nil) {
			err = l.backend.Delete(ctx, vm.ID)
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
