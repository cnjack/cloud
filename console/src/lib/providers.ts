/*
 * providers.ts — the single list of git providers the console understands, and
 * the provider_allowlist option set (the known providers plus the "raw" sentinel
 * for opaque/raw repos). Mirrors the orchestrator (domain.ValidProvider +
 * allowlistProviders in api/projects.go). Import these instead of re-listing the
 * providers inline so the modal and the mock client can never drift apart.
 */
import type { GitProvider } from '../api/types';

/** The git hosts the orchestrator classifies repos on. */
export const KNOWN_PROVIDERS: readonly GitProvider[] = ['gitea', 'github', 'gitlab'];

/**
 * The values a project's provider_allowlist may contain: the known providers plus
 * the "raw" sentinel (raw repos carry no provider).
 */
export const ALLOWLIST_PROVIDERS: readonly (GitProvider | 'raw')[] = [
  ...KNOWN_PROVIDERS,
  'raw',
];
