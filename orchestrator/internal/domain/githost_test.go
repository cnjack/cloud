package domain

import "testing"

// GitHostAllowed is the shared allowlist gate (D20) behind both the API
// dispatch gate and the schedule poller's host gate — these cases pin the
// semantics both callers rely on.
func TestGitHostAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowlist []string
		host      string
		want      bool
	}{
		{"empty allowlist permits everything", nil, "anywhere.example.com", true},
		{"exact match", []string{"github.com"}, "github.com", true},
		{"no match", []string{"github.com"}, "gitlab.com", false},
		{"case-insensitive + URL form normalize", []string{"GitHub.com"}, "https://github.com/o/r", true},
		{"port-sensitive: allowed port matches", []string{"gitea.svc:3000"}, "gitea.svc:3000", true},
		{"port-sensitive: other port rejected", []string{"gitea.svc:3000"}, "gitea.svc:9999", false},
		{"scheme-default port folds away", []string{"gitea.example.com"}, "https://gitea.example.com:443", true},
		{"empty host rejected under a non-empty allowlist", []string{"github.com"}, "", false},
		{"garbage host rejected", []string{"github.com"}, "http://", false},
	}
	for _, c := range cases {
		if got := GitHostAllowed(c.allowlist, c.host); got != c.want {
			t.Errorf("%s: GitHostAllowed(%v, %q) = %v, want %v", c.name, c.allowlist, c.host, got, c.want)
		}
	}
}
