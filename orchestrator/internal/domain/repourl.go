package domain

import (
	"net/url"
	"strings"
)

// RepoSpec is the parsed classification of a repo reference. It maps directly to
// the Service repo fields (repo_kind/provider/repo_owner_name/raw_repo_url) and
// is the single server-side implementation of the "smart repo_url" parse the
// compatibility shim and the console rely on (multitenant blueprint §4).
type RepoSpec struct {
	RepoKind      RepoKind
	Provider      GitProvider // set when RepoKind == RepoKindProvider
	RepoOwnerName string      // "owner/name"; set when RepoKind == RepoKindProvider
	RawRepoURL    string      // set when RepoKind == RepoKindRaw
}

// DefaultProviderHosts maps the well-known public git hosts to their provider.
//
// TODO(M2): extend this from the configured AUTH_{P}_EXTERNAL_URL /
// AUTH_{P}_INTERNAL_URL so self-hosted GitLab / GitHub Enterprise / the local
// Gitea host classify to the right provider. Until then ParseRepoURL treats any
// OTHER http(s) host with an owner/name path as gitea (the single self-hosted
// provider wired in M1) — see ParseRepoURL.
func DefaultProviderHosts() map[string]GitProvider {
	return map[string]GitProvider{
		"github.com": ProviderGitHub,
		"gitlab.com": ProviderGitLab,
	}
}

// ParseRepoURL classifies a repo reference into a RepoSpec (blueprint §4). Rules
// for M1:
//
//   - An http(s) URL whose host is a known provider host (from hosts) and whose
//     path has an "owner/name" shape → provider form for that host's provider.
//   - Any OTHER http(s) URL with an "owner/name" path → provider form classified
//     as gitea (the single self-hosted provider wired in M1). TODO(M2): only
//     classify as gitea when the host matches a configured gitea host; unknown
//     hosts should become raw.
//   - Everything else — git://, ssh, file://, or an http(s) URL without an
//     owner/name path (e.g. the in-cluster git-seed, or a bare host) → raw.
//
// hosts may be nil, in which case DefaultProviderHosts() is used. It is an
// injectable seam so M2 can pass the configured provider hosts.
func ParseRepoURL(raw string, hosts map[string]GitProvider) RepoSpec {
	raw = strings.TrimSpace(raw)
	if hosts == nil {
		hosts = DefaultProviderHosts()
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return RepoSpec{RepoKind: RepoKindRaw, RawRepoURL: raw}
	}
	ownerName, ok := ownerNameFromPath(u.Path)
	if !ok {
		return RepoSpec{RepoKind: RepoKindRaw, RawRepoURL: raw}
	}
	prov, known := hosts[strings.ToLower(u.Hostname())]
	if !known {
		// M1: any other http(s) provider-shaped host is treated as gitea.
		prov = ProviderGitea
	}
	return RepoSpec{RepoKind: RepoKindProvider, Provider: prov, RepoOwnerName: ownerName}
}

// ownerNameFromPath extracts "owner/name" from a URL path, stripping a trailing
// ".git" and folding any extra segments away (only the first two are kept).
// Returns ok=false when there are fewer than two non-empty path segments (so a
// bare "/repo.git" is not mistaken for a provider repo).
func ownerNameFromPath(p string) (string, bool) {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) < 2 {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// ServiceCloneURL returns the clone/push origin for a service given the gitea
// deployment base URL. A raw service returns its opaque raw_repo_url as-is; a
// provider service returns "<provider base>/<owner>/<repo>.git". Returns "" when
// the URL cannot be derived (unknown provider, empty base, or missing
// owner/name). This is the single reconstruction of the flat "repo_url" the
// compatibility shim and the runner env both need.
func ServiceCloneURL(svc Service, giteaURL string) string {
	if svc.RepoKind == RepoKindRaw {
		return svc.RawRepoURL
	}
	base := ProviderBaseURL(svc.Provider, giteaURL)
	if base == "" || strings.TrimSpace(svc.RepoOwnerName) == "" {
		return ""
	}
	return base + "/" + strings.Trim(svc.RepoOwnerName, "/") + ".git"
}

// ProviderBaseURL returns the base URL for a provider's repos. For gitea it is
// the configured deployment URL (giteaURL); github/gitlab use their public
// hosts. Returns "" when the provider is unknown or gitea has no configured URL.
// TODO(M2): source the gitea/self-hosted base from the AUTH_*_INTERNAL_URL
// config instead of the single GITEA_URL.
func ProviderBaseURL(p GitProvider, giteaURL string) string {
	switch p {
	case ProviderGitea:
		return strings.TrimRight(strings.TrimSpace(giteaURL), "/")
	case ProviderGitHub:
		return "https://github.com"
	case ProviderGitLab:
		return "https://gitlab.com"
	default:
		return ""
	}
}
