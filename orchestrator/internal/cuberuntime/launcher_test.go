package cuberuntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/k8s"
)

func TestTaskEnvironmentNeverEntersRootHelpers(t *testing.T) {
	l, _, b, _ := fixture(t)
	spec := testSpec()
	spec.Env["BASH_ENV"] = "/workspace/root-exploit"
	spec.Env["LD_PRELOAD"] = "/workspace/exploit.so"
	spec.Env["http_proxy"] = "http://attacker"
	spec.Env["CUBE_RESTORE_URL"] = "https://attacker"
	spec.Env["CUBE_PLUGIN_ENABLED"] = "1"
	if err := l.CreateJob(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(b.vm.writes["/run/jcloud/control/bootstrap.tgz"]))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	tr := tar.NewReader(z)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		v, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = string(v)
	}
	control := files["control/env.sh"]
	for _, forbidden := range []string{"BASH_ENV", "LD_PRELOAD", "attacker", "CUBE_PLUGIN_ENABLED"} {
		if strings.Contains(control, forbidden) {
			t.Fatalf("root helper inherited %s", forbidden)
		}
	}
	if !strings.Contains(files["config/runner-env.sh"], "BASH_ENV") {
		t.Fatal("task environment was dropped rather than confined")
	}
}

type memoryRegistry struct {
	mu         sync.Mutex
	jobs       map[string]Job
	workspaces map[string]string
}

func (r *memoryRegistry) Lock(context.Context, string) (func(), error) {
	r.mu.Lock()
	return r.mu.Unlock, nil
}
func (r *memoryRegistry) Job(_ context.Context, name string) (*Job, error) {
	v, ok := r.jobs[name]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *memoryRegistry) SaveJob(_ context.Context, j *Job) error { r.jobs[j.Name] = *j; return nil }
func (r *memoryRegistry) AllJobs(_ context.Context, service string) ([]Job, error) {
	var out []Job
	for _, j := range r.jobs {
		if j.ServiceID == service {
			out = append(out, j)
		}
	}
	return out, nil
}
func (r *memoryRegistry) PendingJobs(ctx context.Context, service string) ([]Job, error) {
	jobs, _ := r.AllJobs(ctx, service)
	var out []Job
	for _, j := range jobs {
		if j.CleanedAt == nil {
			out = append(out, j)
		}
	}
	return out, nil
}
func (r *memoryRegistry) Workspace(_ context.Context, s string) (string, bool, error) {
	v, ok := r.workspaces[s]
	return v, ok, nil
}
func (r *memoryRegistry) SaveWorkspace(_ context.Context, s, key string) error {
	r.workspaces[s] = key
	return nil
}
func (r *memoryRegistry) DeleteWorkspace(_ context.Context, s string) error {
	delete(r.workspaces, s)
	return nil
}

type testVM struct {
	id, phase     string
	exit          int
	writes        map[string][]byte
	commands      []string
	failBootstrap bool
}

func (v *testVM) ID() string                                        { return v.id }
func (v *testVM) Write(_ context.Context, p string, b []byte) error { v.writes[p] = b; return nil }
func (v *testVM) Run(_ context.Context, cmd string, _ time.Duration) (string, error) {
	v.commands = append(v.commands, cmd)
	if strings.Contains(cmd, "then cat /run/jcloud/control/state;") {
		return fmt.Sprintf("%s\n%d\n", v.phase, v.exit), nil
	}
	if strings.Contains(cmd, "/setup.sh;") {
		if v.failBootstrap {
			return "", errors.New("setup rejected")
		}
		v.phase = "running"
	}
	if strings.Contains(cmd, "nohup /bin/bash /run/jcloud/control/checkpoint.sh") {
		v.phase = "checkpointing"
	}
	if strings.Contains(cmd, "echo complete >") {
		v.phase = "complete"
	}
	return "", nil
}

type testBackend struct {
	vm                                     *testVM
	creates, deletes                       int
	ambiguousCreate, failDelete, duplicate bool
}

func (b *testBackend) Owned(context.Context, string) ([]OwnedVM, error) {
	if b.vm == nil {
		return nil, nil
	}
	return []OwnedVM{{b.vm.id, testSpec().Name}}, nil
}

func (b *testBackend) Create(context.Context, string, map[string]string) (VM, error) {
	b.creates++
	b.vm = &testVM{id: "sandbox-1", phase: "pending", writes: map[string][]byte{}}
	if b.ambiguousCreate {
		return nil, context.DeadlineExceeded
	}
	return b.vm, nil
}
func (b *testBackend) Connect(context.Context, string) (VM, error) { return b.vm, nil }
func (b *testBackend) Find(context.Context, string, string) ([]string, error) {
	if b.duplicate {
		return []string{"a", "b"}, nil
	}
	if b.vm == nil {
		return nil, nil
	}
	return []string{b.vm.id}, nil
}
func (b *testBackend) Delete(context.Context, string) error {
	b.deletes++
	if b.failDelete {
		return errors.New("provider unavailable")
	}
	b.vm = nil
	return nil
}

