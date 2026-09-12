package cuberuntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is an explicitly labelled local test rig. The image and guest scripts
// are real; only the agent entrypoint and object/credential services are fixtures.
// Production jcode/model execution is verified separately against CubeSandbox.
type dockerVM struct{ name string }

func (v *dockerVM) ID() string { return v.name }
func (v *dockerVM) Run(ctx context.Context, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Match envd's root process cwd, rather than the OCI WORKDIR.
	b, err := exec.CommandContext(ctx, "docker", "exec", "-w", "/root", v.name, "/bin/bash", "-c", command).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("guest command: %w: %s", err, b)
	}
	return string(b), nil
}
func (v *dockerVM) Write(ctx context.Context, path string, b []byte) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", v.name, "/bin/bash", "-c", "cat > "+shellQuote(path))
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("write guest: %w: %s", err, out)
	}
	return nil
}
func dockerGuest(t *testing.T) *dockerVM {
	t.Helper()
	if os.Getenv("CUBE_DOCKER_TEST") != "1" {
		t.Skip("CUBE_DOCKER_TEST=1 enables the real guest script rig")
	}
	name := fmt.Sprintf("jcloud-cube-guest-%d", time.Now().UnixNano())
	cmd := exec.Command("docker", "run", "-d", "--platform", "linux/amd64", "--name", name, "--cap-add", "SYS_ADMIN", "--cap-add", "NET_ADMIN", "--security-opt", "seccomp=unconfined", "jcloud/cube-runner:poc")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start guest: %v %s", err, b)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	v := &dockerVM{name}
	ctx := context.Background()
	if _, err := v.Run(ctx, `curl -fsS --retry 10 --retry-connrefused --retry-delay 1 http://127.0.0.1:49983/health >/dev/null`, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	return v
}

type rigObjects struct{ base string }

func (o rigObjects) PresignPut(k string, _ time.Duration) (string, error) {
	return o.base + "/objects/" + k, nil
}
func (o rigObjects) PresignGet(k string, _ time.Duration) (string, error) {
	return o.base + "/objects/" + k, nil
}
func (o rigObjects) Delete(context.Context, string) error { return nil }

func TestDockerGuestConfinementPluginCheckpointAndRestore(t *testing.T) {
	first := dockerGuest(t)
	ctx := context.Background()
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/plugins/credentials") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"credentials":[{"provider":"jtype","base_url":"https://jtype.example","access_token":"test-access"}]}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read failed", 500)
				return
			}
			objects[r.URL.Path] = b
		case "GET":
			b, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = listener
	srv.Start()
	defer srv.Close()
	base := fmt.Sprintf("http://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	l, _, _, _ := fixture(t)
	l.objects = rigObjects{base}
	l.cfg.PluginBinary = os.Getenv("CUBE_PLUGIN_BINARY")
	if l.cfg.PluginBinary == "" {
		t.Fatal("CUBE_PLUGIN_BINARY must point at a compiled linux/amd64 plugin-runtime")
	}
	spec := testSpec()
	spec.PluginCredentials = true
	spec.PluginProviders = []string{"jtype"}
	spec.Env["ORCH_BASE_URL"] = base
	spec.ModelConfigBase64 = base64.StdEncoding.EncodeToString([]byte(`{"fixture":"model"}`))
	spec.Env["TASK_PROMPT"] = "literal ' prompt $(id)"
	entry := `#!/bin/bash
set -euo pipefail
test "$(id -u)" = 10001
test "$PWD" = /workspace
test "$HOME" = /home/jcode
test "$TASK_PROMPT" = 'literal '\'' prompt $(id)'
grep -q test-access "$HOME/.jcode/mcp.json"
grep -q model /run/jcloud/config/config.json
if (echo overwritten > /run/jcloud/config/config.json) 2>/dev/null; then exit 71; fi
if curl --noproxy '*' -fsS --max-time 3 http://127.0.0.1:49983/health; then exit 72; fi
if curl --noproxy '*' -g -fsS --max-time 3 http://[::1]:49983/health; then exit 73; fi
cd /workspace
git init -q
git config user.name Guest
git config user.email guest@localhost
printf 'saved\n' > proof
chmod 775 proof
ln -s proof link
git add .
git commit -qm proof
printf 'memory\n' > "$HOME/.jcode/memory.txt"
echo guest-confinement-ok
`
	if err := first.Write(ctx, "/usr/local/bin/entrypoint.sh", []byte(entry)); err != nil {
		t.Fatal(err)
	}
	if err := l.bootstrap(ctx, first, spec, ""); err != nil {
		t.Fatal(err)
	}
	waitGuestPhase(t, first, "exited")
	s, _ := guestState(ctx, first)
	if s.exit != 0 {
		logs, _ := first.Run(ctx, "cat /run/jcloud/control/runner.log", time.Second)
		t.Fatalf("runner exit=%d: %s", s.exit, logs)
	}
	j := &Job{Name: spec.Name, ServiceID: spec.ServiceID, CheckpointKey: "test-checkpoint"}
	if err := l.startCheckpoint(ctx, j, first); err != nil {
		t.Fatal(err)
	}
	waitGuestPhase(t, first, "complete")
	second := dockerGuest(t)
	entry = `#!/bin/bash
set -eu
test "$(cat /workspace/proof)" = saved
test -x /workspace/proof
test "$(stat -c %a /workspace/proof)" = 775
test "$(readlink /workspace/link)" = proof
test "$(cat /home/jcode/.jcode/memory.txt)" = memory
test -z "$(git -C /workspace status --porcelain)"
echo checkpoint-restore-ok
`
	if err := second.Write(ctx, "/usr/local/bin/entrypoint.sh", []byte(entry)); err != nil {
		t.Fatal(err)
	}
	restore, _ := l.objects.PresignGet(j.CheckpointKey, time.Hour)
	if err := l.bootstrap(ctx, second, spec, restore); err != nil {
		t.Fatal(err)
	}
	waitGuestPhase(t, second, "exited")
	s, _ = guestState(ctx, second)
	if s.exit != 0 {
		logs, _ := second.Run(ctx, "cat /run/jcloud/control/runner.log", time.Second)
		t.Fatalf("restored runner exit=%d: %s", s.exit, logs)
	}
}
func waitGuestPhase(t *testing.T, vm VM, phase string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		s, err := guestState(context.Background(), vm)
		if err != nil {
			t.Fatal(err)
		}
		if s.phase == phase {
			return
		}
		if s.phase == "checkpoint_failed" {
			logs, _ := vm.Run(context.Background(), "cat /run/jcloud/control/checkpoint.log", time.Second)
			t.Fatalf("checkpoint failed: %s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := vm.Run(context.Background(), "cat /run/jcloud/control/runner.log", time.Second)
	t.Fatalf("guest did not reach %s: %s", phase, logs)
}

func TestDockerGuestHardTimeoutStopsDescendants(t *testing.T) {
	vm := dockerGuest(t)
	ctx := context.Background()
	l, _, _, _ := fixture(t)
	spec := testSpec()
	spec.TimeoutSeconds = 1
	entry := []byte("#!/bin/bash\nset -eu\nsleep 30 &\nwait\n")
	if err := vm.Write(ctx, "/usr/local/bin/entrypoint.sh", entry); err != nil {
		t.Fatal(err)
	}
	if err := l.bootstrap(ctx, vm, spec, ""); err != nil {
		t.Fatal(err)
	}
	waitGuestPhase(t, vm, "exited")
	s, err := guestState(ctx, vm)
	if err != nil || s.exit != 124 {
		t.Fatalf("timeout receipt: %+v %v", s, err)
	}
	if _, err := vm.Run(ctx, `ps -eo uid=,stat=,comm= | awk '$1 == 10001 && $2 !~ /^Z/ { print; live=1 } END { exit live }'`, time.Second); err != nil {
		t.Fatal("runner descendants survived timeout:", err)
	}
}
