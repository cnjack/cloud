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
import { JTypeBoard, type BoardLocale } from 'jtype-board-react';
import 'jtype-board-react/style.css';
import { useApi } from '../api/ApiProvider';
import { qk } from '../api/queries';
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

  return (
    <Modal open title="Kanban" onClose={onClose} data-testid="kanban-board-modal">
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
            <div className={styles.failTitle}>This board could not be opened</div>
            <div className={styles.failMsg}>
              The board <strong>{linkLabel(link)}</strong> could not be opened in jtype —
              it may have been deleted or renamed, or the integration lost access.
            </div>
            {link.board_status === 'invalid' && (
              <div className={styles.failDetail}>
                The link is marked invalid — its board or columns last failed validation.
              </div>
            )}
          </div>
        ) : (
          <div className={styles.board}>
            <JTypeBoard
              client={proxyClient}
              workspaceId={link.workspace_id}
              boardRef={resolved.data}
              live={false}
              locale={boardLocale()}
            />
          </div>
        )}
      </div>
    </Modal>
  );
}
