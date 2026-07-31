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
import { Link } from 'react-router-dom';
import { JTypeApiError, JTypeBoard, type BoardLocale } from 'jtype-board-react';
import 'jtype-board-react/style.css';
import { useApi } from '../api/ApiProvider';
import {
  qk,
  useDeleteServiceKanban,
  usePluginBoards,
  useProjectPlugins,
  usePutServiceKanban,
  useServiceKanbanCardExecutions,
  useServiceKanbanPolicy,
} from '../api/queries';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { SelectField } from '../components/Field';
import { LoadingBlock } from '../components/States';
import { makeBoardProxyClient } from '../kanban/boardProxyClient';
import { resolveBoardPathById } from '../kanban/resolveBoardPathById';
import type { BoardEmbedLink, KanbanCardExecution, PluginBoardResource } from '../api/types';
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

function columnOptions(board?: PluginBoardResource) {
  return (board?.columns ?? []).map((column) => ({
    value: column.key,
    label: column.name || column.key,
  }));
}

function initialTriggerColumn(board?: PluginBoardResource): string {
  return board?.columns.find((column) => column.key === 'ai')?.key
    ?? board?.columns[0]?.key
    ?? '';
}

function initialDoneColumn(board?: PluginBoardResource): string {
  return board?.columns.find((column) => column.key === 'done')?.key ?? '';
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

function KanbanPolicyStrip({ serviceId }: { serviceId: string }) {
  const { t } = useTranslation();
  const policy = useServiceKanbanPolicy(serviceId, !!serviceId);
  if (!serviceId) return null;
  if (policy.isLoading) {
    return <div className={styles.policyStrip} aria-label={t('kanban.loadingPolicy')}>{t('kanban.loadingPolicy')}</div>;
  }
  if (policy.isError || !policy.data) {
    return (
      <div className={styles.policyStrip} data-state="blocked" role="alert">
        {t('kanban.policyUnavailable')}
        <Button type="button" variant="ghost" size="sm" onClick={() => void policy.refetch()}>
          {t('common.retry')}
        </Button>
      </div>
    );
  }
  const value = policy.data;
  const blocker = value.health.blocker
    ? t(`kanban.policyBlockers.${value.health.blocker}`, { defaultValue: value.health.blocker })
    : '';
  return (
    <div
      className={styles.policyStrip}
      data-state={value.health.state}
      data-testid="kanban-policy"
      role={value.health.state === 'blocked' ? 'alert' : 'status'}
    >
      <div className={styles.policyLead}>
        <strong>{value.service_name}</strong>
        <span>{value.repository}</span>
      </div>
      <span>
        {t('kanban.policyTrigger', {
          column: value.trigger_column.label || value.trigger_column.key,
        })}
      </span>
      <span>{value.model.label}</span>
      <span>
        {value.done_column.key
          ? t('kanban.policyWriteback', { column: value.done_column.label || value.done_column.key })
          : t('kanban.policyCommentOnly')}
      </span>
      <span className={styles.policyHealth}>
        {value.health.state === 'ready'
          ? t('kanban.policyReady')
          : (
            <>
              {t('kanban.policyBlocked', { blocker })}
              {value.health.repair_role === 'project_owner' && ` · ${t('kanban.projectOwner')}`}
              {value.health.repair_role === 'cluster_admin' && ` · ${t('kanban.clusterAdmin')}`}
            </>
          )}
      </span>
    </div>
  );
}

function executionStateLabel(execution: KanbanCardExecution, t: TFunction): string {
  if (execution.status === 'terminal' && execution.outcome) {
    return t(`kanban.executionState.${execution.outcome}`);
  }
  return t(`kanban.executionState.${execution.status}`);
}

function executionDescription(execution: KanbanCardExecution, t: TFunction): string {
  if (execution.status === 'blocked' && execution.reason_code) {
    return t(`kanban.policyBlockers.${execution.reason_code}`, {
      defaultValue: execution.reason ?? execution.summary,
    });
  }
  const key = execution.status === 'terminal' && execution.outcome
    ? execution.outcome
    : execution.status;
  return t(`kanban.executionSummary.${key}`, { defaultValue: execution.summary });
}

function CardExecutionsSupplement({
  serviceId,
  workspaceId,
  documentPath,
}: {
  serviceId: string;
  workspaceId: string;
  documentPath: string;
}) {
  const { t } = useTranslation();
  const query = useServiceKanbanCardExecutions(serviceId, workspaceId, documentPath);
  if (query.isLoading) {
    return <div className={styles.executionLoading} aria-label={t('kanban.loadingExecutions')}>{t('kanban.loadingExecutions')}</div>;
  }
  if (query.isError) {
    return (
      <div className={styles.executionError} role="alert">
        <span>{t('kanban.executionsUnavailable')}</span>
        <Button type="button" size="sm" variant="ghost" onClick={() => void query.refetch()}>
          {t('common.retry')}
        </Button>
      </div>
    );
  }
  const pages = query.data?.pages ?? [];
  const executions = pages.flatMap((page) => page.items);
  const claim = pages[0]?.claim ?? null;
  if (executions.length === 0) {
    return <p className={styles.executionEmpty}>{t('kanban.noExecutions')}</p>;
  }
  const current = executions[0]!;
  const history = executions.slice(1);
  return (
    <div className={styles.executions} aria-label={t('kanban.executionsTitle')}>
      {claim && !claim.external_ref_available && (
        <div className={styles.executionUnavailable} role="status">
          {t('kanban.cardUnavailable')}
        </div>
      )}
      <article
        className={styles.executionCurrent}
        data-state={current.status === 'terminal' ? current.outcome : current.status}
        data-testid="kanban-execution-current"
        role={current.status === 'blocked' ? 'alert' : 'status'}
      >
        <div className={styles.executionHeading}>
          <strong>{executionStateLabel(current, t)}</strong>
          <time dateTime={current.updated_at}>{new Date(current.updated_at).toLocaleString()}</time>
        </div>
        <p>{executionDescription(current, t)}</p>
        <div className={styles.executionMeta}>
          {current.requested_actor && (
            <span>{t('kanban.requestedByExternal', { actor: current.requested_actor.label })}</span>
          )}
          {current.repair_role === 'project_owner' && <span>{t('kanban.projectOwner')}</span>}
          {current.repair_role === 'cluster_admin' && <span>{t('kanban.clusterAdmin')}</span>}
          {current.run && <Link to={current.run.href}>{t('kanban.openRun')}</Link>}
          {current.receipt.writeback === 'pending' && <span>{t('kanban.writebackPending')}</span>}
          {current.receipt.writeback === 'unavailable' && <span>{t('kanban.writebackUnavailable')}</span>}
        </div>
      </article>
      {history.length > 0 && (
        <details className={styles.executionHistory}>
          <summary>{t('kanban.priorExecutions', { count: history.length })}</summary>
          <ol>
            {history.map((execution) => (
              <li key={execution.id}>
                <span>{executionStateLabel(execution, t)}</span>
                <time dateTime={execution.updated_at}>{new Date(execution.updated_at).toLocaleString()}</time>
                  {execution.run && <Link to={execution.run.href}>{t('kanban.openRun')}</Link>}
              </li>
            ))}
          </ol>
        </details>
      )}
      {query.hasNextPage && (
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetchingNextPage ? t('kanban.loadingExecutions') : t('kanban.loadEarlierExecutions')}
        </Button>
      )}
    </div>
  );
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
  const jtypePlugins = (plugins.data ?? []).filter((item) =>
    item.provider === 'jtype' && item.status === 'enabled' && item.workspace_id && item.id);
  const jtypePlugin = jtypePlugins.find((item) => !link || item.workspace_id === link.workspace_id)
    ?? jtypePlugins[0];
  const boards = usePluginBoards(
    projectId,
    jtypePlugin?.id ?? '',
    jtypePlugin?.workspace_id ?? '',
    !!jtypePlugin && (!link || canManage),
  );
  const putBinding = usePutServiceKanban(projectId, serviceId);
  const deleteBinding = useDeleteServiceKanban(projectId, serviceId);
  const [boardRef, setBoardRef] = useState('');
  const [triggerColumn, setTriggerColumn] = useState('');
  const [doneColumn, setDoneColumn] = useState('');
  const previewBoard = (boards.data ?? []).find((board) => board.id === boardRef);
  const linkedBoard = link
    ? (boards.data ?? []).find((board) => board.id === link.board_ref)
    : undefined;

  useEffect(() => {
    if (!boardRef && boards.data?.[0]?.id) setBoardRef(boards.data[0].id);
  }, [boardRef, boards.data]);

  useEffect(() => {
    if (links.length === 0) {
      if (selectedId) setSelectedId('');
      return;
    }
    if (!links.some((candidate) => candidate.id === selectedId)) {
      setSelectedId(initialLinkId(links));
    }
  }, [links, selectedId]);

  useEffect(() => {
    const board = link ? linkedBoard : previewBoard;
    if (!board) return;
    if (link) {
      setTriggerColumn(link.trigger_column || initialTriggerColumn(board));
      setDoneColumn(link.done_column ?? '');
      return;
    }
    setTriggerColumn(initialTriggerColumn(board));
    setDoneColumn(initialDoneColumn(board));
  }, [link, linkedBoard, previewBoard]);

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
          <div className={styles.setupPanel} data-testid="kanban-enable-panel">
            <div className={styles.setupHeading}>
              <div className={styles.setupTitle}>{t('kanban.enableTitle')}</div>
              <div className={styles.setupBody}>{t('kanban.enableBody')}</div>
            </div>
            {!jtypePlugin ? (
              <div className={styles.failDetail}>{t('kanban.pluginRequired')}</div>
            ) : boards.isLoading ? (
              <LoadingBlock label={t('kanban.loadingBoards')} />
            ) : boards.isError ? (
              <div className={styles.failDetail} role="alert">{t('kanban.boardsUnavailable')}</div>
            ) : (
              <>
                <SelectField
                  label={t('kanban.boardLabel')}
                  value={boardRef}
                  onChange={setBoardRef}
                  options={(boards.data ?? []).map((board) => ({ value: board.id, label: board.title }))}
                  data-testid="kanban-enable-board"
                />
                {previewBoard && (
                  <div className={styles.columnGrid}>
                    <SelectField
                      label={t('kanban.triggerColumn')}
                      value={triggerColumn}
                      onChange={setTriggerColumn}
                      options={columnOptions(previewBoard)}
                      data-testid="kanban-trigger-column"
                    />
                    <SelectField
                      label={t('kanban.doneColumn')}
                      value={doneColumn}
                      onChange={setDoneColumn}
                      options={[
                        { value: '', label: t('kanban.noDoneColumn') },
                        ...columnOptions(previewBoard),
                      ]}
                      data-testid="kanban-done-column"
                    />
                  </div>
                )}
              </>
            )}
            <div className={styles.setupActions}>
              <Button
                type="button"
                disabled={!canManage || !jtypePlugin || !previewBoard || !triggerColumn || putBinding.isPending}
                loading={putBinding.isPending}
                onClick={() => putBinding.mutate({
                  installation_id: jtypePlugin!.id!,
                  // Submit the document path so the server can resolve and
                  // validate the board, then persist its canonical config id.
                  board_ref: previewBoard!.ref,
                  trigger_column: triggerColumn,
                  done_column: doneColumn,
                  enabled: true,
                })}
                data-testid="kanban-enable"
              >
                {t('kanban.enable')}
              </Button>
            </div>
            {putBinding.isError && (
              <div className={styles.failDetail} role="alert">
                {(putBinding.error as Error).message}
              </div>
            )}
          </div>
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
        {link && serviceId && <KanbanPolicyStrip serviceId={serviceId} />}

        {!link ? null : resolved.isPending ? (
          <LoadingBlock label={t('kanban.openingBoard')} />
        ) : resolved.isError ? (
          <div className={styles.failPanel} role="alert" data-testid="kanban-board-fail">
            <div className={styles.failTitle}>{failure?.title}</div>
            <div className={styles.failMsg}>
              <strong>{linkLabel(link)}</strong> — {failure?.message}
            </div>
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
              <div className={styles.columnEditor} data-testid="kanban-column-editor">
                <div className={styles.columnEditorIntro}>
                  <strong>{t('kanban.columnSettings')}</strong>
                  <span>{t('kanban.columnSettingsHint')}</span>
                </div>
                {boards.isLoading ? (
                  <span className={styles.columnStatus}>{t('kanban.loadingBoards')}</span>
                ) : boards.isError || !jtypePlugin || !linkedBoard ? (
                  <span className={styles.columnStatus} role="alert">{t('kanban.columnsUnavailable')}</span>
                ) : (
                  <>
                    <SelectField
                      label={t('kanban.triggerColumn')}
                      value={triggerColumn}
                      onChange={setTriggerColumn}
                      options={columnOptions(linkedBoard)}
                      data-testid="kanban-trigger-column"
                    />
                    <SelectField
                      label={t('kanban.doneColumn')}
                      value={doneColumn}
                      onChange={setDoneColumn}
                      options={[
                        { value: '', label: t('kanban.noDoneColumn') },
                        ...columnOptions(linkedBoard),
                      ]}
                      data-testid="kanban-done-column"
                    />
                    <div className={styles.columnActions}>
                      <Button
                        type="button"
                        size="sm"
                        disabled={
                          !triggerColumn
                          || (
                            triggerColumn === link.trigger_column
                            && doneColumn === (link.done_column ?? '')
                          )
                          || putBinding.isPending
                        }
                        loading={putBinding.isPending}
                        onClick={() => putBinding.mutate({
                          installation_id: jtypePlugin.id!,
                          board_ref: linkedBoard.ref,
                          trigger_column: triggerColumn,
                          done_column: doneColumn,
                          enabled: true,
                        })}
                        data-testid="kanban-columns-save"
                      >
                        {t('common.save')}
                      </Button>
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
                  </>
                )}
                {putBinding.isError && (
                  <span className={styles.columnStatus} role="alert">
                    {(putBinding.error as Error).message}
                  </span>
                )}
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
                renderCardSupplement={serviceId ? (card) => (
                  <CardExecutionsSupplement
                    serviceId={serviceId}
                    workspaceId={link.workspace_id}
                    documentPath={card.id}
                  />
                ) : undefined}
              />
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