type testObjects struct {
	deleted    []string
	failDelete bool
}

func (o *testObjects) PresignPut(k string, _ time.Duration) (string, error) {
	return "https://storage.test/" + k + "?write=secret", nil
}
func (o *testObjects) PresignGet(k string, _ time.Duration) (string, error) {
	return "https://storage.test/" + k + "?read=secret", nil
}
func (o *testObjects) Delete(_ context.Context, k string) error {
	if o.failDelete {
		return errors.New("delete unavailable")
	}
	o.deleted = append(o.deleted, k)
	return nil
}
func fixture(t *testing.T) (*Launcher, *memoryRegistry, *testBackend, *testObjects) {
	t.Helper()
	r := &memoryRegistry{jobs: map[string]Job{}, workspaces: map[string]string{}}
	b := &testBackend{}
	o := &testObjects{}
	l, err := New(Config{Owner: "company", DefaultImage: "runner:one", Templates: map[string]string{"runner:one": "tpl-one"}}, b, r, o)
	if err != nil {
		t.Fatal(err)
	}
	return l, r, b, o
}
func testSpec() k8s.JobSpec {
	return k8s.JobSpec{Name: "jcloud-run-one", RunID: "one", ServiceID: "service", ProjectID: "project", WorkspacePVC: "ws-service", TimeoutSeconds: 60, Env: map[string]string{"RUN_ID": "one", "RUN_TOKEN": "private"}}
}

