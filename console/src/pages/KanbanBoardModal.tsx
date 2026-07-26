/*
 * KanbanBoardModal — embeds the real jtype kanban board in the project page (D31).
 *
 * Opened from the "Kanban" header button when the project has ≥1 board link.
 * Renders the published `jtype-board-react` board (columns + cards + drag) with
 * the jtype token kept SERVER-SIDE: the board runs on an injected proxy client
 * (`makeBoardProxyClient`) whose every request hits the jcloud member+ board
 * proxy, so no token ever reaches the browser.
 *
 * Two id/name bridges the board needs:
 *  - the link's `board_ref` is a `config.id` (`b_…`), but `<JTypeBoard boardRef>`
 *    wants a name / `.board` relativePath — we resolve it via
 *    `resolveBoardPathById` (over the same proxy) before rendering.
 *  - `live={false}`: the server-side proxy does not expose SSE, so the board
 *    settles on visible polling — no fake-live and no token in the browser.
 *
 * Fail-visible throughout (red line #1): a link that can't be resolved to a
 * board shows a clear panel, never a blank modal.
 */
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useQuery } from '@tanstack/react-query';
import { JTypeApiError, JTypeBoard, type BoardLocale } from 'jtype-board-react';
import 'jtype-board-react/style.css';
import { useApi } from '../api/ApiProvider';
import {
  qk,
  useDeleteServiceKanban,
  usePluginBoards,
  useProjectPlugins,
  usePutServiceKanban,
} from '../api/queries';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { SelectField } from '../components/Field';
import { LoadingBlock } from '../components/States';
import { makeBoardProxyClient } from '../kanban/boardProxyClient';
import { resolveBoardPathById } from '../kanban/resolveBoardPathById';
import type { BoardEmbedLink } from '../api/types';
import styles from './KanbanBoardModal.module.css';

/** Map the browser locale to a board-supported one; default 'en'. */
function boardLocale(): BoardLocale {
  const lang = (typeof navigator !== 'undefined' ? navigator.language : 'en')
    .slice(0, 2)
    .toLowerCase();
  return lang === 'zh' || lang === 'ja' || lang === 'ko' ? (lang as BoardLocale) : 'en';
}

function linkLabel(link: BoardEmbedLink): string {
  return link.board_title ?? link.board_ref;
}

/** Pick the initial link: the first enabled one, else the first. */
function initialLinkId(links: BoardEmbedLink[]): string {
  return (links.find((l) => l.enabled) ?? links[0])?.id ?? '';
}

interface BoardOpenErrorCopy {
  title: string;
  message: string;
}

/**
 * The board proxy preserves typed server error codes. Map them to useful,
 * non-sensitive guidance rather than collapsing every outage into a misleading
 * “deleted or renamed” message.
 */
function boardOpenErrorCopy(error: unknown, t: TFunction): BoardOpenErrorCopy {
  const code = error instanceof JTypeApiError ? error.code : undefined;
  switch (code) {
    case 'kanban_not_configured':
      return {
        title: t('kanban.notConfiguredTitle'),
        message: t('kanban.notConfiguredMsg'),
      };
    case 'jtype_unreachable':
    case 'network_error':
      return {
        title: t('kanban.unavailableTitle'),
        message: t('kanban.unavailableMsg'),
      };
    case 'jtype_unauthorized':
      return {
        title: t('kanban.unauthorizedTitle'),
        message: t('kanban.unauthorizedMsg'),
      };
    case 'workspace_not_found':
      return {
        title: t('kanban.workspaceNotFoundTitle'),
        message: t('kanban.workspaceNotFoundMsg'),
      };
    case 'board_not_found':
      return {
        title: t('kanban.boardNotFoundTitle'),
        message: t('kanban.boardNotFoundMsg'),
      };
    default:
      return {
        title: t('kanban.boardNotFoundTitle'),
        message: t('kanban.boardOpenDefaultMsg'),
      };
  }
}

interface Props {
  projectId: string;
  serviceId?: string;
  links: BoardEmbedLink[];
  canManage?: boolean;
  onClose: () => void;
}

