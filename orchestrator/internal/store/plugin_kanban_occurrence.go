package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	PluginKanbanAlreadyInTrigger = "already_in_trigger"
	PluginKanbanAlreadyRunning   = "already_running"
	PluginKanbanWritebackPending = "writeback_pending"
	PluginKanbanOccurrenceActive = "occurrence_active"
)

type PluginKanbanObservation struct {
	AutomationID   string
	ServiceID      string
	InstallationID string
	WorkspaceID    string
	DocumentID     string
	DocumentPath   string
	TriggerColumn  string
	DoneColumn     string
	ObservedColumn string
	EventKey       string
	EventSequence  *int64
	ActorDisplay   string
	ObservedAt     time.Time
}

type PluginKanbanObservationResult struct {
	Claim            domain.PluginKanbanClaim
	Occurrence       *domain.PluginKanbanOccurrence
	Created          bool
	SuppressedReason string
}

// PluginKanbanOccurrenceCursor is the decoded store-facing half of the Card
// execution API's opaque cursor. Ordering is always (created_at DESC, id DESC).
type PluginKanbanOccurrenceCursor struct {
	CreatedAt time.Time
	ID        string
}

func validatePluginKanbanObservation(in PluginKanbanObservation) error {
	if strings.TrimSpace(in.AutomationID) == "" || strings.TrimSpace(in.ServiceID) == "" ||
		strings.TrimSpace(in.InstallationID) == "" || strings.TrimSpace(in.WorkspaceID) == "" ||
		strings.TrimSpace(in.DocumentID) == "" || strings.TrimSpace(in.DocumentPath) == "" ||
		strings.TrimSpace(in.TriggerColumn) == "" || strings.TrimSpace(in.EventKey) == "" {
		return errors.New("invalid Kanban observation")
	}
	if in.ObservedAt.IsZero() {
		return errors.New("Kanban observation time is required")
	}
	return nil
}

func classifyPluginKanbanEntry(
	claimExists bool,
	claim *domain.PluginKanbanClaim,
	latest *domain.PluginKanbanOccurrence,
	latestRunStatus domain.RunStatus,
	in PluginKanbanObservation,
) (create bool, suppressed string) {
	if in.ObservedColumn != in.TriggerColumn {
		return false, ""
	}
	if !claimExists || latest == nil {
		return true, ""
	}
	if claim.LastObservedColumn == in.TriggerColumn {
		return false, PluginKanbanAlreadyInTrigger
	}

	terminal := latest.State == domain.KanbanOccurrenceTerminal || latestRunStatus.Terminal()
	if !terminal {
		if latest.RunID != "" {
			return false, PluginKanbanAlreadyRunning
		}
		return false, PluginKanbanOccurrenceActive
	}
	if latest.WritebackState != "complete" && claim.WritebackAt == nil {
		return false, PluginKanbanWritebackPending
	}
	if claim.OutsideTriggerAt == nil {
		return false, PluginKanbanAlreadyInTrigger
	}
	return true, ""
}

func newPluginKanbanClaim(in PluginKanbanObservation) domain.PluginKanbanClaim {
	return domain.PluginKanbanClaim{
		AutomationID: in.AutomationID, InstallationID: in.InstallationID,
		DocumentID: in.DocumentID, DocumentPath: in.DocumentPath,
		WorkspaceID: in.WorkspaceID, DoneColumn: in.DoneColumn,
		ExternalRefAvailable: true, CreatedAt: in.ObservedAt, UpdatedAt: in.ObservedAt,
	}
}

func newPluginKanbanOccurrence(in PluginKanbanObservation) domain.PluginKanbanOccurrence {
	return domain.PluginKanbanOccurrence{
		ID: domain.NewID(), AutomationID: in.AutomationID, ServiceID: in.ServiceID,
		InstallationID: in.InstallationID, WorkspaceID: in.WorkspaceID,
		DocumentID: in.DocumentID, DocumentPath: in.DocumentPath, DoneColumn: in.DoneColumn,
		EventKey: in.EventKey, EventSequence: in.EventSequence, ActorDisplay: in.ActorDisplay,
		EntryColumn: in.TriggerColumn, State: domain.KanbanOccurrenceReceived,
		WritebackState: "pending", CreatedAt: in.ObservedAt, UpdatedAt: in.ObservedAt,
	}
}

