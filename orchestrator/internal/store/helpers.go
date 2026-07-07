package store

import (
	"strings"
	"time"
)

// nullTime returns t unchanged; pgx encodes a nil *time.Time as SQL NULL. It
// exists so the auth store's INSERT/UPDATE calls read symmetrically with nullStr.
func nullTime(t *time.Time) *time.Time { return t }

// prefixCols qualifies every column in a comma-separated column list with the
// given table alias, e.g. prefixCols("u", "id, name") => "u.id, u.name". Used so
// the shared *Cols constants can be reused in JOIN queries without ambiguity.
func prefixCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
