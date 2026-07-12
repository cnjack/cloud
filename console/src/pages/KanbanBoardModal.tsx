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
 *  - `live={false}`: we do NOT proxy SSE (an mcp-scoped token is 403'd on the
 *    live surface), so the board settles on visible polling — no fake-live.
 *
 * Fail-visible throughout (red line #1): a link that can't be resolved to a
 * board shows a clear panel, never a blank modal.
 */
import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { JTypeApiError, JTypeBoard, type BoardLocale } from 'jtype-board-react';
import 'jtype-board-react/style.css';
import { useApi } from '../api/ApiProvider';
import { qk } from '../api/queries';
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
function boardOpenErrorCopy(error: unknown): BoardOpenErrorCopy {
  const code = error instanceof JTypeApiError ? error.code : undefined;
  switch (code) {
    case 'kanban_not_configured':
      return {
        title: 'Kanban is not configured',
        message: 'A cluster administrator needs to configure the jtype integration before this board can open.',
      };
    case 'jtype_unreachable':
    case 'network_error':
      return {
        title: 'Kanban is unavailable',
        message: 'The jtype service could not be reached. Retry when the service is available.',
      };
    case 'jtype_unauthorized':
      return {
        title: 'Kanban access needs attention',
        message: 'The current Kanban connection cannot read this board. A project owner or cluster administrator needs to repair the integration.',
      };
    case 'workspace_not_found':
      return {
        title: 'Kanban workspace was not found',
        message: 'This board link points to a workspace that is no longer available to the project.',
      };
    case 'board_not_found':
      return {
        title: 'This board could not be opened',
        message: 'The board may have been deleted or renamed, or the integration lost access.',
      };
    default:
      return {
        title: 'This board could not be opened',
        message: 'The board could not be opened right now. Retry, or ask a project owner to check the board link.',
      };
  }
}

interface Props {
  projectId: string;
  links: BoardEmbedLink[];
  onClose: () => void;
}

export function KanbanBoardModal({ projectId, links, onClose }: Props) {
  const api = useApi();
  // Memoize the injected client: a new identity per render restarts the board.
  const proxyClient = useMemo(
    () => makeBoardProxyClient(api, projectId),
    [api, projectId],
  );

  const [selectedId, setSelectedId] = useState(() => initialLinkId(links));
  const link = links.find((l) => l.id === selectedId) ?? links[0];

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
  const failure = resolved.isError ? boardOpenErrorCopy(resolved.error) : null;

  return (
    <Modal
      open
      title="Kanban"
      onClose={onClose}
      size="wide"
      data-testid="kanban-board-modal"
    >
      <div className={styles.wrap}>
        {links.length > 1 && (
          <div className={styles.selectorRow}>
            <SelectField
              label="Board"
              className={styles.selector}
              value={selectedId}
              onChange={setSelectedId}
              options={links.map((l) => ({ value: l.id, label: linkLabel(l) }))}
              data-testid="kanban-board-select"
            />
          </div>
        )}

        {!link ? null : resolved.isPending ? (
          <LoadingBlock label="Opening board…" />
        ) : resolved.isError ? (
          <div className={styles.failPanel} role="alert" data-testid="kanban-board-fail">
            <div className={styles.failTitle}>{failure?.title}</div>
            <div className={styles.failMsg}>
              <strong>{linkLabel(link)}</strong> — {failure?.message}
            </div>
            {link.board_status === 'invalid' && (
              <div className={styles.failDetail}>
                The link is marked invalid — its board or columns last failed validation.
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
                Retry
              </Button>
            </div>
          </div>
        ) : (
          <>
            {link.board_status === 'invalid' && (
              <div
                className={styles.linkNotice}
                role="alert"
                data-state="invalid"
                data-testid="kanban-board-link-invalid"
              >
                <strong>This Kanban automation link is invalid.</strong> Card-triggered runs and
                writeback are stopped until a project owner repairs the board or column mapping.
                Viewing the board remains available.
              </div>
            )}
            {link.board_status === 'unvalidated' && (
              <div
                className={styles.linkNotice}
                role="status"
                data-state="unvalidated"
                data-testid="kanban-board-link-unvalidated"
              >
                <strong>This Kanban automation link has not been validated.</strong> Its
                card-triggered runs and writeback cannot be confirmed until an owner verifies the
                board connection and column mapping.
              </div>
            )}
            {!link.enabled && (
              <div
                className={styles.linkNotice}
                role="status"
                data-state="disabled"
                data-testid="kanban-board-link-disabled"
              >
                <strong>This Kanban automation link is disabled.</strong> The board is viewable,
                but card-triggered runs and writeback are disabled.
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