func (m *MemStore) ObservePluginKanbanCard(_ context.Context, in PluginKanbanObservation) (*PluginKanbanObservationResult, error) {
	if err := validatePluginKanbanObservation(in); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, occurrence := range m.pluginKanbanOccurrences {
		if occurrence.AutomationID == in.AutomationID && occurrence.EventKey == in.EventKey {
			claim := m.pluginKanbanClaims[pluginKanbanClaimKey(in.AutomationID, in.DocumentID)]
			copy := occurrence
			return &PluginKanbanObservationResult{Claim: claim, Occurrence: &copy}, nil
		}
	}

	key := pluginKanbanClaimKey(in.AutomationID, in.DocumentID)
	claim, claimExists := m.pluginKanbanClaims[key]
	if !claimExists {
		if _, ok := m.pluginKanbanTriggers[in.AutomationID]; !ok {
			return nil, ErrNotFound
		}
		claim = newPluginKanbanClaim(in)
	}
	var latest *domain.PluginKanbanOccurrence
	var latestRunStatus domain.RunStatus
	if claim.LatestOccurrenceID != "" {
		if value, ok := m.pluginKanbanOccurrences[claim.LatestOccurrenceID]; ok {
			copy := value
			latest = &copy
			if copy.RunID != "" {
				latestRunStatus = m.runs[copy.RunID].Status
			}
		}
	}

	create, suppressed := classifyPluginKanbanEntry(claimExists, &claim, latest, latestRunStatus, in)
	claim.DocumentPath = in.DocumentPath
	claim.WorkspaceID = in.WorkspaceID
	claim.DoneColumn = in.DoneColumn
	claim.LastObservedColumn = in.ObservedColumn
	claim.ExternalRefAvailable = true
	claim.UpdatedAt = in.ObservedAt
	if in.ObservedColumn != in.TriggerColumn {
		at := in.ObservedAt
		claim.OutsideTriggerAt = &at
		m.pluginKanbanClaims[key] = claim
		return &PluginKanbanObservationResult{Claim: claim}, nil
	}
	claim.OutsideTriggerAt = nil
	if !create {
		m.pluginKanbanClaims[key] = claim
		result := &PluginKanbanObservationResult{Claim: claim, SuppressedReason: suppressed}
		if suppressed == PluginKanbanAlreadyRunning || suppressed == PluginKanbanWritebackPending || suppressed == PluginKanbanOccurrenceActive {
			result.Occurrence = latest
		}
		return result, nil
	}

	occurrence := newPluginKanbanOccurrence(in)
	m.pluginKanbanOccurrences[occurrence.ID] = occurrence
	claim.LatestOccurrenceID = occurrence.ID
	claim.RunID = ""
	claim.WritebackAt = nil
	m.pluginKanbanClaims[key] = claim
	return &PluginKanbanObservationResult{Claim: claim, Occurrence: &occurrence, Created: true}, nil
}

