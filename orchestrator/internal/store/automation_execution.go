package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

const automationExecutionCols = `id,automation_id,automation_name,prompt_snapshot,project_id,service_id,
	trigger_kind,event_key,state,outcome,output_mode,reason_code,reason_message,repair_role,
	requested_actor,accountable_actor,run_id,external_url,card_automation_id,card_workspace_id,card_document_id,
	card_path,card_state,writeback_state,writeback_error,created_at,updated_at,terminal_at`

func normalizeAutomationExecution(value *domain.AutomationExecution) error {
	if value == nil || strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.AutomationID) == "" ||
		strings.TrimSpace(value.ProjectID) == "" || strings.TrimSpace(value.ServiceID) == "" ||
		strings.TrimSpace(value.EventKey) == "" {
		return errors.New("automation execution identity is required")
	}
	if !domain.ValidAutomationExecutionState(value.State) {
		return errors.New("invalid automation execution state")
	}
	if value.OutputMode == "" {
		value.OutputMode = domain.AutomationOutputRunOnly
	}
	if !domain.ValidAutomationOutputMode(value.OutputMode) {
		return errors.New("invalid automation execution output mode")
	}
	switch value.TriggerKind {
	case "scm", "cron", "manual":
	default:
		return errors.New("invalid automation execution trigger")
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	value.CreatedAt = value.CreatedAt.UTC()
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	value.UpdatedAt = value.UpdatedAt.UTC()
	return nil
}

func scanAutomationExecution(row pgx.Row) (*domain.AutomationExecution, error) {
	var value domain.AutomationExecution
	var requestedActorJSON, accountableActorJSON []byte
	err := row.Scan(
		&value.ID, &value.AutomationID, &value.AutomationName, &value.PromptSnapshot, &value.ProjectID, &value.ServiceID,
		&value.TriggerKind, &value.EventKey, &value.State, &value.Outcome, &value.OutputMode,
		&value.ReasonCode, &value.ReasonMessage, &value.RepairRole, &requestedActorJSON, &accountableActorJSON, &value.RunID,
		&value.ExternalURL, &value.CardAutomationID, &value.CardWorkspaceID, &value.CardDocumentID,
		&value.CardPath, &value.CardState, &value.WritebackState, &value.WritebackError,
		&value.CreatedAt, &value.UpdatedAt, &value.TerminalAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan automation execution: %w", err)
	}
	if len(requestedActorJSON) > 0 {
		if err := json.Unmarshal(requestedActorJSON, &value.RequestedActor); err != nil {
			return nil, fmt.Errorf("scan automation execution actor: %w", err)
		}
	}
	if len(accountableActorJSON) > 0 {
		if err := json.Unmarshal(accountableActorJSON, &value.AccountableActor); err != nil {
			return nil, fmt.Errorf("scan automation execution accountable actor: %w", err)
		}
	}
	return &value, nil
}

