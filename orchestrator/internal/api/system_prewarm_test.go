package api

// system_prewarm_test.go — the runner-image prewarm API: POST
// /api/v1/system/runner-image/prewarm (console Cluster page "sync runner
// image") plus the runner.prewarm half of the /system snapshot. The launcher
// capability (k8s.ImagePrewarmer) is optional, so both the supported and the
// fail-visible unsupported paths are pinned here.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// prewarmFakeLauncher satisfies k8s.JobLauncher via the embedded (nil)
// interface — the prewarm paths under test never call a JobLauncher method —
// and adds the OPTIONAL ImagePrewarmer capability on top.
type prewarmFakeLauncher struct {
	k8s.JobLauncher
	status    k8s.PrewarmStatus
	statusErr error
	syncErr   error
	syncCalls int
}

func (f *prewarmFakeLauncher) PrewarmRunnerImage(context.Context) error {
	f.syncCalls++
	return f.syncErr
}

func (f *prewarmFakeLauncher) RunnerImagePrewarmStatus(context.Context) (k8s.PrewarmStatus, error) {
	return f.status, f.statusErr
}

func newPrewarmServer(launcher k8s.JobLauncher) *httptest.Server {
	cfg := &config.Config{
		ConsoleToken: consoleToken,
		Namespace:    "jcloud",
		RunnerImage:  "ghcr.io/acme/runner:v1",
		JobLauncher:  "kubernetes",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(store.NewMemStore(), cfg, log, sse.NewHub(), launcher)
	return httptest.NewServer(srv.Handler())
}

func TestPrewarmRunnerImageSyncsAndReturnsStatus(t *testing.T) {
	launcher := &prewarmFakeLauncher{status: k8s.PrewarmStatus{
		Desired: 3, Ready: 3, Image: "ghcr.io/acme/runner:v1", LastSync: "2026-07-16T01:00:00Z",
	}}
	ts := newPrewarmServer(launcher)
	defer ts.Close()

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/runner-image/prewarm", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prewarm: status=%d want 200", resp.StatusCode)
	}
	var got systemPrewarm
	decode(t, resp, &got)
	if launcher.syncCalls != 1 {
		t.Fatalf("launcher sync calls = %d, want exactly 1", launcher.syncCalls)
	}
	if !got.Supported || got.Desired != 3 || got.Ready != 3 || got.LastSync == "" {
		t.Fatalf("response=%+v want supported + 3/3 + last_sync", got)
	}
}

func TestPrewarmRunnerImageUnsupportedLauncherIs409(t *testing.T) {
	// nil launcher (API-only mode): the capability is absent, and the contract
	// is a typed, actionable 409 — never a fabricated success (D14).
	ts := newPrewarmServer(nil)
	defer ts.Close()

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/runner-image/prewarm", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("prewarm unsupported: status=%d want 409", resp.StatusCode)
	}
	var got errorBody
	decode(t, resp, &got)
	if got.Error.Code != "prewarm_not_supported" {
		t.Fatalf("error code=%q want prewarm_not_supported", got.Error.Code)
	}
}

func TestPrewarmRunnerImageLauncherFailureIs500(t *testing.T) {
	launcher := &prewarmFakeLauncher{syncErr: errors.New("boom")}
	ts := newPrewarmServer(launcher)
	defer ts.Close()

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/runner-image/prewarm", consoleToken, nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("prewarm failure: status=%d want 500", resp.StatusCode)
	}
	resp.Body.Close()
}

// GET /system must surface the prewarm snapshot honestly in all three states:
// supported+healthy, supported+status-read-error (curated, never raw client-go
// detail), and unsupported (launcher without the capability).
func TestSystemSnapshotPrewarmSection(t *testing.T) {
	healthy := &prewarmFakeLauncher{status: k8s.PrewarmStatus{Desired: 2, Ready: 1, Image: "ghcr.io/acme/runner:v1"}}
	ts := newPrewarmServer(healthy)
	defer ts.Close()

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/system", consoleToken, nil)
	var got systemResponse
	decode(t, resp, &got)
	if !got.Runner.Prewarm.Supported || got.Runner.Prewarm.Desired != 2 || got.Runner.Prewarm.Ready != 1 {
		t.Fatalf("prewarm=%+v want supported + 1/2 ready", got.Runner.Prewarm)
	}

	broken := &prewarmFakeLauncher{statusErr: errors.New("rbac: daemonsets forbidden — raw detail must NOT leak")}
	ts2 := newPrewarmServer(broken)
	defer ts2.Close()
	resp = do(t, http.MethodGet, ts2.URL+"/api/v1/system", consoleToken, nil)
	decode(t, resp, &got)
	if !got.Runner.Prewarm.Supported || got.Runner.Prewarm.Error == "" {
		t.Fatalf("prewarm=%+v want supported + curated error", got.Runner.Prewarm)
	}
	if got.Runner.Prewarm.Error == "rbac: daemonsets forbidden — raw detail must NOT leak" {
		t.Fatal("raw launcher error leaked into /system; want the curated line")
	}

	ts3 := newPrewarmServer(nil)
	defer ts3.Close()
	resp = do(t, http.MethodGet, ts3.URL+"/api/v1/system", consoleToken, nil)
	decode(t, resp, &got)
	if got.Runner.Prewarm.Supported {
		t.Fatalf("prewarm=%+v want supported=false with a nil launcher", got.Runner.Prewarm)
	}
}
