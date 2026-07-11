/*
 * MembersPanel — the Members tab of the project settings modal (blueprint §2/§5).
 * Owners / cluster-admins see management controls: change a member's role, remove
 * a member, and add a member via a user-search dropdown + role. Everyone else
 * sees a read-only roster. The server enforces the same rules (403/409), so this
 * is UX, not authorization.
 */
import { useState } from 'react';
import {
  useMembers,
  useAddMember,
  useRemoveMember,
  useSearchUsers,
} from '../api/queries';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { Button } from '../components/Button';
import { Select } from '../components/Select';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import type { MemberRole, UserSearchResult } from '../api/types';
import styles from './MembersPanel.module.css';

const ROLES: MemberRole[] = ['owner', 'member', 'viewer'];

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0]!.charAt(0).toUpperCase();
  return (parts[0]!.charAt(0) + parts[parts.length - 1]!.charAt(0)).toUpperCase();
}

function Avatar({ url, name }: { url?: string; name: string }) {
  if (url) return <img className={styles.avatar} src={url} alt="" aria-hidden />;
  return (
    <span className={styles.avatarFallback} aria-hidden>
      {initials(name)}
    </span>
  );
}

export function MembersPanel({
  projectId,
  canManage,
}: {
  projectId: string;
  canManage: boolean;
}) {
  const members = useMembers(projectId);
  const addMember = useAddMember(projectId);
  const removeMember = useRemoveMember(projectId);
  const toast = useToast();

  const [query, setQuery] = useState('');
  const [role, setRole] = useState<MemberRole>('member');
  const search = useSearchUsers(query);

  const existingIds = new Set((members.data ?? []).map((m) => m.user_id));

  const add = (user: UserSearchResult) => {
    addMember.mutate(
      { user_id: user.id, role },
      {
        onSuccess: (m) => {
          toast.push({ kind: 'success', message: `${m.display_name} added as ${m.role}.` });
          setQuery('');
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not add member.',
          }),
      },
    );
  };

  const changeRole = (userId: string, next: MemberRole) => {
    addMember.mutate(
      { user_id: userId, role: next },
      {
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not update role.',
          }),
      },
    );
  };

  const remove = (userId: string, name: string) => {
    removeMember.mutate(userId, {
      onSuccess: () => toast.push({ kind: 'info', message: `${name} removed.` }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not remove member.',
        }),
    });
  };

  return (
    <div className={styles.panel} data-testid="members-panel">
      {canManage && (
        <div className={styles.addRow} data-testid="member-add">
          <div className={styles.searchWrap}>
            <input
              className={styles.search}
              placeholder="Search people by name…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              data-testid="member-search-input"
              autoComplete="off"
              aria-label="Search people to add"
            />
            {query.trim() && (
              <div className={styles.results} role="listbox" data-testid="member-search-results">
                {search.isLoading ? (
                  <div className={styles.resultEmpty}>Searching…</div>
                ) : (search.data ?? []).filter((u) => !existingIds.has(u.id)).length === 0 ? (
                  <div className={styles.resultEmpty}>No matching people.</div>
                ) : (
                  (search.data ?? [])
                    .filter((u) => !existingIds.has(u.id))
                    .map((u) => (
                      <button
                        key={u.id}
                        type="button"
                        className={styles.result}
                        onClick={() => add(u)}
                        data-testid="member-search-result"
                        role="option"
                        aria-selected={false}
                      >
                        <Avatar url={u.avatar_url} name={u.display_name} />
                        <span className={styles.resultName}>{u.display_name}</span>
                        {u.is_cluster_admin && <span className={styles.resultTag}>admin</span>}
                      </button>
                    ))
                )}
              </div>
            )}
          </div>
          <Select
            className={styles.roleSelect}
            value={role}
            onChange={(value) => setRole(value as MemberRole)}
            data-testid="member-add-role"
            aria-label="Role for the added member"
            options={ROLES.map((r) => ({ value: r, label: r }))}
          />
        </div>
      )}

      {members.isLoading ? (
        <LoadingBlock label="Loading members…" />
      ) : members.isError ? (
        <ErrorBlock
          error={members.error}
          onRetry={() => members.refetch()}
          title="Couldn't load members"
        />
      ) : (
        <ul className={styles.list} data-testid="members-list">
          {(members.data ?? []).map((m) => (
            <li key={m.user_id} className={styles.member} data-testid="member-row" data-role={m.role}>
              <Avatar url={m.avatar_url} name={m.display_name} />
              <div className={styles.memberText}>
                <span className={styles.memberName}>{m.display_name}</span>
                {m.username && <span className={styles.memberSub}>{m.username}</span>}
              </div>
              {canManage ? (
                <>
                  <Select
                    className={styles.roleSelect}
                    value={m.role}
                    onChange={(value) => changeRole(m.user_id, value as MemberRole)}
                    data-testid="member-role-select"
                    aria-label={`Role for ${m.display_name}`}
                    options={ROLES.map((r) => ({ value: r, label: r }))}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => remove(m.user_id, m.display_name)}
                    data-testid="member-remove"
                    aria-label={`Remove ${m.display_name}`}
                  >
                    Remove
                  </Button>
                </>
              ) : (
                <span className={styles.roleBadge}>{m.role}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
