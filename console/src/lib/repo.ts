/*
 * repo.ts — the console-side mirror of the orchestrator's repo-URL classifier
 * (orchestrator/internal/domain/repourl.go · ParseRepoURL). Kept in lockstep so
 * the new-project form can predict, before submit, which provider a draft-PR
 * repo will target — and prompt the user to link that provider's identity.
 *
 * Rules (M1):
 *   - github.com          → github
 *   - gitlab.com          → gitlab
 *   - any OTHER http(s) host with an owner/name path → gitea (the single
 *     self-hosted provider wired in M1)
 *   - git://, ssh, file://, or an http(s) URL without an owner/name path → raw
 *     (no provider; never eligible for draft_pr)
 */
import type { GitProvider, Service } from '../api/types';

/** Well-known public hosts → provider (mirrors DefaultProviderHosts()). */
const KNOWN_HOSTS: Record<string, GitProvider> = {
  'github.com': 'github',
  'gitlab.com': 'gitlab',
};

/**
 * The provider a draft-PR flow would target for this repo URL, or null when the
 * URL is not a provider repo (raw — draft_pr is not possible).
 */
export function providerForRepoUrl(raw: string): GitProvider | null {
  const trimmed = raw.trim();
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    return null;
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
  if (!ownerNameFromPath(u.pathname)) return null;
  const host = u.hostname.toLowerCase();
  return KNOWN_HOSTS[host] ?? 'gitea';
}

/** True when the URL parses to a provider repo (owner/name on a http(s) host). */
export function isProviderRepoUrl(raw: string): boolean {
  return providerForRepoUrl(raw) !== null;
}

/** "owner/name" from a URL path, or "" when there are fewer than two segments. */
function ownerNameFromPath(path: string): string {
  const stripped = path.replace(/\.git$/, '').replace(/^\/+|\/+$/g, '');
  const parts = stripped.split('/').filter(Boolean);
  if (parts.length < 2) return '';
  return `${parts[0]}/${parts[1]}`;
}

/** A short human label for one repository (service): "owner/name" or the raw URL. */
export function serviceRepoLabel(svc: Service): string {
  return (svc.repo_kind === 'provider' ? svc.repo_owner_name : svc.raw_repo_url) ?? svc.name;
}

/**
 * The one-line repo summary a project card/switcher shows now that repo config
 * lives only on services: nothing / the sole repo's label / a count.
 */
export function projectRepoSummary(services: Service[] | undefined): string {
  const list = services ?? [];
  if (list.length === 0) return 'No repositories';
  if (list.length === 1 && list[0]) return serviceRepoLabel(list[0]);
  return `${list.length} repositories`;
}
