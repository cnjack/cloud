package config

import (
	"strings"
	"testing"
)

func TestArchiveEnabled(t *testing.T) {
	full := Config{S3Endpoint: "http://m:9000", S3Bucket: "b", S3AccessKey: "a", S3SecretKey: "s"}
	if !full.ArchiveEnabled() {
		t.Fatal("fully-configured object storage should be enabled")
	}
	// Any single missing field disables it (partial config is never a best-effort).
	for _, drop := range []func(*Config){
		func(c *Config) { c.S3Endpoint = "" },
		func(c *Config) { c.S3Bucket = "" },
		func(c *Config) { c.S3AccessKey = "" },
		func(c *Config) { c.S3SecretKey = "" },
	} {
		c := full
		drop(&c)
		if c.ArchiveEnabled() {
			t.Errorf("partial config should be disabled: %+v", c)
		}
	}
}

func TestArchiveDisabledReason(t *testing.T) {
	// Fully wired + persistent + positive idle days => enabled (empty reason).
	on := Config{
		PersistentWorkspace: true, ArchiveIdleDays: 14,
		S3Endpoint: "http://m:9000", S3Bucket: "b", S3AccessKey: "a", S3SecretKey: "s",
	}
	if r := on.ArchiveDisabledReason(); r != "" {
		t.Fatalf("expected enabled (empty reason), got %q", r)
	}

	cases := []struct {
		name   string
		mut    func(*Config)
		expect string
	}{
		{"persistent off", func(c *Config) { c.PersistentWorkspace = false }, "persistent workspace"},
		{"no object storage", func(c *Config) { c.S3Bucket = "" }, "object storage not configured"},
		{"idle days zero", func(c *Config) { c.ArchiveIdleDays = 0 }, "ARCHIVE_IDLE_DAYS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := on
			tc.mut(&c)
			r := c.ArchiveDisabledReason()
			if !strings.Contains(r, tc.expect) {
				t.Fatalf("reason = %q, want substring %q", r, tc.expect)
			}
		})
	}
}
