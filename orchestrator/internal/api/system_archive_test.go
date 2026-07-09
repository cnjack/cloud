package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// systemArchiveFor builds a one-off /api/v1/system server around cfg and returns
// the decoded archive snapshot plus the raw body (for the no-leak scan).
func systemArchiveFor(t *testing.T, cfg *config.Config) (systemArchive, string) {
	t.Helper()
	cfg.ConsoleToken = consoleToken
	srv := New(store.NewMemStore(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp := do(t, "GET", ts.URL+"/api/v1/system", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system: status=%d want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var got systemResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, raw)
	}
	return got.Archive, string(raw)
}

func TestSystemArchiveDisabledReason(t *testing.T) {
	// Object storage unconfigured (persistent on) => disabled + honest reason.
	arch, _ := systemArchiveFor(t, &config.Config{PersistentWorkspace: true, ArchiveIdleDays: 14})
	if arch.Enabled {
		t.Fatal("archive should be disabled with no object storage")
	}
	if !strings.Contains(arch.Reason, "object storage not configured") {
		t.Fatalf("reason = %q, want it to name the missing object storage", arch.Reason)
	}
	if arch.Endpoint != "" || arch.Bucket != "" {
		t.Fatalf("disabled archive must not expose addressing: %+v", arch)
	}

	// Persistent workspace off => that prerequisite is reported first.
	arch, _ = systemArchiveFor(t, &config.Config{
		PersistentWorkspace: false, ArchiveIdleDays: 14,
		S3Endpoint: "http://m:9000", S3Bucket: "b", S3AccessKey: "a", S3SecretKey: "s",
	})
	if arch.Enabled || !strings.Contains(arch.Reason, "persistent workspace") {
		t.Fatalf("expected persistent-workspace reason, got %+v", arch)
	}
}

func TestSystemArchiveEnabledNoSecretLeak(t *testing.T) {
	cfg := &config.Config{
		PersistentWorkspace: true, ArchiveIdleDays: 14,
		S3Endpoint: "http://minio.jcloud.svc:9000", S3Bucket: "jcloud-workspaces",
		S3AccessKey: "AKIA-DO-NOT-LEAK", S3SecretKey: "s3-secret-DO-NOT-LEAK",
	}
	arch, raw := systemArchiveFor(t, cfg)
	if !arch.Enabled {
		t.Fatalf("archive should be enabled: %+v", arch)
	}
	if arch.Endpoint != cfg.S3Endpoint || arch.Bucket != cfg.S3Bucket || arch.IdleDays != 14 {
		t.Fatalf("archive addressing wrong: %+v", arch)
	}
	if arch.Reason != "" {
		t.Fatalf("enabled archive must have empty reason, got %q", arch.Reason)
	}
	// Load-bearing: the S3 credentials must NEVER appear in the snapshot.
	for _, secret := range []string{cfg.S3AccessKey, cfg.S3SecretKey, "DO-NOT-LEAK"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("SECRET LEAK: system body contains %q\nbody: %s", secret, raw)
		}
	}
}
