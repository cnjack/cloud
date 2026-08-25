package store

import (
	"strings"
	"testing"
)

func TestRepositoryAgentBoardExecutionAccountMigration(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0074_repository_agent_board.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := strings.Join(strings.Fields(string(sql)), " ")
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS execution_account_id TEXT",
		"REFERENCES users(id) ON DELETE SET NULL",
		"CREATE INDEX IF NOT EXISTS automations_v2_execution_account_idx",
		"'repository_owner'",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("0074 migration missing %q", want)
		}
	}
	for _, line := range strings.Split(string(sql), "\n") {
		trimmed := strings.TrimSpace(strings.ToUpper(line))
		if strings.HasPrefix(trimmed, "DROP ") || strings.HasPrefix(trimmed, "TRUNCATE ") {
			t.Fatalf("0074 migration must be append-only; found %q", strings.TrimSpace(line))
		}
	}
}