func (m *MemStore) CreatePluginKanbanOccurrenceRun(_ context.Context, occurrenceID string, run *domain.Run) (bool, error) {
	if run == nil {
		return false, errors.New("run is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	occurrence, ok := m.pluginKanbanOccurrences[occurrenceID]
	if !ok {
		return false, ErrNotFound
	}
	if occurrence.RunID != "" {
		return false, nil
	}
	normalizeRunForCreate(run)
	if err := m.validateRunForCreateLocked(run); err != nil {
		return false, err
	}
	if err := m.resolveRunContractLocked(run); err != nil {
		return false, err
	}
	m.insertRunLocked(run)
	now := time.Now().UTC()
	occurrence.RunID = run.ID
	occurrence.State = domain.KanbanOccurrenceQueued
	occurrence.ReasonCode = ""
	occurrence.ReasonMessage = ""
	occurrence.RepairRole = ""
	occurrence.ReceiptPhase = "accepted"
	occurrence.ReceiptWrittenAt = nil
	occurrence.WritebackState = "pending"
	occurrence.WritebackError = ""
	occurrence.UpdatedAt = now
	m.pluginKanbanOccurrences[occurrenceID] = occurrence
	key := pluginKanbanClaimKey(occurrence.AutomationID, occurrence.DocumentID)
	claim := m.pluginKanbanClaims[key]
	claim.RunID = run.ID
	claim.WritebackAt = nil
	claim.LatestOccurrenceID = occurrenceID
	claim.UpdatedAt = now
	m.pluginKanbanClaims[key] = claim
	return true, nil
}

func (m *MemStore) SetPluginKanbanOccurrenceBlocked(_ context.Context, occurrenceID, reasonCode, reasonMessage, repairRole string) (*domain.PluginKanbanOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	occurrence, ok := m.pluginKanbanOccurrences[occurrenceID]
	if !ok {
		return nil, ErrNotFound
	}
	if occurrence.RunID != "" {
		return nil, ErrConflict
	}
	if occurrence.ReceiptPhase != "blocked" ||
		occurrence.ReasonCode != reasonCode ||
		occurrence.RepairRole != repairRole {
		occurrence.ReceiptWrittenAt = nil
	}
	occurrence.State = domain.KanbanOccurrenceBlocked
	occurrence.ReasonCode = reasonCode
	occurrence.ReasonMessage = reasonMessage
	occurrence.RepairRole = repairRole
	occurrence.ReceiptPhase = "blocked"
	occurrence.WritebackState = "not_required"
	occurrence.WritebackError = ""
	occurrence.UpdatedAt = time.Now().UTC()
	m.pluginKanbanOccurrences[occurrenceID] = occurrence
	return &occurrence, nil
}

func sortPluginKanbanOccurrences(out []domain.PluginKanbanOccurrence) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
}

func sortPluginKanbanOccurrencesOldestFirst(out []domain.PluginKanbanOccurrence) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
}

