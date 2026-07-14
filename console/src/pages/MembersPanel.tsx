/*
 * MembersPanel — the Members tab of the project settings modal (blueprint §2/§5).
 * Owners / cluster-admins see management controls: change a member's role, remove
 * a member, and add a member via a user-search dropdown + role. Everyone else
 * sees a read-only roster. The server enforces the same rules (403/409), so this
 * is UX, not authorization.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
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
  const { t } = useTranslation();
  const members = useMembers(projectId);
  const addMember = useAddMember(projectId);
  const removeMember = useRemoveMember(projectId);
  const toast = useToast();

  const [query, setQuery] = useState('');
  const [role, setRole] = useState<MemberRole>('member');
  const search = useSearchUsers(query);

  const roleLabel = (r: MemberRole) =>
    r === 'owner'
      ? t('members.roleOwner')
      : r === 'member'
        ? t('members.roleMember')
        : t('members.roleViewer');

  const existingIds = new Set((members.data ?? []).map((m) => m.user_id));

  const add = (user: UserSearchResult) => {
    addMember.mutate(
      { user_id: user.id, role },
      {
        onSuccess: (m) => {
          toast.push({
            kind: 'success',
            message: t('members.addedAs', { name: m.display_name, role: m.role }),
          });
          setQuery('');
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('members.addError'),
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
            message: err instanceof ApiError ? err.message : t('members.updateRoleError'),
          }),
      },
    );
  };

  const remove = (userId: string, name: string) => {
    removeMember.mutate(userId, {
      onSuccess: () => toast.push({ kind: 'info', message: t('members.removed', { name }) }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : t('members.removeError'),
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
              placeholder={t('members.searchPlaceholder')}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              data-testid="member-search-input"
              autoComplete="off"
              aria-label={t('members.searchAria')}
            />
            {query.trim() && (
              <div className={styles.results} role="listbox" data-testid="member-search-results">
                {search.isLoading ? (
                  <div className={styles.resultEmpty}>{t('members.searching')}</div>
                ) : (search.data ?? []).filter((u) => !existingIds.has(u.id)).length === 0 ? (
                  <div className={styles.resultEmpty}>{t('members.noMatches')}</div>
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
                        {u.is_cluster_admin && <span className={styles.resultTag}>{t('members.adminTag')}</span>}
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
            aria-label={t('members.roleForAddedAria')}
            options={ROLES.map((r) => ({ value: r, label: roleLabel(r) }))}
          />
        </div>
      )}

      {members.isLoading ? (
        <LoadingBlock label={t('members.loading')} />
      ) : members.isError ? (
        <ErrorBlock
          error={members.error}
          onRetry={() => members.refetch()}
          title={t('members.loadError')}
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
                    aria-label={t('members.roleForAria', { name: m.display_name })}
                    options={ROLES.map((r) => ({ value: r, label: roleLabel(r) }))}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => remove(m.user_id, m.display_name)}
                    data-testid="member-remove"
                    aria-label={t('members.removeAria', { name: m.display_name })}
                  >
                    {t('common.remove')}
                  </Button>
                </>
              ) : (
                <span className={styles.roleBadge}>{roleLabel(m.role)}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