func TestCancelRetainsVMUntilVerifiedCheckpointThenSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	l, r, b, o := fixture(t)
	r.workspaces["service"] = "previous"
	if err := l.CreateJob(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	if err := l.DeleteJob(ctx, testSpec().Name); err == nil || b.deletes != 0 {
		t.Fatal("cancel must retain running VM")
	}
	if err := l.EnsureWorkspacePVC(ctx, "service", "project"); err == nil {
		t.Fatal("another run must wait for checkpoint")
	}
	b.vm.phase = "exited"
	b.vm.exit = 143
	if err := l.DeleteJob(ctx, testSpec().Name); err == nil {
		t.Fatal("checkpoint must be asynchronous")
	}
	if b.deletes != 0 || r.workspaces["service"] != "previous" {
		t.Fatal("unverified upload changed durable state")
	}
	b.vm.phase = "complete"
	// A fresh launcher reconstructs exclusively from the registry and provider.
	fresh, _ := New(l.cfg, b, r, o)
	if err := fresh.DeleteJob(ctx, testSpec().Name); err != nil {
		t.Fatal(err)
	}
	job := r.jobs[testSpec().Name]
	if !job.CheckpointSaved || job.CleanedAt == nil || r.workspaces["service"] != job.CheckpointKey || b.vm != nil {
		t.Fatal("verified checkpoint or cleanup missing")
	}
	if len(o.deleted) != 1 || o.deleted[0] != "previous" {
		t.Fatal("old checkpoint cleanup missing")
	}
	if err := fresh.EnsureWorkspacePVC(ctx, "service", "project"); err != nil {
		t.Fatal(err)
	}
}
func TestCreateIsIdempotentAndUnknownProfilesFailBeforeAllocation(t *testing.T) {
	l, _, b, _ := fixture(t)
	ctx := context.Background()
	spec := testSpec()
	spec.Image = "attacker-image"
	if err := l.CreateJob(ctx, spec); err == nil || b.creates != 0 {
		t.Fatal("unconfigured profile allocated a VM")
	}
	spec.Image = ""
	for i := 0; i < 2; i++ {
		if err := l.CreateJob(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if b.creates != 1 {
		t.Fatalf("created %d VMs", b.creates)
	}
}
func TestAmbiguousCreateCanBeAdoptedForCleanupWithoutExecuting(t *testing.T) {
	l, r, b, _ := fixture(t)
	ctx := context.Background()
	b.ambiguousCreate = true
	if err := l.CreateJob(ctx, testSpec()); err == nil {
		t.Fatal("ambiguous create must report failure")
	}
	if err := l.DeleteJob(ctx, testSpec().Name); err != nil {
		t.Fatal(err)
	}
	if b.deletes != 1 || r.jobs[testSpec().Name].CleanedAt == nil {
		t.Fatal("orphan was not adopted and cleaned")
	}
}

func TestLateAllocationAfterCleanupIsReapedButLiveJobIsPreserved(t *testing.T) {
	l, r, b, _ := fixture(t)
	ctx := context.Background()
	if err := l.CreateJob(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	if err := l.ReapOrphanedJobs(ctx); err != nil || b.deletes != 0 {
		t.Fatal("live ownership was reaped")
	}
	j := r.jobs[testSpec().Name]
	now := time.Now()
	j.CleanedAt = &now
	r.jobs[j.Name] = j
	if err := l.ReapOrphanedJobs(ctx); err != nil || b.deletes != 1 || b.vm != nil {
		t.Fatal("late allocation survived terminal cleanup")
	}
}
func TestDuplicateProviderIdentityFailsClosed(t *testing.T) {
	l, _, b, _ := fixture(t)
	b.duplicate = true
	if err := l.CreateJob(context.Background(), testSpec()); err == nil || b.creates > 0 || b.deletes > 0 {
		t.Fatal("ambiguous ownership must not mutate provider resources")
	}
}
func TestDeleteFailureDoesNotEraseResourcePointer(t *testing.T) {
	l, r, b, _ := fixture(t)
	ctx := context.Background()
	if err := l.CreateJob(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	b.vm.phase = "complete"
	b.failDelete = true
	if err := l.DeleteJob(ctx, testSpec().Name); err == nil {
		t.Fatal("provider deletion failure hidden")
	}
	if r.jobs[testSpec().Name].CleanedAt != nil || b.vm == nil {
		t.Fatal("resource pointer erased before provider confirmation")
	}
	b.failDelete = false
	if err := l.DeleteJob(ctx, testSpec().Name); err != nil {
		t.Fatal(err)
	}
}
func TestExitCodesClassifiedOnlyAfterCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		code int
		want k8s.JobState
	}{{0, k8s.JobSucceeded}, {2, k8s.JobFailed}, {124, k8s.JobDeadlineExceeded}, {137, k8s.JobDeadlineExceeded}} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			l, _, b, _ := fixture(t)
			ctx := context.Background()
			if err := l.CreateJob(ctx, testSpec()); err != nil {
				t.Fatal(err)
			}
			b.vm.phase = "exited"
			b.vm.exit = tc.code
			if got, err := l.GetJobState(ctx, testSpec().Name); err != nil || got != k8s.JobRunning {
				t.Fatalf("before checkpoint: %v %v", got, err)
			}
			b.vm.phase = "complete"
			if got, err := l.GetJobState(ctx, testSpec().Name); err != nil || got != tc.want {
				t.Fatalf("after checkpoint: %v %v", got, err)
			}
		})
	}
}
func TestMissingUncheckpointedVMRequiresVisibleRecovery(t *testing.T) {
	l, r, b, _ := fixture(t)
	ctx := context.Background()
	if err := l.CreateJob(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	b.vm = nil
	if err := l.CreateJob(ctx, testSpec()); err == nil || b.creates != 1 {
		t.Fatal("lost execution was silently replaced")
	}
	if err := l.DeleteJob(ctx, testSpec().Name); err == nil || r.jobs[testSpec().Name].CleanedAt != nil {
		t.Fatal("lost workspace was silently discarded")
	}
}

func TestWallClockDeadlineIsEnforcedAfterProviderPauseOrRestart(t *testing.T) {
	l, r, b, _ := fixture(t)
	ctx := context.Background()
	if err := l.CreateJob(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	j := r.jobs[testSpec().Name]
	j.CreatedAt = time.Now().Add(-2 * time.Minute)
	r.jobs[j.Name] = j
	state, err := l.GetJobState(ctx, j.Name)
	if err != nil || state != k8s.JobRunning {
		t.Fatalf("deadline stop: %v %v", state, err)
	}
	if !strings.Contains(b.vm.commands[len(b.vm.commands)-1], "deadline-exceeded") {
		t.Fatal("expired wall-clock deadline did not stop the resumed guest")
	}
}
func TestCheckpointDeleteFailurePreservesWorkspaceRegistry(t *testing.T) {
	l, r, _, o := fixture(t)
	r.workspaces["service"] = "saved"
	o.failDelete = true
	if err := l.DeleteWorkspacePVC(context.Background(), "service"); err == nil {
		t.Fatal("object erasure failure hidden")
	}
	if r.workspaces["service"] != "saved" {
		t.Fatal("workspace registry erased prematurely")
	}
}
func TestEnvironmentRoundTripDoesNotExecutePromptText(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "injected")
	value := "hello ' \"\n$(touch " + marker + ") `touch " + marker + "`"
	b, err := encodeEnv(map[string]string{"TASK_PROMPT": value})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", "-c", string(b)+"printf %s \"$TASK_PROMPT\"")
	out, err := cmd.Output()
	if err != nil || string(out) != value {
		t.Fatalf("round trip mismatch: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("prompt text executed as shell")
	}
	if _, err := encodeEnv(map[string]string{"BAD;echo": "x"}); err == nil {
		t.Fatal("invalid environment key accepted")
	}
}