func (m *MemStore) ListPluginKanbanDispatchableOccurrences(_ context.Context, automationID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := make([]domain.PluginKanbanOccurrence, 0)
	for _, occurrence := range m.pluginKanbanOccurrences {
		if occurrence.AutomationID == automationID && occurrence.RunID == "" &&
			(occurrence.State == domain.KanbanOccurrenceReceived || occurrence.State == domain.KanbanOccurrenceBlocked) {
			out = append(out, occurrence)
		}
	}
	sortPluginKanbanOccurrencesOldestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListPluginKanbanReceiptPending(_ context.Context, automationID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := make([]domain.PluginKanbanOccurrence, 0)
	for _, occurrence := range m.pluginKanbanOccurrences {
		if occurrence.AutomationID == automationID &&
			pluginKanbanReceiptPhaseRetryable(occurrence.ReceiptPhase) &&
			occurrence.ReceiptWrittenAt == nil {
			out = append(out, occurrence)
		}
	}
	sortPluginKanbanOccurrencesOldestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func pluginKanbanReceiptPhaseRetryable(phase string) bool {
	switch phase {
	case "accepted", "blocked", PluginKanbanAlreadyRunning, PluginKanbanWritebackPending:
		return true
	default:
		return false
	}
}

func (m *MemStore) SetPluginKanbanOccurrenceReceiptPhase(
	_ context.Context,
	occurrenceID, phase string,
) (*domain.PluginKanbanOccurrence, error) {
	if !pluginKanbanReceiptPhaseRetryable(phase) {
		return nil, errors.New("invalid Kanban receipt phase")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	occurrence, ok := m.pluginKanbanOccurrences[occurrenceID]
	if !ok {
		return nil, ErrNotFound
	}
	if occurrence.ReceiptPhase != phase {
		occurrence.ReceiptPhase = phase
		occurrence.ReceiptWrittenAt = nil
		occurrence.WritebackError = ""
		occurrence.UpdatedAt = time.Now().UTC()
		m.pluginKanbanOccurrences[occurrenceID] = occurrence
	}
	copy := occurrence
	return &copy, nil
}

func (m *MemStore) MarkPluginKanbanOccurrenceReceipt(_ context.Context, occurrenceID, phase string, writtenAt *time.Time, writebackError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	occurrence, ok := m.pluginKanbanOccurrences[occurrenceID]
	if !ok {
		return ErrNotFound
	}
	if occurrence.ReceiptPhase != phase {
		return ErrConflict
	}
	occurrence.WritebackError = writebackError
	if writtenAt != nil {
		at := *writtenAt
		occurrence.ReceiptWrittenAt = &at
		occurrence.WritebackError = ""
		occurrence.UpdatedAt = at
	} else {
		occurrence.UpdatedAt = time.Now().UTC()
	}
	m.pluginKanbanOccurrences[occurrenceID] = occurrence
	return nil
}

func (m *MemStore) ListPluginKanbanOccurrences(_ context.Context, automationID, documentID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := make([]domain.PluginKanbanOccurrence, 0)
	for _, occurrence := range m.pluginKanbanOccurrences {
		if occurrence.AutomationID == automationID && occurrence.DocumentID == documentID {
			out = append(out, occurrence)
		}
	}
	sortPluginKanbanOccurrences(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) GetPluginKanbanClaimByPath(_ context.Context, automationID, workspaceID, documentPath string) (*domain.PluginKanbanClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, claim := range m.pluginKanbanClaims {
		if claim.AutomationID == automationID && claim.WorkspaceID == workspaceID &&
			claim.DocumentPath == documentPath {
			copy := claim
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) MarkPluginKanbanCardUnavailable(_ context.Context, automationID, workspaceID, documentPath string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, claim := range m.pluginKanbanClaims {
		if claim.AutomationID == automationID && claim.WorkspaceID == workspaceID &&
			claim.DocumentPath == documentPath {
			claim.ExternalRefAvailable = false
			claim.LastObservedColumn = ""
			claim.OutsideTriggerAt = &at
			claim.UpdatedAt = at
			m.pluginKanbanClaims[key] = claim
			return true, nil
		}
	}
	return false, nil
}

func pluginKanbanOccurrenceBefore(occurrence domain.PluginKanbanOccurrence, before *PluginKanbanOccurrenceCursor) bool {
	if before == nil {
		return true
	}
	return occurrence.CreatedAt.Before(before.CreatedAt) ||
		(occurrence.CreatedAt.Equal(before.CreatedAt) && occurrence.ID < before.ID)
}

func (m *MemStore) ListPluginKanbanCardExecutions(_ context.Context, automationID, serviceID, workspaceID, documentPath string, before *PluginKanbanOccurrenceCursor, limit int) ([]domain.PluginKanbanOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := make([]domain.PluginKanbanOccurrence, 0)
	for _, occurrence := range m.pluginKanbanOccurrences {
		if occurrence.AutomationID == automationID && occurrence.ServiceID == serviceID &&
			occurrence.WorkspaceID == workspaceID && occurrence.DocumentPath == documentPath &&
			pluginKanbanOccurrenceBefore(occurrence, before) {
			out = append(out, occurrence)
		}
	}
	sortPluginKanbanOccurrences(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) AdvancePluginKanbanTrigger(_ context.Context, automationID string, previousCursor, nextCursor int64, bootstrappedAt *time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	trigger, ok := m.pluginKanbanTriggers[automationID]
	if !ok {
		return false, ErrNotFound
	}
	if trigger.EventCursor != previousCursor || nextCursor < previousCursor {
		return false, nil
	}
	if bootstrappedAt != nil && trigger.BootstrappedAt != nil {
		return false, nil
	}
	trigger.EventCursor = nextCursor
	if bootstrappedAt != nil {
		at := *bootstrappedAt
		trigger.BootstrappedAt = &at
	}
	m.pluginKanbanTriggers[automationID] = trigger
	return true, nil
}

const pluginKanbanOccurrenceCols = `id,automation_id,service_id,installation_id,workspace_id,
	document_id,document_path,done_column,event_key,event_sequence,actor_display,entry_column,
	state,outcome,reason_code,reason_message,repair_role,COALESCE(run_id,''),receipt_phase,
	receipt_written_at,writeback_state,writeback_error,created_at,updated_at,terminal_at`

const qualifiedPluginKanbanOccurrenceCols = `o.id,o.automation_id,o.service_id,o.installation_id,o.workspace_id,
	o.document_id,o.document_path,o.done_column,o.event_key,o.event_sequence,o.actor_display,o.entry_column,
	o.state,o.outcome,o.reason_code,o.reason_message,o.repair_role,COALESCE(o.run_id,''),o.receipt_phase,
	o.receipt_written_at,o.writeback_state,o.writeback_error,o.created_at,o.updated_at,o.terminal_at`

const pluginKanbanClaimCols = `automation_id,installation_id,document_id,document_path,
	workspace_id,done_column,COALESCE(run_id,''),writeback_at,last_observed_column,
	outside_trigger_at,latest_occurrence_id,external_ref_available,created_at,updated_at`

func scanPluginKanbanClaim(row pgx.Row) (*domain.PluginKanbanClaim, error) {
	var claim domain.PluginKanbanClaim
	err := row.Scan(
		&claim.AutomationID, &claim.InstallationID, &claim.DocumentID,
		&claim.DocumentPath, &claim.WorkspaceID, &claim.DoneColumn, &claim.RunID,
		&claim.WritebackAt, &claim.LastObservedColumn, &claim.OutsideTriggerAt,
		&claim.LatestOccurrenceID, &claim.ExternalRefAvailable, &claim.CreatedAt,
		&claim.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan Kanban claim: %w", err)
	}
	return &claim, nil
}

func scanPluginKanbanOccurrence(row pgx.Row) (*domain.PluginKanbanOccurrence, error) {
	var occurrence domain.PluginKanbanOccurrence
	err := row.Scan(
		&occurrence.ID, &occurrence.AutomationID, &occurrence.ServiceID,
		&occurrence.InstallationID, &occurrence.WorkspaceID, &occurrence.DocumentID,
		&occurrence.DocumentPath, &occurrence.DoneColumn, &occurrence.EventKey,
		&occurrence.EventSequence, &occurrence.ActorDisplay, &occurrence.EntryColumn,
		&occurrence.State, &occurrence.Outcome, &occurrence.ReasonCode,
		&occurrence.ReasonMessage, &occurrence.RepairRole, &occurrence.RunID,
		&occurrence.ReceiptPhase, &occurrence.ReceiptWrittenAt, &occurrence.WritebackState,
		&occurrence.WritebackError, &occurrence.CreatedAt, &occurrence.UpdatedAt,
		&occurrence.TerminalAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan Kanban occurrence: %w", err)
	}
	return &occurrence, nil
}

func (s *PGStore) ObservePluginKanbanCard(ctx context.Context, in PluginKanbanObservation) (*PluginKanbanObservationResult, error) {
	if err := validatePluginKanbanObservation(in); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("observe Kanban card: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	existing, err := scanPluginKanbanOccurrence(tx.QueryRow(ctx,
		`SELECT `+pluginKanbanOccurrenceCols+` FROM automation_kanban_occurrences WHERE automation_id=$1 AND event_key=$2`,
		in.AutomationID, in.EventKey))
	if err == nil {
		claim, claimErr := scanPluginKanbanClaim(tx.QueryRow(ctx,
			`SELECT `+pluginKanbanClaimCols+` FROM automation_kanban_claims WHERE automation_id=$1 AND document_id=$2`,
			in.AutomationID, in.DocumentID))
		if claimErr != nil {
			return nil, claimErr
		}
		return &PluginKanbanObservationResult{Claim: *claim, Occurrence: existing}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	claim, claimErr := scanPluginKanbanClaim(tx.QueryRow(ctx,
		`SELECT `+pluginKanbanClaimCols+` FROM automation_kanban_claims WHERE automation_id=$1 AND document_id=$2 FOR UPDATE`,
		in.AutomationID, in.DocumentID))
	claimExists := claimErr == nil
	if claimErr != nil && !errors.Is(claimErr, ErrNotFound) {
		return nil, claimErr
	}
	if !claimExists {
		value := newPluginKanbanClaim(in)
		if _, err = tx.Exec(ctx, `INSERT INTO automation_kanban_claims(
			automation_id,installation_id,document_id,document_path,workspace_id,done_column,
			last_observed_column,external_ref_available,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,'',TRUE,$7,$7)`,
			in.AutomationID, in.InstallationID, in.DocumentID, in.DocumentPath,
			in.WorkspaceID, in.DoneColumn, in.ObservedAt); err != nil {
			if isUniqueViolation(err) {
				_ = tx.Rollback(ctx)
				return s.ObservePluginKanbanCard(ctx, in)
			}
			return nil, fmt.Errorf("observe Kanban card: create claim: %w", err)
		}
		claim = &value
	}

	var latest *domain.PluginKanbanOccurrence
	var latestRunStatus domain.RunStatus
	if claim.LatestOccurrenceID != "" {
		latest, err = scanPluginKanbanOccurrence(tx.QueryRow(ctx,
			`SELECT `+pluginKanbanOccurrenceCols+` FROM automation_kanban_occurrences WHERE id=$1`,
			claim.LatestOccurrenceID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if latest != nil && latest.RunID != "" {
			if err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, latest.RunID).Scan(&latestRunStatus); errors.Is(err, pgx.ErrNoRows) {
				err = nil
			}
			if err != nil {
				return nil, fmt.Errorf("observe Kanban card: load latest run: %w", err)
			}
		}
	}

	create, suppressed := classifyPluginKanbanEntry(claimExists, claim, latest, latestRunStatus, in)
	var outsideAt any
	if in.ObservedColumn != in.TriggerColumn {
		outsideAt = in.ObservedAt
	}
	_, err = tx.Exec(ctx, `UPDATE automation_kanban_claims SET
		document_path=$3,workspace_id=$4,done_column=$5,last_observed_column=$6,
		outside_trigger_at=$7,external_ref_available=TRUE,updated_at=$8
		WHERE automation_id=$1 AND document_id=$2`,
		in.AutomationID, in.DocumentID, in.DocumentPath, in.WorkspaceID,
		in.DoneColumn, in.ObservedColumn, outsideAt, in.ObservedAt)
	if err != nil {
		return nil, fmt.Errorf("observe Kanban card: update claim: %w", err)
	}
	claim.DocumentPath = in.DocumentPath
	claim.WorkspaceID = in.WorkspaceID
	claim.DoneColumn = in.DoneColumn
	claim.LastObservedColumn = in.ObservedColumn
	claim.ExternalRefAvailable = true
	claim.UpdatedAt = in.ObservedAt
	if outsideAt != nil {
		at := in.ObservedAt
		claim.OutsideTriggerAt = &at
	} else {
		claim.OutsideTriggerAt = nil
	}
	if !create {
		if err = tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("observe Kanban card: commit observation: %w", err)
		}
		result := &PluginKanbanObservationResult{Claim: *claim, SuppressedReason: suppressed}
		if suppressed == PluginKanbanAlreadyRunning || suppressed == PluginKanbanWritebackPending || suppressed == PluginKanbanOccurrenceActive {
			result.Occurrence = latest
		}
		return result, nil
	}

	occurrence := newPluginKanbanOccurrence(in)
	_, err = tx.Exec(ctx, `INSERT INTO automation_kanban_occurrences(
		id,automation_id,service_id,installation_id,workspace_id,document_id,document_path,
		done_column,event_key,event_sequence,actor_display,entry_column,state,writeback_state,
		created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`,
		occurrence.ID, occurrence.AutomationID, occurrence.ServiceID,
		occurrence.InstallationID, occurrence.WorkspaceID, occurrence.DocumentID,
		occurrence.DocumentPath, occurrence.DoneColumn, occurrence.EventKey,
		occurrence.EventSequence, occurrence.ActorDisplay, occurrence.EntryColumn,
		occurrence.State, occurrence.WritebackState, occurrence.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback(ctx)
			return s.ObservePluginKanbanCard(ctx, in)
		}
		return nil, fmt.Errorf("observe Kanban card: create occurrence: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE automation_kanban_claims
		SET latest_occurrence_id=$3,run_id=NULL,writeback_at=NULL,updated_at=$4
		WHERE automation_id=$1 AND document_id=$2`,
		in.AutomationID, in.DocumentID, occurrence.ID, in.ObservedAt)
	if err != nil {
		return nil, fmt.Errorf("observe Kanban card: attach occurrence: %w", err)
	}
	claim.LatestOccurrenceID = occurrence.ID
	claim.RunID = ""
	claim.WritebackAt = nil
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("observe Kanban card: commit occurrence: %w", err)
	}
	return &PluginKanbanObservationResult{Claim: *claim, Occurrence: &occurrence, Created: true}, nil
}

func (s *PGStore) CreatePluginKanbanOccurrenceRun(ctx context.Context, occurrenceID string, run *domain.Run) (bool, error) {
	if run == nil {
		return false, errors.New("run is required")
	}
	normalizeRunForCreate(run)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("claim Kanban occurrence run: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	occurrence, err := scanPluginKanbanOccurrence(tx.QueryRow(ctx,
		`SELECT `+pluginKanbanOccurrenceCols+` FROM automation_kanban_occurrences WHERE id=$1 FOR UPDATE`,
		occurrenceID))
	if err != nil {
		return false, err
	}
	if occurrence.RunID != "" {
		return false, nil
	}
	if err = s.createRunTx(ctx, tx, run); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE automation_kanban_occurrences SET
		run_id=$2,state='queued',reason_code='',reason_message='',repair_role='',
		receipt_phase='accepted',receipt_written_at=NULL,writeback_state='pending',
		writeback_error='',updated_at=$3
		WHERE id=$1`, occurrenceID, run.ID, now); err != nil {
		return false, fmt.Errorf("claim Kanban occurrence run: update occurrence: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE automation_kanban_claims SET
		run_id=$3,writeback_at=NULL,latest_occurrence_id=$4,updated_at=$5
		WHERE automation_id=$1 AND document_id=$2`,
		occurrence.AutomationID, occurrence.DocumentID, run.ID, occurrenceID, now); err != nil {
		return false, fmt.Errorf("claim Kanban occurrence run: update claim: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("claim Kanban occurrence run: commit: %w", err)
	}
	return true, nil
}

func (s *PGStore) SetPluginKanbanOccurrenceBlocked(ctx context.Context, occurrenceID, reasonCode, reasonMessage, repairRole string) (*domain.PluginKanbanOccurrence, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_kanban_occurrences SET
		state='blocked',reason_code=$2,reason_message=$3,repair_role=$4,
		receipt_written_at=CASE
			WHEN receipt_phase='blocked' AND reason_code=$2 AND repair_role=$4
			THEN receipt_written_at ELSE NULL END,
		receipt_phase='blocked',writeback_state='not_required',writeback_error='',updated_at=now()
		WHERE id=$1 AND run_id IS NULL`,
		occurrenceID, reasonCode, reasonMessage, repairRole)
	if err != nil {
		return nil, fmt.Errorf("block Kanban occurrence: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, loadErr := scanPluginKanbanOccurrence(s.pool.QueryRow(ctx,
			`SELECT `+pluginKanbanOccurrenceCols+` FROM automation_kanban_occurrences WHERE id=$1`,
			occurrenceID)); errors.Is(loadErr, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrConflict
	}
	return scanPluginKanbanOccurrence(s.pool.QueryRow(ctx,
		`SELECT `+pluginKanbanOccurrenceCols+` FROM automation_kanban_occurrences WHERE id=$1`,
		occurrenceID))
}

func (s *PGStore) ListPluginKanbanDispatchableOccurrences(ctx context.Context, automationID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	return s.listPluginKanbanOccurrencesByPredicate(ctx, automationID,
		`run_id IS NULL AND state IN ('received','blocked')`, limit)
}

func (s *PGStore) ListPluginKanbanReceiptPending(ctx context.Context, automationID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	return s.listPluginKanbanOccurrencesByPredicate(ctx, automationID,
		`receipt_phase IN ('accepted','blocked','already_running','writeback_pending')
		 AND receipt_written_at IS NULL`, limit)
}

func (s *PGStore) listPluginKanbanOccurrencesByPredicate(ctx context.Context, automationID, predicate string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pluginKanbanOccurrenceCols+`
		FROM automation_kanban_occurrences
		WHERE automation_id=$1 AND `+predicate+`
		ORDER BY created_at,id LIMIT $2`, automationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list actionable Kanban occurrences: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PluginKanbanOccurrence, 0)
	for rows.Next() {
		occurrence, scanErr := scanPluginKanbanOccurrence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *occurrence)
	}
	return out, rows.Err()
}

func (s *PGStore) SetPluginKanbanOccurrenceReceiptPhase(
	ctx context.Context,
	occurrenceID, phase string,
) (*domain.PluginKanbanOccurrence, error) {
	if !pluginKanbanReceiptPhaseRetryable(phase) {
		return nil, errors.New("invalid Kanban receipt phase")
	}
	return scanPluginKanbanOccurrence(s.pool.QueryRow(ctx, `UPDATE automation_kanban_occurrences
		SET receipt_phase=$2,
		    receipt_written_at=CASE WHEN receipt_phase=$2 THEN receipt_written_at ELSE NULL END,
		    writeback_error=CASE WHEN receipt_phase=$2 THEN writeback_error ELSE '' END,
		    updated_at=CASE WHEN receipt_phase=$2 THEN updated_at ELSE now() END
		WHERE id=$1
		RETURNING `+pluginKanbanOccurrenceCols,
		occurrenceID, phase))
}

func (s *PGStore) MarkPluginKanbanOccurrenceReceipt(ctx context.Context, occurrenceID, phase string, writtenAt *time.Time, writebackError string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_kanban_occurrences SET
		receipt_written_at=$3,writeback_error=$4,updated_at=now()
		WHERE id=$1 AND receipt_phase=$2`,
		occurrenceID, phase, writtenAt, writebackError)
	if err != nil {
		return fmt.Errorf("mark Kanban occurrence receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *PGStore) ListPluginKanbanOccurrences(ctx context.Context, automationID, documentID string, limit int) ([]domain.PluginKanbanOccurrence, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pluginKanbanOccurrenceCols+`
		FROM automation_kanban_occurrences
		WHERE automation_id=$1 AND document_id=$2
		ORDER BY created_at DESC,id DESC LIMIT $3`, automationID, documentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Kanban occurrences: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PluginKanbanOccurrence, 0)
	for rows.Next() {
		occurrence, err := scanPluginKanbanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *occurrence)
	}
	return out, rows.Err()
}

func (s *PGStore) GetPluginKanbanClaimByPath(ctx context.Context, automationID, workspaceID, documentPath string) (*domain.PluginKanbanClaim, error) {
	return scanPluginKanbanClaim(s.pool.QueryRow(ctx, `SELECT `+pluginKanbanClaimCols+`
		FROM automation_kanban_claims
		WHERE automation_id=$1 AND workspace_id=$2 AND document_path=$3`,
		automationID, workspaceID, documentPath))
}

func (s *PGStore) MarkPluginKanbanCardUnavailable(ctx context.Context, automationID, workspaceID, documentPath string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_kanban_claims
		SET external_ref_available=FALSE,last_observed_column='',
		    outside_trigger_at=$4,updated_at=$4
		WHERE automation_id=$1 AND workspace_id=$2 AND document_path=$3`,
		automationID, workspaceID, documentPath, at)
	if err != nil {
		return false, fmt.Errorf("mark Kanban Card unavailable: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PGStore) ListPluginKanbanCardExecutions(ctx context.Context, automationID, serviceID, workspaceID, documentPath string, before *PluginKanbanOccurrenceCursor, limit int) ([]domain.PluginKanbanOccurrence, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var beforeAt any
	var beforeID any
	if before != nil {
		beforeAt = before.CreatedAt
		beforeID = before.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pluginKanbanOccurrenceCols+`
		FROM automation_kanban_occurrences
		WHERE automation_id=$1 AND service_id=$2 AND workspace_id=$3 AND document_path=$4
		  AND ($5::timestamptz IS NULL OR (created_at,id) < ($5,$6))
		ORDER BY created_at DESC,id DESC LIMIT $7`,
		automationID, serviceID, workspaceID, documentPath, beforeAt, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Card executions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PluginKanbanOccurrence, 0)
	for rows.Next() {
		occurrence, scanErr := scanPluginKanbanOccurrence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *occurrence)
	}
	return out, rows.Err()
}

func (s *PGStore) AdvancePluginKanbanTrigger(ctx context.Context, automationID string, previousCursor, nextCursor int64, bootstrappedAt *time.Time) (bool, error) {
	if nextCursor < previousCursor {
		return false, nil
	}
	var tag pgconn.CommandTag
	var err error
	if bootstrappedAt != nil {
		tag, err = s.pool.Exec(ctx, `UPDATE automation_kanban_triggers
			SET event_cursor=$3,bootstrapped_at=$4
			WHERE automation_id=$1 AND event_cursor=$2 AND bootstrapped_at IS NULL`,
			automationID, previousCursor, nextCursor, bootstrappedAt)
	} else {
		tag, err = s.pool.Exec(ctx, `UPDATE automation_kanban_triggers
			SET event_cursor=$3
			WHERE automation_id=$1 AND event_cursor=$2`,
			automationID, previousCursor, nextCursor)
	}
	if err != nil {
		return false, fmt.Errorf("advance Kanban trigger cursor: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
