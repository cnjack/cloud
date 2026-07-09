package domain

// GitHostAllowed reports whether host is permitted by the cluster git-host
// allowlist (D20 / F5). An empty allowlist imposes NO restriction (suitable only
// for closed deployments); otherwise the host is compared in the canonical
// "hostname[:port]" form (NormalizeGitHost) — the comparison is PORT-SENSITIVE
// (SSRF review C1②), so an entry for gitea.svc:3000 does not open gitea.svc:9999.
//
// This is the SINGLE implementation behind every dispatch-time host gate (the
// API's create/retry/resume/review gate and the schedule poller's gate). It is
// security-sensitive: keep the two callers delegating here rather than growing
// their own copies, so the checks cannot drift apart.
func GitHostAllowed(allowlist []string, host string) bool {
	if len(allowlist) == 0 {
		return true
	}
	h := NormalizeGitHost(host)
	if h == "" {
		return false
	}
	for _, a := range allowlist {
		if NormalizeGitHost(a) == h {
			return true
		}
	}
	return false
}
