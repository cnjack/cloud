package store

import (
	"strings"
	"testing"
)

// A duplicate version can appear locally green on a fresh database while an
// existing installation skips the new file entirely. Guard the deployed upgrade
// path without requiring PostgreSQL in every CI job.
func TestMigrationVersionsAreUnique(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, _, _ := strings.Cut(entry.Name(), "_")
		if previous, ok := seen[version]; ok {
			t.Fatalf("migration version %s reused by %s and %s", version, previous, entry.Name())
		}
		seen[version] = entry.Name()
	}
}
