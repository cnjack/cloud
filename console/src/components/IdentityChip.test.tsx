/*
 * IdentityChip — the header principal + its dropdown menu (M4, blueprint §5).
 *   - a real user: avatar/name/role, linked identities, Link <unlinked provider>,
 *     Sign out
 *   - the console-token service principal: no identities to link
 *   - fallback (no principal): a plain role label so the shell still renders
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { IdentityChip } from './IdentityChip';
import type { AuthProviderInfo, Me } from '../api/types';

const PROVIDERS: AuthProviderInfo[] = [
  { id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' },
  { id: 'github', name: 'GitHub', login_url: '/auth/login/github' },
];

function userMe(): Me {
  return {
    user: { id: 'u1', display_name: 'Ada Lovelace', avatar_url: '', is_cluster_admin: true },
    is_service: false,
    identities: [{ provider: 'gitea', username: 'ada' }],
  };
}

describe('IdentityChip — user principal', () => {
  it('shows the name + cluster-admin badge and opens a menu with identities + link options', () => {
    const onSignOut = vi.fn();
    render(
      <IdentityChip me={userMe()} providers={PROVIDERS} role="cluster-admin" onSignOut={onSignOut} />,
    );

    const chip = screen.getByTestId('identity-chip');
    expect(chip.textContent).toContain('Ada Lovelace');
    expect(chip.textContent).toContain('Cluster admin');

    // Menu is closed until the chip is clicked.
    expect(screen.queryByTestId('identity-menu')).toBeNull();
    fireEvent.click(chip);

    // Linked identity (gitea/ada) is listed.
    expect(screen.getByTestId('linked-identities').textContent).toContain('ada');
    // github is configured but unlinked → a Link affordance to /auth/link/github.
    const link = screen.getByTestId('link-github');
    expect(link.getAttribute('href')).toBe('/auth/link/github');
    // Already-linked gitea is NOT offered again.
    expect(screen.queryByTestId('link-gitea')).toBeNull();

    fireEvent.click(screen.getByTestId('sign-out'));
    expect(onSignOut).toHaveBeenCalledTimes(1);
  });

  // The shell docks the chip in the bottom rail: a downward menu would run past
  // the viewport (and the rail's overflow clips it), hiding Sign out.
  it('flips the menu above the chip when there is no room below it', () => {
    render(<IdentityChip me={userMe()} providers={PROVIDERS} role="cluster-admin" onSignOut={vi.fn()} />);
    const chip = screen.getByTestId('identity-chip');
    vi.spyOn(chip, 'getBoundingClientRect').mockReturnValue({
      // Sitting at the bottom of a 768px-tall jsdom viewport.
      top: window.innerHeight - 45,
      bottom: window.innerHeight - 15,
    } as DOMRect);

    fireEvent.click(chip);
    expect(screen.getByTestId('identity-menu').getAttribute('data-placement')).toBe('top');
  });

  it('keeps the menu below the chip when it fits (header placement)', () => {
    render(<IdentityChip me={userMe()} providers={PROVIDERS} role="cluster-admin" onSignOut={vi.fn()} />);
    const chip = screen.getByTestId('identity-chip');
    vi.spyOn(chip, 'getBoundingClientRect').mockReturnValue({ top: 8, bottom: 38 } as DOMRect);

    fireEvent.click(chip);
    expect(screen.getByTestId('identity-menu').getAttribute('data-placement')).toBe('bottom');
  });
});

describe('IdentityChip — service principal', () => {
  it('shows the console-token principal with no identities to link', () => {
    const me: Me = {
      user: { display_name: 'console token', is_cluster_admin: true },
      is_service: true,
      identities: [],
    };
    render(<IdentityChip me={me} providers={PROVIDERS} role="cluster-admin" onSignOut={vi.fn()} />);

    const chip = screen.getByTestId('identity-chip');
    expect(chip.getAttribute('data-service')).toBe('true');
    fireEvent.click(chip);
    // No link affordances for a service principal.
    expect(screen.queryByTestId('linkable-providers')).toBeNull();
    expect(screen.queryByTestId('link-github')).toBeNull();
  });
});

describe('IdentityChip — fallback (no principal)', () => {
  it('renders a plain role label when me is null', () => {
    render(<IdentityChip me={null} role="project-admin" />);
    const chip = screen.getByTestId('identity-chip');
    expect(chip.getAttribute('data-role')).toBe('project-admin');
    expect(chip.textContent).toContain('Project admin');
  });
});
