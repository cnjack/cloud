package domain

import "testing"

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		kind   RepoKind
		prov   GitProvider
		owner  string
		rawURL string
	}{
		{"github .git", "https://github.com/owner/repo.git", RepoKindProvider, ProviderGitHub, "owner/repo", ""},
		{"github no .git", "https://github.com/owner/repo", RepoKindProvider, ProviderGitHub, "owner/repo", ""},
		{"github mixed case host", "https://GitHub.com/Owner/Repo", RepoKindProvider, ProviderGitHub, "Owner/Repo", ""},
		{"gitlab", "http://gitlab.com/group/proj", RepoKindProvider, ProviderGitLab, "group/proj", ""},
		{"self-hosted host → gitea", "https://gitea.example.com/o/n.git", RepoKindProvider, ProviderGitea, "o/n", ""},
		{"in-cluster gitea http", "http://gitea.jcloud.svc.cluster.local:3000/jcloud/seed.git", RepoKindProvider, ProviderGitea, "jcloud/seed", ""},
		{"extra path segments folded", "https://host.com/a/b/c/d", RepoKindProvider, ProviderGitea, "a/b", ""},
		{"git:// seed → raw", "git://git.jcloud.svc.cluster.local/seed.git", RepoKindRaw, "", "", "git://git.jcloud.svc.cluster.local/seed.git"},
		{"file:// → raw", "file:///seed/repo.git", RepoKindRaw, "", "", "file:///seed/repo.git"},
		{"ssh scheme → raw", "ssh://git@host/o/n.git", RepoKindRaw, "", "", "ssh://git@host/o/n.git"},
		{"scp-like → raw", "git@github.com:owner/repo.git", RepoKindRaw, "", "", "git@github.com:owner/repo.git"},
		{"single path segment → raw", "https://git/x.git", RepoKindRaw, "", "", "https://git/x.git"},
		{"host only → raw", "https://host.com/only", RepoKindRaw, "", "", "https://host.com/only"},
		{"empty → raw", "", RepoKindRaw, "", "", ""},
		{"whitespace trimmed", "  https://github.com/o/n  ", RepoKindProvider, ProviderGitHub, "o/n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRepoURL(tc.in, nil)
			if got.RepoKind != tc.kind {
				t.Fatalf("repo_kind = %q want %q", got.RepoKind, tc.kind)
			}
			if got.Provider != tc.prov {
				t.Fatalf("provider = %q want %q", got.Provider, tc.prov)
			}
			if got.RepoOwnerName != tc.owner {
				t.Fatalf("owner_name = %q want %q", got.RepoOwnerName, tc.owner)
			}
			if got.RawRepoURL != tc.rawURL {
				t.Fatalf("raw_repo_url = %q want %q", got.RawRepoURL, tc.rawURL)
			}
		})
	}
}

// TestParseRepoURLInjectedHosts proves the hosts map is an injectable seam (M2
// will pass the configured provider hosts).
func TestParseRepoURLInjectedHosts(t *testing.T) {
	hosts := map[string]GitProvider{"code.internal": ProviderGitLab}
	got := ParseRepoURL("https://code.internal/team/app.git", hosts)
	if got.RepoKind != RepoKindProvider || got.Provider != ProviderGitLab || got.RepoOwnerName != "team/app" {
		t.Fatalf("injected host not honoured: %+v", got)
	}
	// A host NOT in the injected map still falls back to gitea (M1 rule).
	got = ParseRepoURL("https://other.internal/team/app", hosts)
	if got.Provider != ProviderGitea {
		t.Fatalf("unknown host fallback = %q want gitea", got.Provider)
	}
}

func TestProviderBaseURL(t *testing.T) {
	if got := ProviderBaseURL(ProviderGitea, "http://gitea.test/"); got != "http://gitea.test" {
		t.Fatalf("gitea base = %q want http://gitea.test (trailing slash trimmed)", got)
	}
	if got := ProviderBaseURL(ProviderGitHub, "ignored"); got != "https://github.com" {
		t.Fatalf("github base = %q", got)
	}
	if got := ProviderBaseURL(ProviderGitLab, ""); got != "https://gitlab.com" {
		t.Fatalf("gitlab base = %q", got)
	}
	if got := ProviderBaseURL(ProviderGitea, ""); got != "" {
		t.Fatalf("gitea base with no url = %q want empty", got)
	}
	if got := ProviderBaseURL("nope", "x"); got != "" {
		t.Fatalf("unknown provider base = %q want empty", got)
	}
}

func TestServiceCloneURL(t *testing.T) {
	raw := Service{RepoKind: RepoKindRaw, RawRepoURL: "git://x/seed.git"}
	if got := ServiceCloneURL(raw, "http://gitea.test"); got != "git://x/seed.git" {
		t.Fatalf("raw clone url = %q", got)
	}
	prov := Service{RepoKind: RepoKindProvider, Provider: ProviderGitea, RepoOwnerName: "jcloud/seed"}
	if got := ServiceCloneURL(prov, "http://gitea.test"); got != "http://gitea.test/jcloud/seed.git" {
		t.Fatalf("gitea clone url = %q", got)
	}
	gh := Service{RepoKind: RepoKindProvider, Provider: ProviderGitHub, RepoOwnerName: "o/n"}
	if got := ServiceCloneURL(gh, ""); got != "https://github.com/o/n.git" {
		t.Fatalf("github clone url = %q", got)
	}
	// Missing owner/name → empty.
	if got := ServiceCloneURL(Service{RepoKind: RepoKindProvider, Provider: ProviderGitea}, "http://g"); got != "" {
		t.Fatalf("no owner clone url = %q want empty", got)
	}
}