export function KanbanBoardModal({ projectId, serviceId = '', links, canManage = false, onClose }: Props) {
  const { t } = useTranslation();
  const api = useApi();
  // Memoize the injected client: a new identity per render restarts the board.
  const proxyClient = useMemo(
    () => makeBoardProxyClient(api, projectId),
    [api, projectId],
  );

  const [selectedId, setSelectedId] = useState(() => initialLinkId(links));
  const link = links.find((l) => l.id === selectedId) ?? links[0];
  const plugins = useProjectPlugins(projectId);
  const jtypePlugin = (plugins.data ?? []).find((item) =>
    item.provider === 'jtype' && item.status === 'enabled' && item.workspace_id && item.id);
  const boards = usePluginBoards(
    projectId,
    jtypePlugin?.id ?? '',
    jtypePlugin?.workspace_id ?? '',
    !link && !!jtypePlugin,
  );
  const putBinding = usePutServiceKanban(projectId, serviceId);
  const deleteBinding = useDeleteServiceKanban(projectId, serviceId);
  const [boardRef, setBoardRef] = useState('');
  const previewBoard = (boards.data ?? []).find((board) => board.id === boardRef);

  useEffect(() => {
    if (!boardRef && boards.data?.[0]?.id) setBoardRef(boards.data[0].id);
  }, [boardRef, boards.data]);

  // Resolve the link's board_ref (config id) → the board's relativePath, over
  // the member+ proxy. Keyed on the selected link so switching boards refetches.
  const resolved = useQuery({
    queryKey: [...qk.projectBoardLinks(projectId), 'resolve', link?.workspace_id, link?.board_ref],
    queryFn: () => resolveBoardPathById(proxyClient, link!.workspace_id, link!.board_ref),
    enabled: !!link,
    retry: false,
    // The board doc set is stable across a modal session; don't refetch on focus.
    staleTime: 60_000,
  });
  const failure = resolved.isError ? boardOpenErrorCopy(resolved.error, t) : null;

  return (
    <Modal
      open
      title={t('kanban.title')}
      onClose={onClose}
      size="wide"
      data-testid="kanban-board-modal"
    >
      <div className={styles.wrap}>
        {!link && (
          <>
            <div className={styles.failPanel} data-testid="kanban-enable-panel">
              <div className={styles.failTitle}>{t('kanban.enableTitle')}</div>
              <div className={styles.failMsg}>{t('kanban.enableBody')}</div>
              {!jtypePlugin ? (
                <div className={styles.failDetail}>{t('kanban.pluginRequired')}</div>
              ) : boards.isLoading ? (
                <LoadingBlock label={t('kanban.loadingBoards')} />
              ) : boards.isError ? (
                <div className={styles.failDetail} role="alert">{t('kanban.boardsUnavailable')}</div>
              ) : (
                <SelectField
                  label={t('kanban.boardLabel')}
                  value={boardRef}
                  onChange={setBoardRef}
                  options={(boards.data ?? []).map((board) => ({ value: board.id, label: board.title }))}
                  data-testid="kanban-enable-board"
                />
              )}
              <div className={styles.failActions}>
                <Button
                  type="button"
                  disabled={!canManage || !jtypePlugin || !boardRef || putBinding.isPending}
                  loading={putBinding.isPending}
                  onClick={() => putBinding.mutate({
                    installation_id: jtypePlugin!.id!,
                    board_ref: boardRef,
                    enabled: true,
                  })}
                  data-testid="kanban-enable"
                >
                  {t('kanban.enable')}
                </Button>
              </div>
              {putBinding.isError && <div className={styles.failDetail} role="alert">{(putBinding.error as Error).message}</div>}
            </div>
            {jtypePlugin && previewBoard && (
              <div className={styles.board} data-testid="kanban-readonly-preview">
                <JTypeBoard
                  client={proxyClient}
                  workspaceId={jtypePlugin.workspace_id!}
                  boardRef={previewBoard.ref}
                  readOnly
                  live={false}
                  locale={boardLocale()}
                />
              </div>
            )}
          </>
        )}
        {links.length > 1 && (
          <div className={styles.selectorRow}>
            <SelectField
              label={t('kanban.boardLabel')}
              className={styles.selector}
              value={selectedId}
              onChange={setSelectedId}
              options={links.map((l) => ({ value: l.id, label: linkLabel(l) }))}
              data-testid="kanban-board-select"
            />
          </div>
        )}

        {!link ? null : resolved.isPending ? (
          <LoadingBlock label={t('kanban.openingBoard')} />
        ) : resolved.isError ? (
          <div className={styles.failPanel} role="alert" data-testid="kanban-board-fail">
            <div className={styles.failTitle}>{failure?.title}</div>
            <div className={styles.failMsg}>
              <strong>{linkLabel(link)}</strong> — {failure?.message}
            </div>
            {link.board_status === 'invalid' && (
              <div className={styles.failDetail}>
                {t('kanban.linkMarkedInvalid')}
              </div>
            )}
            <div className={styles.failActions}>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => void resolved.refetch()}
                data-testid="kanban-board-retry"
              >
                {t('common.retry')}
              </Button>
            </div>
          </div>
        ) : (
          <>
            {canManage && (
              <div className={styles.selectorRow}>
                <span>{t('kanban.defaultColumns')}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  loading={deleteBinding.isPending}
                  onClick={() => deleteBinding.mutate(undefined, { onSuccess: onClose })}
                >
                  {t('kanban.disable')}
                </Button>
              </div>
            )}
            {link.board_status === 'invalid' && (
              <div
                className={styles.linkNotice}
                role="alert"
                data-state="invalid"
                data-testid="kanban-board-link-invalid"
              >
                <strong>{t('kanban.linkInvalidTitle')}</strong> {t('kanban.linkInvalidBody')}
              </div>
            )}
            {link.board_status === 'unvalidated' && (
              <div
                className={styles.linkNotice}
                role="status"
                data-state="unvalidated"
                data-testid="kanban-board-link-unvalidated"
              >
                <strong>{t('kanban.linkUnvalidatedTitle')}</strong> {t('kanban.linkUnvalidatedBody')}
              </div>
            )}
            {!link.enabled && (
              <div
                className={styles.linkNotice}
                role="status"
                data-state="disabled"
                data-testid="kanban-board-link-disabled"
              >
                <strong>{t('kanban.linkDisabledTitle')}</strong> {t('kanban.linkDisabledBody')}
              </div>
            )}
            <div className={styles.board}>
              <JTypeBoard
                client={proxyClient}
                workspaceId={link.workspace_id}
                boardRef={resolved.data}
                live={false}
                locale={boardLocale()}
              />
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