func insertAutomationExecutionTx(ctx context.Context, tx pgx.Tx, value *domain.AutomationExecution) (bool, error) {
	if err := normalizeAutomationExecution(value); err != nil {
		return false, err
	}
	requestedActorJSON, err := json.Marshal(value.RequestedActor)
	if err != nil {
		return false, fmt.Errorf("encode automation execution actor: %w", err)
	}
	accountableActorJSON, err := json.Marshal(value.AccountableActor)
	if err != nil {
		return false, fmt.Errorf("encode automation execution accountable actor: %w", err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO automation_executions (`+automationExecutionCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
		ON CONFLICT (automation_id,event_key) DO NOTHING`,
		value.ID, value.AutomationID, value.AutomationName, value.PromptSnapshot, value.ProjectID, value.ServiceID,
		value.TriggerKind, value.EventKey, value.State, value.Outcome, value.OutputMode,
		value.ReasonCode, value.ReasonMessage, value.RepairRole, requestedActorJSON, accountableActorJSON, value.RunID,
		value.ExternalURL, value.CardAutomationID, value.CardWorkspaceID, value.CardDocumentID,
		value.CardPath, value.CardState, value.WritebackState, value.WritebackError,
		value.CreatedAt, value.UpdatedAt, value.TerminalAt)
	if err != nil {
		return false, fmt.Errorf("insert automation execution: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGStore) CreateAutomationExecution(ctx context.Context, value *domain.AutomationExecution, run *domain.Run) (*domain.AutomationExecution, bool, error) {
	return s.createAutomationExecution(ctx, value, run, nil)
}

func (s *PGStore) CreateAutomationExecutionWithReviewStatus(ctx context.Context, value *domain.AutomationExecution, run *domain.Run, intent *domain.ReviewStatusComment) (*domain.AutomationExecution, bool, error) {
	return s.createAutomationExecution(ctx, value, run, intent)
}

func (s *PGStore) createAutomationExecution(ctx context.Context, value *domain.AutomationExecution, run *domain.Run, intent *domain.ReviewStatusComment) (*domain.AutomationExecution, bool, error) {
	if intent != nil {
		if err := validateReviewStatusIntent(value, run, intent); err != nil {
			return nil, false, err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("create automation execution: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var (
		currentStatus      *domain.ReviewStatusComment
		acceptedCursor     int64
		acceptedCursorHead string
	)
	if intent != nil {
		// Global Plugin mutation order: Project -> provider config -> installation -> Service ->
		// binding, then receipt/status rows. Uninstall follows installation -> Service;
		// taking the immutable acceptance grant first avoids both receipt and Service
		// FK lock inversions.
		if err := lockReviewStatusGrantTx(ctx, tx, intent, run.ProjectID); err != nil {
			return nil, false, err
		}
		activeReceipt, activeErr := lockWebhookReceiptClaimTx(ctx, tx, intent.AcceptedSequence, intent.ReceiptClaimToken)
		if activeErr != nil {
			return nil, false, fmt.Errorf("create automation execution: verify webhook receipt claim: %w", activeErr)
		}
		if !activeReceipt {
			return nil, false, nil
		}
		// The row may not exist yet, so use a key-scoped advisory lock in addition
		// to its unique constraint. This serializes first insert, later revisions,
		// and same-head cursor bumps before any execution or Run mutation.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"review-status:"+domain.ReviewStatusCommentMarker(intent.Key)); err != nil {
			return nil, false, fmt.Errorf("create automation execution: lock review status: %w", err)
		}
		args := reviewStatusKeyArgs(intent.Key)
		currentStatus, err = scanReviewStatusComment(tx.QueryRow(ctx, `SELECT `+reviewStatusCommentCols+`
			FROM review_status_comments
			WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4
			FOR UPDATE`, args...))
		if errors.Is(err, ErrNotFound) {
			currentStatus, err = nil, nil
		}
		if err != nil {
			return nil, false, err
		}
		cursorErr := tx.QueryRow(ctx, `SELECT head_sha,accepted_sequence
			FROM review_status_cursors
			WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4
			FOR UPDATE`, args...).Scan(&acceptedCursorHead, &acceptedCursor)
		if !errors.Is(cursorErr, pgx.ErrNoRows) && cursorErr != nil {
			return nil, false, fmt.Errorf("create automation execution: load review status cursor: %w", cursorErr)
		}
		if cursorErr == nil && acceptedCursor > intent.AcceptedSequence {
			return nil, false, nil
		}
		if currentStatus != nil && currentStatus.AcceptedSequence >= intent.AcceptedSequence {
			return nil, false, nil
		}
	}
	created, err := insertAutomationExecutionTx(ctx, tx, value)
	if err != nil {
		return nil, false, err
	}
	if !created && intent != nil && (currentStatus == nil || !strings.EqualFold(currentStatus.HeadSHA, intent.HeadSHA)) &&
		acceptedCursor == intent.AcceptedSequence && strings.EqualFold(acceptedCursorHead, intent.HeadSHA) {
		// A PR may legitimately return to an earlier SHA after a force-push. Its
		// head-derived event key then matches the first visit, but the monotonic
		// ingress sequence proves this is a new accepted revision and Run.
		returnEventKey := fmt.Sprintf("%s:return:%d", value.EventKey, intent.AcceptedSequence)
		value.EventKey = returnEventKey
		if run != nil {
			run.OriginEventKey = returnEventKey
		}
		created, err = insertAutomationExecutionTx(ctx, tx, value)
		if err != nil {
			return nil, false, err
		}
	}
	if !created {
		existing, getErr := scanAutomationExecution(tx.QueryRow(ctx,
			`SELECT `+automationExecutionCols+` FROM automation_executions WHERE automation_id=$1 AND event_key=$2`,
			value.AutomationID, value.EventKey))
		if getErr != nil {
			return nil, false, getErr
		}
		if intent != nil && currentStatus != nil &&
			strings.EqualFold(currentStatus.HeadSHA, intent.HeadSHA) &&
			acceptedCursor == intent.AcceptedSequence && strings.EqualFold(acceptedCursorHead, intent.HeadSHA) {
			args := append(reviewStatusKeyArgs(intent.Key), intent.AcceptedSequence)
			tag, bumpErr := tx.Exec(ctx, `UPDATE review_status_comments
				SET accepted_sequence=$5
				WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4
				  AND accepted_sequence<$5`, args...)
			if bumpErr != nil {
				return nil, false, fmt.Errorf("advance duplicate review status cursor: %w", bumpErr)
			}
			if tag.RowsAffected() != 1 {
				return nil, false, nil
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, false, fmt.Errorf("advance duplicate review status cursor: commit: %w", err)
			}
		}
		return existing, false, nil
	}
	if run != nil {
		normalizeRunForCreate(run)
		if run.CoalesceKey != "" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, run.CoalesceKey); err != nil {
				return nil, false, fmt.Errorf("create automation execution: lock coalesce key: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE runs
				SET status=$2,phase='Superseded',finished_at=COALESCE(finished_at,$3)
				WHERE coalesce_key=$1 AND status=$4`,
				run.CoalesceKey, domain.StatusCanceled, time.Now().UTC(), domain.StatusQueued); err != nil {
				return nil, false, fmt.Errorf("create automation execution: supersede queued: %w", err)
			}
		}
		if err := s.createRunTx(ctx, tx, run); err != nil {
			return nil, false, err
		}
	}
	if intent != nil {
		if err := freezeReviewStatusSnapshotTx(ctx, tx, intent); err != nil {
			return nil, false, err
		}
		advanced, upsertErr := upsertReviewStatusCommentTx(ctx, tx, intent)
		if upsertErr != nil {
			return nil, false, upsertErr
		}
		if !advanced {
			// A larger ingress sequence already owns this Service+PR. Rolling back
			// also discards the provisional execution, Run, and queued supersede.
			return nil, false, nil
		}
		args := append(reviewStatusKeyArgs(intent.Key), intent.HeadSHA, intent.AcceptedSequence, intent.UpdatedAt.UTC())
		if _, cursorErr := tx.Exec(ctx, `INSERT INTO review_status_cursors(
			service_id,provider,provider_repo_id,pr_number,head_sha,accepted_sequence,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(service_id,provider,provider_repo_id,pr_number) DO UPDATE SET
				head_sha=EXCLUDED.head_sha,accepted_sequence=EXCLUDED.accepted_sequence,updated_at=EXCLUDED.updated_at
			WHERE review_status_cursors.accepted_sequence<EXCLUDED.accepted_sequence`, args...); cursorErr != nil {
			return nil, false, fmt.Errorf("create automation execution: advance review status cursor: %w", cursorErr)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("create automation execution: commit: %w", err)
	}
	copyValue := *value
	return &copyValue, true, nil
}

func (s *PGStore) ClaimPluginCronExecution(
	ctx context.Context,
	automationID string,
	previous, firedAt *time.Time,
	execution *domain.AutomationExecution,
	run *domain.Run,
) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("claim cron execution: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE automation_cron_triggers
		SET last_fired_at=$3,last_error=$4
		WHERE automation_id=$1 AND last_fired_at IS NOT DISTINCT FROM $2`,
		automationID, previous, firedAt, execution.ReasonMessage)
	if err != nil {
		return false, fmt.Errorf("claim cron execution: advance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := insertAutomationExecutionTx(ctx, tx, execution); err != nil {
		return false, err
	}
	if run != nil {
		normalizeRunForCreate(run)
		if err := s.createRunTx(ctx, tx, run); err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE automations_v2
		SET last_triggered_at=$2,last_run_id=$3,last_error=$4,updated_at=$2
		WHERE id=$1`,
		automationID, firedAt, nullStr(execution.RunID), execution.ReasonMessage)
	if err != nil {
		return false, fmt.Errorf("claim cron execution: update automation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("claim cron execution: commit: %w", err)
	}
	return true, nil
}

func (s *PGStore) GetAutomationExecution(ctx context.Context, automationID, executionID string) (*domain.AutomationExecution, error) {
	return scanAutomationExecution(s.pool.QueryRow(ctx,
		`SELECT `+automationExecutionCols+` FROM automation_executions WHERE automation_id=$1 AND id=$2`,
		automationID, executionID))
}

func (s *PGStore) GetAutomationExecutionByEventKey(ctx context.Context, automationID, eventKey string) (*domain.AutomationExecution, error) {
	return scanAutomationExecution(s.pool.QueryRow(ctx,
		`SELECT `+automationExecutionCols+` FROM automation_executions WHERE automation_id=$1 AND event_key=$2`,
		automationID, eventKey))
}

func (s *PGStore) GetAutomationExecutionForKanbanOccurrence(
	ctx context.Context,
	occurrenceID string,
) (*domain.AutomationExecution, error) {
	return scanAutomationExecution(s.pool.QueryRow(ctx, `SELECT `+automationExecutionCols+`
		FROM automation_executions
		WHERE id=(
			SELECT e.id
			FROM automation_kanban_occurrences o
			JOIN automation_executions e
			  ON e.card_automation_id=o.automation_id
			 AND e.card_workspace_id=o.workspace_id
			 AND e.card_path=o.document_path
			WHERE o.id=$1
			ORDER BY e.created_at DESC,e.id DESC
			LIMIT 1
		)`, occurrenceID))
}

func (s *PGStore) ListAutomationExecutions(
	ctx context.Context,
	automationID, state string,
	beforeCreatedAt *time.Time,
	beforeID string,
	limit int,
) ([]domain.AutomationExecution, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `WITH projected AS (
			SELECT e.*,
				COALESCE(NULLIF(e.run_id,''),NULLIF(o.run_id,''),'') AS projected_run_id
			FROM automation_executions e
			LEFT JOIN automation_kanban_claims c
				ON c.automation_id=e.card_automation_id AND c.document_id=e.card_document_id
			LEFT JOIN automation_kanban_occurrences o
				ON o.id=c.latest_occurrence_id
		), filtered AS (
			SELECT p.*,r.status AS projected_run_status,r.phase AS projected_run_phase
			FROM projected p
			LEFT JOIN runs r ON r.id=p.projected_run_id
		)
		SELECT `+automationExecutionCols+`
		FROM filtered
		WHERE automation_id=$1
		  AND ($2='' OR (
		    CASE
		      WHEN projected_run_status='canceled' AND projected_run_phase='Superseded' THEN 'superseded'
		      WHEN projected_run_status IN ('succeeded','failed','canceled') THEN 'terminal'
		      WHEN projected_run_status IN ('scheduling','running','awaiting_input') THEN 'running'
		      WHEN projected_run_status IS NOT NULL THEN 'queued'
		      ELSE state
		    END
		  )=$2)
		  AND ($3::timestamptz IS NULL OR (created_at,id) < ($3,$4))
		ORDER BY created_at DESC,id DESC LIMIT $5`,
		automationID, state, beforeCreatedAt, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list automation executions: %w", err)
	}
	defer rows.Close()
	out := []domain.AutomationExecution{}
	for rows.Next() {
		value, err := scanAutomationExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *value)
	}
	return out, rows.Err()
}

func (s *PGStore) ListPendingAutomationCards(ctx context.Context, limit int) ([]domain.AutomationExecution, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+automationExecutionCols+`
		FROM automation_executions
		WHERE output_mode='create_card' AND card_state IN ('planned','creating')
		ORDER BY updated_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending automation cards: %w", err)
	}
	defer rows.Close()
	out := []domain.AutomationExecution{}
	for rows.Next() {
		value, err := scanAutomationExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *value)
	}
	return out, rows.Err()
}

func (s *PGStore) ClaimAutomationCardCreation(ctx context.Context, executionID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_executions
		SET card_state='creating',updated_at=now()
		WHERE id=$1 AND card_state='planned'`, executionID)
	if err != nil {
		return false, fmt.Errorf("claim automation card creation: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGStore) UpdateAutomationExecutionCard(
	ctx context.Context,
	executionID, cardState, cardAutomationID, workspaceID, documentID, documentPath,
	reasonCode, reasonMessage, repairRole string,
) error {
	state := domain.AutomationExecutionAccepted
	if reasonCode != "" {
		state = domain.AutomationExecutionBlocked
	}
	tag, err := s.pool.Exec(ctx, `UPDATE automation_executions SET
		state=$2,card_state=$3,card_automation_id=$4,card_workspace_id=$5,
		card_document_id=$6,card_path=$7,reason_code=$8,reason_message=$9,
		repair_role=$10,updated_at=now()
		WHERE id=$1`,
		executionID, state, cardState, cardAutomationID, workspaceID, documentID,
		documentPath, reasonCode, reasonMessage, repairRole)
	if err != nil {
		return fmt.Errorf("update automation execution card: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (m *MemStore) CreateAutomationExecution(_ context.Context, value *domain.AutomationExecution, run *domain.Run) (*domain.AutomationExecution, bool, error) {
	return m.createAutomationExecution(value, run, nil)
}

func (m *MemStore) CreateAutomationExecutionWithReviewStatus(_ context.Context, value *domain.AutomationExecution, run *domain.Run, intent *domain.ReviewStatusComment) (*domain.AutomationExecution, bool, error) {
	return m.createAutomationExecution(value, run, intent)
}

func (m *MemStore) createAutomationExecution(value *domain.AutomationExecution, run *domain.Run, intent *domain.ReviewStatusComment) (*domain.AutomationExecution, bool, error) {
	if intent != nil {
		if err := validateReviewStatusIntent(value, run, intent); err != nil {
			return nil, false, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		statusMapKey            string
		currentStatus           domain.ReviewStatusComment
		statusExists            bool
		currentCursor           reviewStatusCursor
		cursorExists            bool
		frozenReviewSnapshot    domain.RunPluginSnapshot
		hasFrozenReviewSnapshot bool
	)
	if intent != nil {
		statusMapKey = reviewStatusMapKey(intent.Key)
		currentStatus, statusExists = m.reviewStatusComments[statusMapKey]
		currentCursor, cursorExists = m.reviewStatusCursors[statusMapKey]
		if !m.webhookReceiptClaimActiveLocked(intent.AcceptedSequence, intent.ReceiptClaimToken) {
			return nil, false, nil
		}
		if cursorExists && currentCursor.AcceptedSequence > intent.AcceptedSequence {
			return nil, false, nil
		}
		if statusExists && currentStatus.AcceptedSequence >= intent.AcceptedSequence {
			return nil, false, nil
		}
	}
	if err := normalizeAutomationExecution(value); err != nil {
		return nil, false, err
	}
	var duplicate *domain.AutomationExecution
	for _, existing := range m.automationExecutions {
		if existing.AutomationID == value.AutomationID && existing.EventKey == value.EventKey {
			copyExisting := existing
			duplicate = &copyExisting
			break
		}
	}
	if duplicate != nil && intent != nil && (!statusExists || !strings.EqualFold(currentStatus.HeadSHA, intent.HeadSHA)) &&
		cursorExists && currentCursor.AcceptedSequence == intent.AcceptedSequence && strings.EqualFold(currentCursor.HeadSHA, intent.HeadSHA) {
		returnEventKey := fmt.Sprintf("%s:return:%d", value.EventKey, intent.AcceptedSequence)
		value.EventKey = returnEventKey
		if run != nil {
			run.OriginEventKey = returnEventKey
		}
		duplicate = nil
		for _, existing := range m.automationExecutions {
			if existing.AutomationID == value.AutomationID && existing.EventKey == value.EventKey {
				copyExisting := existing
				duplicate = &copyExisting
				break
			}
		}
	}
	if duplicate != nil {
		if intent != nil && statusExists && strings.EqualFold(currentStatus.HeadSHA, intent.HeadSHA) &&
			cursorExists && currentCursor.AcceptedSequence == intent.AcceptedSequence && strings.EqualFold(currentCursor.HeadSHA, intent.HeadSHA) {
			currentStatus.AcceptedSequence = intent.AcceptedSequence
			m.reviewStatusComments[statusMapKey] = currentStatus
			m.reviewStatusCursors[statusMapKey] = reviewStatusCursor{
				Key: intent.Key, HeadSHA: currentStatus.HeadSHA,
				AcceptedSequence: intent.AcceptedSequence, UpdatedAt: intent.UpdatedAt.UTC(),
			}
		}
		return duplicate, false, nil
	}
	if run != nil {
		normalizeRunForCreate(run)
		if err := m.validateRunForCreateLocked(run); err != nil {
			return nil, false, err
		}
		if err := m.resolveRunContractLocked(run); err != nil {
			return nil, false, err
		}
		if intent != nil {
			var snapshotErr error
			frozenReviewSnapshot, hasFrozenReviewSnapshot, snapshotErr = m.buildReviewStatusSnapshotLocked(run, intent)
			if snapshotErr != nil {
				return nil, false, snapshotErr
			}
		}
	}
	copyValue := *value
	m.automationExecutions[value.ID] = copyValue
	if run != nil {
		if run.CoalesceKey != "" {
			now := time.Now().UTC()
			for id, existing := range m.runs {
				if existing.CoalesceKey != run.CoalesceKey || existing.Status != domain.StatusQueued {
					continue
				}
				existing.Status = domain.StatusCanceled
				existing.Phase = "Superseded"
				if existing.FinishedAt == nil {
					finishedAt := now
					existing.FinishedAt = &finishedAt
				}
				m.runs[id] = existing
			}
		}
		m.insertRunLocked(run)
		if hasFrozenReviewSnapshot {
			if m.runPluginSnapshots[run.ID] == nil {
				m.runPluginSnapshots[run.ID] = map[string]domain.RunPluginSnapshot{}
			}
			m.runPluginSnapshots[run.ID][frozenReviewSnapshot.InstallationID] = frozenReviewSnapshot
		}
	}
	if intent != nil {
		if !m.upsertReviewStatusCommentLocked(intent) {
			panic("review status ingress sequence changed while MemStore lock was held")
		}
		m.reviewStatusCursors[statusMapKey] = reviewStatusCursor{
			Key: intent.Key, HeadSHA: intent.HeadSHA,
			AcceptedSequence: intent.AcceptedSequence, UpdatedAt: intent.UpdatedAt.UTC(),
		}
	}
	return &copyValue, true, nil
}

func (m *MemStore) ClaimPluginCronExecution(
	_ context.Context,
	automationID string,
	previous, firedAt *time.Time,
	execution *domain.AutomationExecution,
	run *domain.Run,
) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cron, ok := m.pluginCronTriggers[automationID]
	if !ok {
		return false, ErrNotFound
	}
	if (cron.LastFiredAt == nil) != (previous == nil) ||
		(cron.LastFiredAt != nil && !cron.LastFiredAt.Equal(*previous)) {
		return false, nil
	}
	if err := normalizeAutomationExecution(execution); err != nil {
		return false, err
	}
	if run != nil {
		normalizeRunForCreate(run)
		if err := m.validateRunForCreateLocked(run); err != nil {
			return false, err
		}
		if err := m.resolveRunContractLocked(run); err != nil {
			return false, err
		}
	}
	cron.LastFiredAt = firedAt
	cron.LastError = execution.ReasonMessage
	m.pluginCronTriggers[automationID] = cron
	m.automationExecutions[execution.ID] = *execution
	if run != nil {
		m.insertRunLocked(run)
	}
	if automation, ok := m.pluginAutomations[automationID]; ok {
		automation.LastTriggeredAt = firedAt
		automation.LastRunID = execution.RunID
		automation.LastError = execution.ReasonMessage
		automation.UpdatedAt = *firedAt
		m.pluginAutomations[automationID] = automation
	}
	return true, nil
}

func (m *MemStore) GetAutomationExecution(_ context.Context, automationID, executionID string) (*domain.AutomationExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.automationExecutions[executionID]
	if !ok || value.AutomationID != automationID {
		return nil, ErrNotFound
	}
	return &value, nil
}

func (m *MemStore) GetAutomationExecutionByEventKey(_ context.Context, automationID, eventKey string) (*domain.AutomationExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, value := range m.automationExecutions {
		if value.AutomationID == automationID && value.EventKey == eventKey {
			copyValue := value
			return &copyValue, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) GetAutomationExecutionForKanbanOccurrence(
	_ context.Context,
	occurrenceID string,
) (*domain.AutomationExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.automationExecutionForKanbanOccurrenceLocked(occurrenceID)
}

func (m *MemStore) automationExecutionForKanbanOccurrenceLocked(
	occurrenceID string,
) (*domain.AutomationExecution, error) {
	occurrence, ok := m.pluginKanbanOccurrences[occurrenceID]
	if !ok {
		return nil, ErrNotFound
	}
	var source *domain.AutomationExecution
	for _, value := range m.automationExecutions {
		if value.CardAutomationID != occurrence.AutomationID ||
			value.CardWorkspaceID != occurrence.WorkspaceID ||
			value.CardPath != occurrence.DocumentPath {
			continue
		}
		if source == nil || value.CreatedAt.After(source.CreatedAt) ||
			value.CreatedAt.Equal(source.CreatedAt) && value.ID > source.ID {
			copyValue := value
			source = &copyValue
		}
	}
	if source == nil {
		return nil, ErrNotFound
	}
	return source, nil
}

func (m *MemStore) ListAutomationExecutions(
	_ context.Context,
	automationID, state string,
	beforeCreatedAt *time.Time,
	beforeID string,
	limit int,
) ([]domain.AutomationExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := []domain.AutomationExecution{}
	for _, value := range m.automationExecutions {
		projectedState := m.projectAutomationExecutionStateLocked(value)
		if value.AutomationID != automationID || (state != "" && string(projectedState) != state) {
			continue
		}
		if beforeCreatedAt != nil && !(value.CreatedAt.Before(*beforeCreatedAt) ||
			(value.CreatedAt.Equal(*beforeCreatedAt) && value.ID < beforeID)) {
			continue
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) projectAutomationExecutionStateLocked(
	value domain.AutomationExecution,
) domain.AutomationExecutionState {
	if run, ok := m.runs[value.RunID]; ok {
		return projectedAutomationExecutionState(run)
	}
	if value.CardAutomationID == "" || value.CardDocumentID == "" {
		return value.State
	}
	claim, ok := m.pluginKanbanClaims[pluginKanbanClaimKey(value.CardAutomationID, value.CardDocumentID)]
	if !ok || claim.LatestOccurrenceID == "" {
		return value.State
	}
	occurrence, ok := m.pluginKanbanOccurrences[claim.LatestOccurrenceID]
	if !ok {
		return value.State
	}
	if run, ok := m.runs[occurrence.RunID]; ok {
		return projectedAutomationExecutionState(run)
	}
	return value.State
}

func projectedAutomationExecutionState(run domain.Run) domain.AutomationExecutionState {
	switch {
	case run.Status == domain.StatusCanceled && run.Phase == "Superseded":
		return domain.AutomationExecutionSuperseded
	case run.Status.Terminal():
		return domain.AutomationExecutionTerminal
	case run.Status == domain.StatusRunning || run.Status == domain.StatusAwaitingInput ||
		run.Status == domain.StatusScheduling:
		return domain.AutomationExecutionRunning
	default:
		return domain.AutomationExecutionQueued
	}
}

func (m *MemStore) ListPendingAutomationCards(_ context.Context, limit int) ([]domain.AutomationExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := []domain.AutomationExecution{}
	for _, value := range m.automationExecutions {
		if value.OutputMode == domain.AutomationOutputCreateCard &&
			(value.CardState == "planned" || value.CardState == "creating") {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ClaimAutomationCardCreation(_ context.Context, executionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.automationExecutions[executionID]
	if !ok {
		return false, ErrNotFound
	}
	if value.CardState != "planned" {
		return false, nil
	}
	value.CardState = "creating"
	value.UpdatedAt = time.Now().UTC()
	m.automationExecutions[executionID] = value
	return true, nil
}

func (m *MemStore) UpdateAutomationExecutionCard(
	_ context.Context,
	executionID, cardState, cardAutomationID, workspaceID, documentID, documentPath,
	reasonCode, reasonMessage, repairRole string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.automationExecutions[executionID]
	if !ok {
		return ErrNotFound
	}
	value.State = domain.AutomationExecutionAccepted
	if reasonCode != "" {
		value.State = domain.AutomationExecutionBlocked
	}
	value.CardState = cardState
	value.CardAutomationID = cardAutomationID
	value.CardWorkspaceID = workspaceID
	value.CardDocumentID = documentID
	value.CardPath = documentPath
	value.ReasonCode = reasonCode
	value.ReasonMessage = reasonMessage
	value.RepairRole = repairRole
	value.UpdatedAt = time.Now().UTC()
	m.automationExecutions[executionID] = value
	return nil
}
