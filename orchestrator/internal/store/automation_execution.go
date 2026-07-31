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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("create automation execution: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	created, err := insertAutomationExecutionTx(ctx, tx, value)
	if err != nil {
		return nil, false, err
	}
	if !created {
		existing, getErr := scanAutomationExecution(tx.QueryRow(ctx,
			`SELECT `+automationExecutionCols+` FROM automation_executions WHERE automation_id=$1 AND event_key=$2`,
			value.AutomationID, value.EventKey))
		if getErr != nil {
			return nil, false, getErr
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := normalizeAutomationExecution(value); err != nil {
		return nil, false, err
	}
	for _, existing := range m.automationExecutions {
		if existing.AutomationID == value.AutomationID && existing.EventKey == value.EventKey {
			copyValue := existing
			return &copyValue, false, nil
		}
	}
	if run != nil {
		normalizeRunForCreate(run)
		if err := m.validateRunForCreateLocked(run); err != nil {
			return nil, false, err
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
