package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// MemStore is an in-memory Store for tests. It enforces the same state-machine
// and idempotency semantics as PGStore so tests exercise real behaviour without
// a database. It is safe for concurrent use.
type MemStore struct {
	mu              sync.Mutex
	projects        map[string]domain.Project
	services        map[string]domain.Service
	runs            map[string]domain.Run
	events          map[string][]domain.RunEvent     // keyed by runID, kept sorted by seq
	dedupe          map[string]bool                  // keyed by runID+"|"+source+"|"+client_seq
	artifacts       map[string]domain.RunArtifact    // keyed by runID+"/"+kind
	users           map[string]domain.User           // keyed by user id
	identities      map[string]domain.UserIdentity   // keyed by identity id
	sessions        map[string]domain.Session        // keyed by session id
	members         map[string]domain.ProjectMember  // keyed by projectID+"|"+userID
	modelProviders  map[string]domain.ModelProvider  // keyed by provider id
	models          map[string]domain.Model          // catalog, keyed by model id (D21)
	modelGrants     map[string]bool                  // keyed by modelID+"|"+projectID
	integrations    map[string]domain.Integration    // keyed by integration id (D19 / F5)
	kanbanLinks     map[string]domain.KanbanLink     // keyed by link id
	kanbanClaims    map[string]domain.KanbanClaim    // keyed by linkID+"|"+documentID
	schedules       map[string]domain.Schedule       // keyed by schedule id (F11 / D24)
	automations     map[string]domain.Automation     // keyed by automation id
	webhookBindings map[string]domain.WebhookBinding // keyed by service id
	runMessages     map[string][]domain.RunMessage   // session follow-up queue, keyed by runID (D22)
	permissions     map[string]domain.RunPermission  // permission requests, keyed by request_id (F8b)
	apiKeys         map[string]domain.APIKey         // keyed by api key id (F12 / D24)
	kanbanConfig    *domain.KanbanConfig             // single-row cluster kanban config, nil = absent (D27)
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		projects:        map[string]domain.Project{},
		services:        map[string]domain.Service{},
		runs:            map[string]domain.Run{},
		events:          map[string][]domain.RunEvent{},
		dedupe:          map[string]bool{},
		artifacts:       map[string]domain.RunArtifact{},
		users:           map[string]domain.User{},
		identities:      map[string]domain.UserIdentity{},
		sessions:        map[string]domain.Session{},
		members:         map[string]domain.ProjectMember{},
		modelProviders:  map[string]domain.ModelProvider{},
		models:          map[string]domain.Model{},
		modelGrants:     map[string]bool{},
		integrations:    map[string]domain.Integration{},
		kanbanLinks:     map[string]domain.KanbanLink{},
		kanbanClaims:    map[string]domain.KanbanClaim{},
		schedules:       map[string]domain.Schedule{},
		automations:     map[string]domain.Automation{},
		webhookBindings: map[string]domain.WebhookBinding{},
		runMessages:     map[string][]domain.RunMessage{},
		permissions:     map[string]domain.RunPermission{},
		apiKeys:         map[string]domain.APIKey{},
	}
}

// dedupeKey builds the per-source idempotency key mirroring the DB unique index
// on (run_id, source, client_seq).
func dedupeKey(runID, source string, clientSeq int64) string {
	return runID + "|" + source + "|" + strconv.FormatInt(clientSeq, 10)
}

// maxSeqLocked returns the current highest seq for a run (0 if none). Caller
// must hold m.mu.
func (m *MemStore) maxSeqLocked(runID string) int64 {
	var max int64
	for _, e := range m.events[runID] {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max
}

func (m *MemStore) Close() {}

// --- projects ---

func (m *MemStore) CreateProject(_ context.Context, p *domain.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects[p.ID] = *p
	return nil
}

func (m *MemStore) GetProject(_ context.Context, id string) (*domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := p
	return &cp, nil
}

func (m *MemStore) ListProjects(_ context.Context) ([]domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateProject(_ context.Context, p *domain.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[p.ID]; !ok {
		return ErrNotFound
	}
	m.projects[p.ID] = *p
	return nil
}

func (m *MemStore) DeleteProject(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return ErrNotFound
	}
	delete(m.projects, id)
	// Cascade: drop the project's services and runs (mirrors the FK ON DELETE
	// CASCADE on services.project_id / runs.project_id).
	for sid, svc := range m.services {
		if svc.ProjectID == id {
			delete(m.services, sid)
		}
	}
	for rid, r := range m.runs {
		if r.ProjectID == id {
			delete(m.runs, rid)
			delete(m.runMessages, rid) // cascade run_messages (FK ON DELETE CASCADE)
			// cascade run_permissions (FK ON DELETE CASCADE)
			for reqID, perm := range m.permissions {
				if perm.RunID == rid {
					delete(m.permissions, reqID)
				}
			}
		}
	}
	for k, mem := range m.members {
		if mem.ProjectID == id {
			delete(m.members, k)
		}
	}
	return nil
}

// --- services ---

func (m *MemStore) CreateService(_ context.Context, s *domain.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.GitMode == "" {
		s.GitMode = domain.GitModeReadonly
	}
	if s.DefaultBranch == "" {
		s.DefaultBranch = "main"
	}
	m.services[s.ID] = *s
	return nil
}

func (m *MemStore) GetService(_ context.Context, id string) (*domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := s
	return &cp, nil
}

func (m *MemStore) ListServices(_ context.Context, projectID string) ([]domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Service
	for _, s := range m.services {
		if s.ProjectID == projectID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) GetDefaultService(_ context.Context, projectID string) (*domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.services {
		if s.ProjectID == projectID && s.Name == "default" {
			cp := s
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListServicesByRepo(_ context.Context, provider domain.GitProvider, repoOwnerName string) ([]domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Service
	for _, s := range m.services {
		if s.RepoKind == domain.RepoKindProvider && s.Provider == provider && s.RepoOwnerName == repoOwnerName {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateService(_ context.Context, s *domain.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.services[s.ID]; !ok {
		return ErrNotFound
	}
	m.services[s.ID] = *s
	return nil
}

func (m *MemStore) DeleteService(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.services[id]; !ok {
		return ErrNotFound
	}
	delete(m.services, id)
	return nil
}

// ListArchiveCandidates mirrors the PG query (F10): a service not already
// archived, with at least one run, whose most-recent run predates idleBefore and
// which has no run in a non-terminal state.
func (m *MemStore) ListArchiveCandidates(_ context.Context, idleBefore time.Time) ([]ArchiveCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Per-service: most-recent run time + whether any run is live (non-terminal).
	type agg struct {
		last    time.Time
		anyRun  bool
		hasLive bool
	}
	byService := map[string]*agg{}
	for _, r := range m.runs {
		a := byService[r.ServiceID]
		if a == nil {
			a = &agg{}
			byService[r.ServiceID] = a
		}
		a.anyRun = true
		if r.CreatedAt.After(a.last) {
			a.last = r.CreatedAt
		}
		switch r.Status {
		case domain.StatusQueued, domain.StatusScheduling, domain.StatusRunning, domain.StatusAwaitingInput:
			a.hasLive = true
		}
	}
	var out []ArchiveCandidate
	for sid, svc := range m.services {
		if svc.ArchivedAt != nil {
			continue
		}
		a := byService[sid]
		if a == nil || !a.anyRun || a.hasLive {
			continue
		}
		if !a.last.Before(idleBefore) {
			continue
		}
		out = append(out, ArchiveCandidate{ServiceID: sid, ProjectID: svc.ProjectID, LastActivity: a.last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity.Before(out[j].LastActivity) })
	return out, nil
}

// MarkServiceArchived stamps archived_at + archive_key (F10).
func (m *MemStore) MarkServiceArchived(_ context.Context, serviceID, archiveKey string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[serviceID]
	if !ok {
		return ErrNotFound
	}
	t := at
	svc.ArchivedAt = &t
	svc.ArchiveKey = archiveKey
	m.services[serviceID] = svc
	return nil
}

// ClearServiceArchive clears archived_at + archive_key (F10). Idempotent.
func (m *MemStore) ClearServiceArchive(_ context.Context, serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[serviceID]
	if !ok {
		return ErrNotFound
	}
	svc.ArchivedAt = nil
	svc.ArchiveKey = ""
	m.services[serviceID] = svc
	return nil
}

// --- runs ---

func (m *MemStore) CreateRun(_ context.Context, r *domain.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Kind == "" {
		r.Kind = domain.RunKindAgent
	}
	if r.Origin == "" {
		r.Origin = domain.RunOriginAPI
	}
	// Mirror the PG partial-unique index on origin_comment_id: a redelivered
	// webhook comment cannot create a second run.
	if r.OriginCommentID != "" {
		for _, ex := range m.runs {
			if ex.OriginCommentID == r.OriginCommentID {
				return fmt.Errorf("origin_comment_id already used: %s", r.OriginCommentID)
			}
		}
	}
	if r.OriginEventKey != "" {
		for _, ex := range m.runs {
			if ex.OriginEventKey == r.OriginEventKey {
				return fmt.Errorf("origin_event_key already used: %s", r.OriginEventKey)
			}
		}
	}
	m.runs[r.ID] = *r
	return nil
}

func (m *MemStore) GetRunByOriginCommentID(_ context.Context, commentID string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if commentID == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.OriginCommentID == commentID {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) GetRunByOriginEventKey(_ context.Context, eventKey string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if eventKey == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.OriginEventKey == eventKey {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) GetRun(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *MemStore) GetRunByTokenHash(_ context.Context, tokenHash string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.TokenHash == tokenHash {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListRuns(_ context.Context, projectID string, limit int) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if projectID == "" || r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListRunsByService(_ context.Context, serviceID string, limit int) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.ServiceID == serviceID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListRunsByStatus(_ context.Context, statuses ...domain.RunStatus) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[domain.RunStatus]bool{}
	for _, s := range statuses {
		want[s] = true
	}
	var out []domain.Run
	for _, r := range m.runs {
		if want[r.Status] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) CountActiveRuns(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Status == domain.StatusScheduling || r.Status == domain.StatusRunning {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) CountRunsByStatus(_ context.Context, statuses ...domain.RunStatus) (map[domain.RunStatus]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[domain.RunStatus]int, len(statuses))
	for _, s := range statuses {
		out[s] = 0 // every requested status is present as a key, defaulting to 0
	}
	for _, r := range m.runs {
		if _, ok := out[r.Status]; ok {
			out[r.Status]++
		}
	}
	return out, nil
}

// transitionLocked applies a status change plus a field mutator to the CURRENTLY
// stored row (never a caller snapshot), enforcing the state machine. It mirrors
// PGStore's "re-read committed row, mutate named fields, return committed copy"
// semantics so the two stores stay behaviourally identical. Caller holds m.mu.
func (m *MemStore) transitionLocked(id string, to domain.RunStatus, mut func(*domain.Run)) (*domain.Run, error) {
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !domain.CanTransition(cur.Status, to) {
		return nil, ErrInvalidTransition
	}
	cur.Status = to
	mut(&cur)
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

func (m *MemStore) ScheduleRun(_ context.Context, id, jobName, tokenHash, phase string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusScheduling, func(r *domain.Run) {
		r.Phase = phase
		r.K8sJobName = jobName
		r.TokenHash = tokenHash
	})
}

func (m *MemStore) MarkRunning(_ context.Context, id, phase string, startedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusRunning, func(r *domain.Run) {
		r.Phase = phase
		if r.StartedAt == nil {
			t := startedAt
			r.StartedAt = &t
		}
	})
}

func (m *MemStore) MarkSucceeded(_ context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusSucceeded, func(r *domain.Run) {
		r.Phase = phase
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

func (m *MemStore) MarkFailed(_ context.Context, id, phase string, reason domain.FailureReason, msg string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusFailed, func(r *domain.Run) {
		r.Phase = phase
		if r.FailureReason == "" {
			r.FailureReason = reason
		}
		if r.FailureMessage == "" {
			r.FailureMessage = msg
		}
		r.Error = r.FailureMessage
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

func (m *MemStore) SetRunnerFailure(_ context.Context, id string, reason domain.FailureReason, msg string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Status.Terminal() {
		cp := cur
		return &cp, nil // already terminal: leave it
	}
	if cur.FailureReason == "" {
		cur.FailureReason = reason
	}
	if cur.FailureMessage == "" {
		cur.FailureMessage = msg
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

func (m *MemStore) CancelRun(_ context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusCanceled, func(r *domain.Run) {
		r.Phase = phase
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

// MarkJobCleaned stamps JobCleanedAt once the run's Job is confirmed deleted.
// K8sJobName is KEPT (historical record). Idempotent: a prior stamp is
// preserved. No status change; a missing run is a no-op.
func (m *MemStore) MarkJobCleaned(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok && r.JobCleanedAt == nil {
		t := time.Now().UTC()
		r.JobCleanedAt = &t
		m.runs[id] = r
	}
	return nil
}

func (m *MemStore) ListTerminalRunsWithJob(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status.Terminal() && r.K8sJobName != "" && r.JobCleanedAt == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// SetRunGit records branch/commit first-writer-wins, no status change.
func (m *MemStore) SetRunGit(_ context.Context, id, branch, commitSHA string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.GitBranch == "" {
		cur.GitBranch = branch
	}
	if cur.CommitSHA == "" {
		cur.CommitSHA = commitSHA
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetRunResult records a run outcome (run.result) first-writer-wins, no status
// change. Writes only where result is still nil, so a duplicate event is a no-op.
func (m *MemStore) SetRunResult(_ context.Context, id string, result domain.RunResult) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Result == nil {
		rr := result
		cur.Result = &rr
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetRunACPSession records the run's ACP session id (run.session) first-writer-
// wins, no status change. Writes only where acp_session_id is still empty (and
// the id is non-empty), so a duplicate event / a pre-filled resume run is a no-op.
func (m *MemStore) SetRunACPSession(_ context.Context, id, acpSessionID string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.AcpSessionID == "" && acpSessionID != "" {
		cur.AcpSessionID = acpSessionID
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// MarkPRCreated stamps pr_url/pr_number idempotently, first-writer-wins.
func (m *MemStore) MarkPRCreated(_ context.Context, id, prURL string, prNumber int) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.PRURL == "" {
		cur.PRURL = prURL
		cur.PRNumber = prNumber
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// ListRunsAwaitingPR returns succeeded NON-session agent runs with a recorded
// branch but no PR yet. Session runs are handled by ListSessionRunsAwaitingPush.
func (m *MemStore) ListRunsAwaitingPR(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Kind == domain.RunKindAgent &&
			r.GitBranch != "" && r.PRURL == "" && !r.Session {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// --- Session runs (D22) ---

// SetRunAwaitingInput: running -> awaiting_input, stamping awaiting_since only
// where it is still nil (first-writer-wins) so a duplicate turn-complete does
// not reset the idle timer.
func (m *MemStore) SetRunAwaitingInput(_ context.Context, id string, at time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusAwaitingInput, func(r *domain.Run) {
		if r.AwaitingSince == nil {
			t := at
			r.AwaitingSince = &t
		}
	})
}

// ResumeRun: awaiting_input -> running, clearing awaiting_since.
func (m *MemStore) ResumeRun(_ context.Context, id, phase string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusRunning, func(r *domain.Run) {
		r.Phase = phase
		r.AwaitingSince = nil
	})
}

// MarkSessionFinalizing sets session_finalizing while non-terminal (idempotent).
func (m *MemStore) MarkSessionFinalizing(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Status.Terminal() {
		cp := cur
		return &cp, nil
	}
	cur.SessionFinalizing = true
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// FinalizeIdleSession — conditional finalize (idle-timeout pass): flips the flag
// only while the run is STILL awaiting_input, not already finalizing, and idle
// since at-or-before cutoff. All checks under the same lock (no TOCTOU).
func (m *MemStore) FinalizeIdleSession(_ context.Context, id string, cutoff time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return false, nil
	}
	if cur.Status != domain.StatusAwaitingInput || cur.SessionFinalizing ||
		cur.AwaitingSince == nil || cur.AwaitingSince.After(cutoff) {
		return false, nil
	}
	cur.SessionFinalizing = true
	m.runs[id] = cur
	return true, nil
}

// AppendRunMessage enqueues a follow-up prompt, allocating the next per-run seq.
func (m *MemStore) AppendRunMessage(_ context.Context, runID, prompt, createdBy string) (*domain.RunMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, ErrNotFound
	}
	var maxSeq int64
	for _, msg := range m.runMessages[runID] {
		if msg.Seq > maxSeq {
			maxSeq = msg.Seq
		}
	}
	msg := domain.RunMessage{
		ID: domain.NewID(), RunID: runID, Seq: maxSeq + 1, Prompt: prompt,
		CreatedBy: createdBy, CreatedAt: time.Now().UTC(),
	}
	m.runMessages[runID] = append(m.runMessages[runID], msg)
	cp := msg
	return &cp, nil
}

// OfferNextMessage — phase 1 of the two-phase delivery, all under one lock so
// two concurrent offers converge on the SAME message (never two different ones):
// an offered-but-not-consumed message is re-delivered verbatim (fresh=false),
// otherwise the oldest unoffered one is stamped offered_at (fresh=true).
func (m *MemStore) OfferNextMessage(_ context.Context, runID string, at time.Time) (*domain.RunMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, false, ErrNotFound
	}
	msgs := m.runMessages[runID]
	// msgs is kept append-ordered (ascending seq).
	for i := range msgs {
		if msgs[i].OfferedAt != nil && msgs[i].ConsumedAt == nil {
			cp := msgs[i]
			return &cp, false, nil // idempotent re-delivery
		}
	}
	for i := range msgs {
		if msgs[i].OfferedAt == nil {
			t := at
			msgs[i].OfferedAt = &t
			m.runMessages[runID] = msgs
			cp := msgs[i]
			return &cp, true, nil
		}
	}
	return nil, false, ErrNotFound
}

// ConsumeOfferedMessage — phase 2: stamps consumed_at on the offered message.
// (false, nil) when none is offered (e.g. the first TASK_PROMPT turn).
func (m *MemStore) ConsumeOfferedMessage(_ context.Context, runID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.runMessages[runID]
	consumed := false
	for i := range msgs {
		if msgs[i].OfferedAt != nil && msgs[i].ConsumedAt == nil {
			t := at
			msgs[i].ConsumedAt = &t
			consumed = true
		}
	}
	if consumed {
		m.runMessages[runID] = msgs
	}
	return consumed, nil
}

// ListRunMessages returns a run's queued messages, oldest first.
func (m *MemStore) ListRunMessages(_ context.Context, runID string) ([]domain.RunMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.RunMessage, len(m.runMessages[runID]))
	copy(out, m.runMessages[runID])
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// --- Session permission approval (F8b) ---------------------------------------

// copyPermission deep-copies a RunPermission (Options is a slice; pointer
// fields are re-pointed at copies) so callers can never mutate stored state.
func copyPermission(p domain.RunPermission) domain.RunPermission {
	cp := p
	cp.Options = append([]domain.PermissionOption(nil), p.Options...)
	if p.DecidedOptionID != nil {
		v := *p.DecidedOptionID
		cp.DecidedOptionID = &v
	}
	if p.DecidedBy != nil {
		v := *p.DecidedBy
		cp.DecidedBy = &v
	}
	if p.DecidedAt != nil {
		v := *p.DecidedAt
		cp.DecidedAt = &v
	}
	if p.ResolvedOptionID != nil {
		v := *p.ResolvedOptionID
		cp.ResolvedOptionID = &v
	}
	if p.Resolution != nil {
		v := *p.Resolution
		cp.Resolution = &v
	}
	if p.ResolvedAt != nil {
		v := *p.ResolvedAt
		cp.ResolvedAt = &v
	}
	return cp
}

// UpsertRunPermission — insert-only idempotency: an existing request_id is left
// completely untouched (a duplicate request event must never reset a
// decided/resolved row).
func (m *MemStore) UpsertRunPermission(_ context.Context, p *domain.RunPermission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[p.RunID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.permissions[p.RequestID]; ok {
		return nil // idempotent re-delivery: never reset decided/resolved state
	}
	m.permissions[p.RequestID] = copyPermission(*p)
	return nil
}

func (m *MemStore) GetRunPermission(_ context.Context, runID, requestID string) (*domain.RunPermission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID {
		return nil, ErrNotFound
	}
	cp := copyPermission(p)
	return &cp, nil
}

// DecideRunPermission — the conditional user-answer write: wins only while the
// row is neither decided nor resolved (mirrors the PG WHERE clause).
func (m *MemStore) DecideRunPermission(_ context.Context, runID, requestID, optionID, decidedBy string, at time.Time) (*domain.RunPermission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID {
		return nil, false, ErrNotFound
	}
	if p.DecidedOptionID != nil || p.ResolvedAt != nil {
		cp := copyPermission(p)
		return &cp, false, nil // already answered/resolved: the caller 409s
	}
	opt := optionID
	t := at
	p.DecidedOptionID = &opt
	p.DecidedAt = &t
	if decidedBy != "" {
		by := decidedBy
		p.DecidedBy = &by
	}
	m.permissions[requestID] = p
	cp := copyPermission(p)
	return &cp, true, nil
}

// ResolveRunPermission — first-writer-wins on the resolved_* fields; a missing
// row or an already-resolved row is a silent no-op (duplicate/orphan events).
func (m *MemStore) ResolveRunPermission(_ context.Context, runID, requestID, optionID, resolution string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID || p.ResolvedAt != nil {
		return nil
	}
	opt := optionID
	res := resolution
	t := at
	p.ResolvedOptionID = &opt
	p.Resolution = &res
	p.ResolvedAt = &t
	m.permissions[requestID] = p
	return nil
}

// ListRunPermissions returns a run's permission requests, oldest first.
func (m *MemStore) ListRunPermissions(_ context.Context, runID string) ([]domain.RunPermission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RunPermission
	for _, p := range m.permissions {
		if p.RunID == runID {
			out = append(out, copyPermission(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// BumpBundleRev increments bundle_rev.
func (m *MemStore) BumpBundleRev(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cur.BundleRev++
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetPushedRev advances pushed_rev to at-least rev and records commit_sha. An
// empty sha preserves the stored value (PR-already-exists recovery pushes
// nothing and must not wipe the last recorded tip).
func (m *MemStore) SetPushedRev(_ context.Context, id string, rev int64, commitSHA string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if rev > cur.PushedRev {
		cur.PushedRev = rev
	}
	if commitSHA != "" {
		cur.CommitSHA = commitSHA
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// ListSessionRunsAwaitingPush returns session agent runs with a recorded branch
// and a bundle newer than the last push, still non-final. Oldest-first.
func (m *MemStore) ListSessionRunsAwaitingPush(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Session && r.Kind == domain.RunKindAgent && r.GitBranch != "" &&
			r.BundleRev > r.PushedRev &&
			r.Status != domain.StatusFailed && r.Status != domain.StatusCanceled {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListAwaitingInputRuns returns every run currently in awaiting_input.
func (m *MemStore) ListAwaitingInputRuns(ctx context.Context) ([]domain.Run, error) {
	return m.ListRunsByStatus(ctx, domain.StatusAwaitingInput)
}

// ListReviewRunsAwaitingPost returns succeeded review runs with output that has
// not been posted to the PR yet.
func (m *MemStore) ListReviewRunsAwaitingPost(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Kind == domain.RunKindReview &&
			r.ReviewOutput != "" && r.ReviewPostedAt == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListRunsAwaitingUpdatePush(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Origin == domain.RunOriginWebhook &&
			r.Kind == domain.RunKindAgent && r.GitBranch != "" && r.PRURL != "" && r.CommitSHA == "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// SetReviewOutput records a review run's output first-writer-wins, no status change.
func (m *MemStore) SetReviewOutput(_ context.Context, id, md string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.ReviewOutput == "" {
		cur.ReviewOutput = md
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// MarkReviewPosted stamps review_posted_at idempotently, returning true only for
// the caller that actually stamped it.
func (m *MemStore) MarkReviewPosted(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return false, ErrNotFound
	}
	if cur.ReviewPostedAt != nil {
		return false, nil
	}
	t := time.Now().UTC()
	cur.ReviewPostedAt = &t
	m.runs[id] = cur
	return true, nil
}

// --- events ---

func (m *MemStore) AppendEvents(_ context.Context, runID string, events []EventInput) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := map[int64]bool{}
	for _, e := range m.events[runID] {
		existing[e.Seq] = true
	}
	inserted := 0
	for _, e := range events {
		if existing[e.Seq] {
			continue // idempotent by (run_id, seq)
		}
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		m.events[runID] = append(m.events[runID], domain.RunEvent{
			RunID: runID, Seq: e.Seq, Type: e.Type, Payload: payload,
		})
		m.dedupe[dedupeKey(runID, SourceInternal, e.Seq)] = true
		existing[e.Seq] = true
		inserted++
	}
	sort.Slice(m.events[runID], func(i, j int) bool {
		return m.events[runID][i].Seq < m.events[runID][j].Seq
	})
	return inserted, nil
}

func (m *MemStore) AppendRunnerEvents(_ context.Context, runID string, events []EventInput) ([]domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, ErrNotFound
	}
	next := m.maxSeqLocked(runID)
	out := make([]domain.RunEvent, 0, len(events))
	for _, e := range events {
		key := dedupeKey(runID, SourceRunner, e.Seq)
		if m.dedupe[key] {
			continue // idempotent by (run_id, runner, client_seq); no seq consumed
		}
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		next++
		ev := domain.RunEvent{RunID: runID, Seq: next, TS: time.Now().UTC(), Type: e.Type, Payload: payload}
		m.events[runID] = append(m.events[runID], ev)
		m.dedupe[key] = true
		out = append(out, ev)
	}
	sort.Slice(m.events[runID], func(i, j int) bool {
		return m.events[runID][i].Seq < m.events[runID][j].Seq
	})
	return out, nil
}

func (m *MemStore) AppendInternalEvent(_ context.Context, runID, typ string, payload map[string]any) (domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return domain.RunEvent{}, ErrNotFound
	}
	if payload == nil {
		payload = map[string]any{}
	}
	seq := m.maxSeqLocked(runID) + 1
	ev := domain.RunEvent{RunID: runID, Seq: seq, TS: time.Now().UTC(), Type: typ, Payload: payload}
	m.events[runID] = append(m.events[runID], ev)
	m.dedupe[dedupeKey(runID, SourceInternal, seq)] = true
	return ev, nil
}

func (m *MemStore) NextEventSeq(_ context.Context, runID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxSeqLocked(runID) + 1, nil
}

func (m *MemStore) ListEvents(_ context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RunEvent
	for _, e := range m.events[runID] {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// --- artifacts ---

func (m *MemStore) PutArtifact(_ context.Context, a *domain.RunArtifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.artifacts[a.RunID+"/"+string(a.Kind)] = *a
	return nil
}

func (m *MemStore) GetArtifact(_ context.Context, runID string, kind domain.ArtifactKind) (*domain.RunArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.artifacts[runID+"/"+string(kind)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := a
	return &cp, nil
}

// PutRunBundle stores a run's git bundle bytes (kind=bundle) in the artifact map.
func (m *MemStore) PutRunBundle(_ context.Context, runID string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.artifacts[runID+"/"+string(domain.ArtifactBundle)] = domain.RunArtifact{
		RunID: runID, Kind: domain.ArtifactBundle, Bytes: cp, CreatedAt: time.Now().UTC(),
	}
	return nil
}

// GetRunBundle returns a run's stored git bundle bytes (ErrNotFound if absent).
func (m *MemStore) GetRunBundle(_ context.Context, runID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.artifacts[runID+"/"+string(domain.ArtifactBundle)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(a.Bytes))
	copy(cp, a.Bytes)
	return cp, nil
}

// --- model catalog + project grants (D21) ---

func cloneModelProvider(p domain.ModelProvider) domain.ModelProvider {
	if p.APIKeyEnc != nil {
		p.APIKeyEnc = append([]byte(nil), p.APIKeyEnc...)
	}
	if p.CatalogAvailable != nil {
		v := *p.CatalogAvailable
		p.CatalogAvailable = &v
	}
	if p.LastVerifiedAt != nil {
		v := *p.LastVerifiedAt
		p.LastVerifiedAt = &v
	}
	return p
}

func (m *MemStore) CreateModelProvider(_ context.Context, p *domain.ModelProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.modelProviders {
		if existing.Name == p.Name {
			return ErrAlreadyExists
		}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = p.CreatedAt
	m.modelProviders[p.ID] = cloneModelProvider(*p)
	return nil
}

func (m *MemStore) GetModelProvider(_ context.Context, id string) (*domain.ModelProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.modelProviders[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneModelProvider(p)
	return &cp, nil
}

func (m *MemStore) ListModelProviders(_ context.Context) ([]domain.ModelProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.ModelProvider, 0, len(m.modelProviders))
	for _, p := range m.modelProviders {
		out = append(out, cloneModelProvider(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateModelProvider(_ context.Context, p *domain.ModelProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[p.ID]; !ok {
		return ErrNotFound
	}
	for id, existing := range m.modelProviders {
		if id != p.ID && existing.Name == p.Name {
			return ErrAlreadyExists
		}
	}
	p.UpdatedAt = time.Now().UTC()
	m.modelProviders[p.ID] = cloneModelProvider(*p)
	for id, mod := range m.models {
		if mod.ProviderID == p.ID {
			mod.BaseURL = p.BaseURL
			mod.APIKeyEnc = append([]byte(nil), p.APIKeyEnc...)
			mod.ModelName = p.Kind + "/" + mod.ModelID
			m.models[id] = mod
		}
	}
	return nil
}

func (m *MemStore) deleteModelLocked(id string) error {
	if _, ok := m.models[id]; !ok {
		return ErrNotFound
	}
	delete(m.models, id)
	for k := range m.modelGrants {
		if strings.HasPrefix(k, id+"|") {
			delete(m.modelGrants, k)
		}
	}
	for sid, svc := range m.services {
		if svc.DefaultModelID != nil && *svc.DefaultModelID == id {
			svc.DefaultModelID = nil
			m.services[sid] = svc
		}
	}
	for rid, run := range m.runs {
		if run.ModelID != nil && *run.ModelID == id {
			run.ModelID = nil
			m.runs[rid] = run
		}
	}
	return nil
}

func (m *MemStore) DeleteModelProvider(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[id]; !ok {
		return ErrNotFound
	}
	for modelID, mod := range m.models {
		if mod.ProviderID == id {
			_ = m.deleteModelLocked(modelID)
		}
	}
	delete(m.modelProviders, id)
	return nil
}

func (m *MemStore) ListModelsForProvider(_ context.Context, providerID string) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[providerID]; !ok {
		return nil, ErrNotFound
	}
	var out []domain.Model
	for _, mod := range m.models {
		if mod.ProviderID == providerID {
			out = append(out, cloneModel(mod))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// cloneModel deep-copies a model so callers can't mutate the stored api_key_enc.
func cloneModel(m domain.Model) domain.Model {
	if m.APIKeyEnc != nil {
		m.APIKeyEnc = append([]byte(nil), m.APIKeyEnc...)
	}
	return m
}

func grantKey(modelID, projectID string) string { return modelID + "|" + projectID }

// CreateModel inserts a catalog model. Duplicate name => ErrAlreadyExists.
func (m *MemStore) CreateModel(_ context.Context, mod *domain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.models {
		if e.Name == mod.Name {
			return ErrAlreadyExists
		}
	}
	if mod.CreatedAt.IsZero() {
		mod.CreatedAt = time.Now().UTC()
	}
	if mod.ProviderID == "" {
		mod.ProviderID = mod.ID
		authType := domain.ModelProviderAuthNone
		if len(mod.APIKeyEnc) > 0 {
			authType = domain.ModelProviderAuthAPIKey
		}
		m.modelProviders[mod.ProviderID] = domain.ModelProvider{
			ID: mod.ProviderID, Name: mod.Name, Kind: "custom", BaseURL: mod.BaseURL,
			AuthType: authType, APIKeyEnc: append([]byte(nil), mod.APIKeyEnc...),
			CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: mod.CreatedAt,
			UpdatedAt: mod.CreatedAt, UpdatedBy: mod.UpdatedBy,
		}
	}
	if _, ok := m.modelProviders[mod.ProviderID]; !ok {
		return ErrNotFound
	}
	if mod.Source == "" {
		mod.Source = "custom"
	}
	if mod.ModelID == "" {
		_, bare, ok := strings.Cut(mod.ModelName, "/")
		if ok {
			mod.ModelID = bare
		} else {
			mod.ModelID = mod.ModelName
		}
	}
	m.models[mod.ID] = cloneModel(*mod)
	return nil
}

// GetModel returns a catalog model by id.
func (m *MemStore) GetModel(_ context.Context, id string) (*domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.models[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneModel(e)
	return &cp, nil
}

// ListModels returns the whole catalog, newest first.
func (m *MemStore) ListModels(_ context.Context) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Model, 0, len(m.models))
	for _, e := range m.models {
		out = append(out, cloneModel(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CountModels returns the number of catalog models.
func (m *MemStore) CountModels(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.models), nil
}

// UpdateModel updates a model's mutable fields. Duplicate name => ErrAlreadyExists.
func (m *MemStore) UpdateModel(_ context.Context, mod *domain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[mod.ID]; !ok {
		return ErrNotFound
	}
	for id, e := range m.models {
		if id != mod.ID && e.Name == mod.Name {
			return ErrAlreadyExists
		}
	}
	if mod.Source == "" {
		mod.Source = "custom"
	}
	if mod.ModelID == "" {
		_, bare, ok := strings.Cut(mod.ModelName, "/")
		if ok {
			mod.ModelID = bare
		} else {
			mod.ModelID = mod.ModelName
		}
	}
	m.models[mod.ID] = cloneModel(*mod)
	if provider, ok := m.modelProviders[mod.ProviderID]; ok {
		kind, _, hasKind := strings.Cut(mod.ModelName, "/")
		if hasKind {
			provider.Kind = kind
		}
		provider.BaseURL = mod.BaseURL
		provider.APIKeyEnc = append([]byte(nil), mod.APIKeyEnc...)
		provider.AuthType = domain.ModelProviderAuthNone
		if len(mod.APIKeyEnc) > 0 {
			provider.AuthType = domain.ModelProviderAuthAPIKey
		}
		provider.UpdatedAt = time.Now().UTC()
		provider.UpdatedBy = mod.UpdatedBy
		m.modelProviders[provider.ID] = provider
		for id, sibling := range m.models {
			if sibling.ProviderID != provider.ID {
				continue
			}
			sibling.BaseURL = provider.BaseURL
			sibling.APIKeyEnc = append([]byte(nil), provider.APIKeyEnc...)
			sibling.ModelName = provider.Kind + "/" + sibling.ModelID
			m.models[id] = sibling
		}
	}
	return nil
}

// DeleteModel removes a model, cascading its grants and nulling any service
// default / run reference (mirrors the ON DELETE SET NULL / CASCADE FKs).
func (m *MemStore) DeleteModel(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteModelLocked(id)
}

// ListModelsForProject returns the models granted to a project, newest first.
func (m *MemStore) ListModelsForProject(_ context.Context, projectID string) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Model
	for id, mod := range m.models {
		if m.modelGrants[grantKey(id, projectID)] {
			out = append(out, cloneModel(mod))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListProjectIDsForModel returns the project ids a model is granted to.
func (m *MemStore) ListProjectIDsForModel(_ context.Context, modelID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return nil, ErrNotFound
	}
	var out []string
	prefix := modelID + "|"
	for k := range m.modelGrants {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

// GrantModel authorizes a project to use a model (idempotent).
func (m *MemStore) GrantModel(_ context.Context, modelID, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.projects[projectID]; !ok {
		return ErrNotFound
	}
	m.modelGrants[grantKey(modelID, projectID)] = true
	return nil
}

// RevokeModel removes a project's grant (idempotent no-op when absent).
func (m *MemStore) RevokeModel(_ context.Context, modelID, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.modelGrants, grantKey(modelID, projectID))
	return nil
}

// --- cluster kanban config (D27) ---

func (m *MemStore) GetClusterKanbanConfig(_ context.Context) (*domain.KanbanConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kanbanConfig == nil {
		return nil, ErrNotFound
	}
	// Clone (incl. the token blob + expiry) so a caller can't mutate the stored row.
	cp := *m.kanbanConfig
	if m.kanbanConfig.TokenEnc != nil {
		cp.TokenEnc = append([]byte(nil), m.kanbanConfig.TokenEnc...)
	}
	if m.kanbanConfig.TokenExpiresAt != nil {
		t := *m.kanbanConfig.TokenExpiresAt
		cp.TokenExpiresAt = &t
	}
	return &cp, nil
}

func (m *MemStore) UpsertClusterKanbanConfig(_ context.Context, cfg *domain.KanbanConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := domain.KanbanConfig{BaseURL: cfg.BaseURL, UpdatedBy: cfg.UpdatedBy, UpdatedAt: time.Now().UTC()}
	if cfg.TokenEnc != nil {
		stored.TokenEnc = append([]byte(nil), cfg.TokenEnc...)
	}
	if cfg.TokenExpiresAt != nil {
		t := *cfg.TokenExpiresAt
		stored.TokenExpiresAt = &t
	}
	m.kanbanConfig = &stored
	return nil
}

// SetClusterKanbanToken conditionally seals a device-flow token (D28): the
// check (row present + same base_url) and the write happen atomically under the
// store mutex — mirroring the pg conditional UPDATE. A missing row or a changed
// base_url is ErrNotFound; base_url is never written.
func (m *MemStore) SetClusterKanbanToken(_ context.Context, baseURL string, tokenEnc []byte, expiresAt *time.Time, updatedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kanbanConfig == nil || m.kanbanConfig.BaseURL != baseURL {
		return ErrNotFound
	}
	if tokenEnc == nil {
		m.kanbanConfig.TokenEnc = nil
	} else {
		m.kanbanConfig.TokenEnc = append([]byte(nil), tokenEnc...)
	}
	if expiresAt == nil {
		m.kanbanConfig.TokenExpiresAt = nil
	} else {
		t := *expiresAt
		m.kanbanConfig.TokenExpiresAt = &t
	}
	m.kanbanConfig.UpdatedBy = updatedBy
	m.kanbanConfig.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemStore) DeleteClusterKanbanConfig(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kanbanConfig = nil // idempotent: absent => still nil
	return nil
}

// --- kanban integration (Feature E) ---

// claimKey is the kanban_claims natural key (linkID, documentID).
// --- integrations (D19 / F5) ---

func (m *MemStore) CreateIntegration(_ context.Context, in *domain.Integration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.integrations {
		if e.ProjectID == in.ProjectID && e.Name == in.Name {
			return fmt.Errorf("create integration: %w", ErrAlreadyExists)
		}
	}
	cp := *in
	if in.CredType == "" {
		cp.CredType = domain.CredTypePAT
	}
	cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
	m.integrations[in.ID] = cp
	return nil
}

func (m *MemStore) GetIntegration(_ context.Context, id string) (*domain.Integration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.integrations[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := in
	cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
	return &cp, nil
}

func (m *MemStore) ListIntegrationsByProject(_ context.Context, projectID string) ([]domain.Integration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Integration, 0)
	for _, in := range m.integrations {
		if in.ProjectID == projectID {
			cp := in
			cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateIntegration(_ context.Context, in *domain.Integration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.integrations[in.ID]
	if !ok {
		return ErrNotFound
	}
	// Name uniqueness within the project (excluding this row).
	for id, e := range m.integrations {
		if id != in.ID && e.ProjectID == cur.ProjectID && e.Name == in.Name {
			return fmt.Errorf("update integration: %w", ErrAlreadyExists)
		}
	}
	cur.Name = in.Name
	cur.TokenEnc = append([]byte(nil), in.TokenEnc...)
	cur.BotUsername = in.BotUsername
	cur.UpdatedAt = time.Now().UTC()
	m.integrations[in.ID] = cur
	return nil
}

func (m *MemStore) DeleteIntegration(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[id]; !ok {
		return ErrNotFound
	}
	delete(m.integrations, id)
	// Null the FK on any service that referenced it (ON DELETE SET NULL parity).
	for sid, svc := range m.services {
		if svc.IntegrationID != nil && *svc.IntegrationID == id {
			svc.IntegrationID = nil
			m.services[sid] = svc
		}
	}
	return nil
}

func (m *MemStore) CountServicesUsingIntegration(_ context.Context, integrationID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, svc := range m.services {
		if svc.IntegrationID != nil && *svc.IntegrationID == integrationID {
			n++
		}
	}
	return n, nil
}

func claimKey(linkID, documentID string) string { return linkID + "|" + documentID }

func (m *MemStore) CreateKanbanLink(_ context.Context, l *domain.KanbanLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.kanbanLinks {
		if e.WorkspaceID == l.WorkspaceID && e.BoardRef == l.BoardRef {
			return fmt.Errorf("create kanban link: %w", ErrAlreadyExists)
		}
	}
	cp := *l
	if cp.BoardStatus == "" {
		cp.BoardStatus = domain.KanbanBoardOK // mirror the pg DEFAULT
	}
	m.kanbanLinks[l.ID] = cp
	return nil
}

func (m *MemStore) GetKanbanLink(_ context.Context, id string) (*domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Deep-copy the token blob + expiry pointer so a caller can't mutate the
	// stored row through the returned copy (matches GetClusterKanbanConfig).
	cp := l
	if l.TokenEnc != nil {
		cp.TokenEnc = append([]byte(nil), l.TokenEnc...)
	}
	if l.TokenExpiresAt != nil {
		t := *l.TokenExpiresAt
		cp.TokenExpiresAt = &t
	}
	return &cp, nil
}

func (m *MemStore) ListKanbanLinks(_ context.Context) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.KanbanLink, 0, len(m.kanbanLinks))
	for _, l := range m.kanbanLinks {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListKanbanLinksByProject(_ context.Context, projectID string) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.KanbanLink, 0)
	for _, l := range m.kanbanLinks {
		if l.ProjectID == projectID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListEnabledKanbanLinks(_ context.Context) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KanbanLink
	for _, l := range m.kanbanLinks {
		if l.Enabled {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) SetKanbanLinkToken(_ context.Context, id string, tokenEnc []byte, expiresAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return ErrNotFound
	}
	// Copy the blob defensively (callers may reuse their buffer); nil clears.
	if tokenEnc == nil {
		l.TokenEnc = nil
	} else {
		l.TokenEnc = append([]byte(nil), tokenEnc...)
	}
	// token_expires_at follows the token: nil (manual paste/clear) => NULL; a
	// device-flow expiry copies the value (D28).
	if expiresAt == nil {
		l.TokenExpiresAt = nil
	} else {
		t := *expiresAt
		l.TokenExpiresAt = &t
	}
	l.UpdatedAt = time.Now().UTC()
	m.kanbanLinks[id] = l
	return nil
}

func (m *MemStore) SetKanbanLinkBoardStatus(_ context.Context, id, status, canonicalRef, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return ErrNotFound
	}
	l.BoardStatus = status
	if canonicalRef != "" {
		l.BoardRef = canonicalRef
	}
	if title != "" {
		l.BoardTitle = title
	}
	l.UpdatedAt = time.Now().UTC()
	m.kanbanLinks[id] = l
	return nil
}

func (m *MemStore) DeleteKanbanLink(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.kanbanLinks[id]; !ok {
		return ErrNotFound
	}
	delete(m.kanbanLinks, id)
	// Cascade: drop claims belonging to the removed link.
	for k, c := range m.kanbanClaims {
		if c.LinkID == id {
			delete(m.kanbanClaims, k)
		}
	}
	return nil
}

func (m *MemStore) EnsureKanbanClaim(_ context.Context, linkID, documentID, documentPath string) (*domain.KanbanClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	if c, ok := m.kanbanClaims[key]; ok {
		cp := c
		return &cp, nil
	}
	c := domain.KanbanClaim{
		ID:           domain.NewID(),
		LinkID:       linkID,
		DocumentID:   documentID,
		DocumentPath: documentPath,
		ClaimedAt:    time.Now().UTC(),
	}
	m.kanbanClaims[key] = c
	cp := c
	return &cp, nil
}

func (m *MemStore) SetKanbanClaimRun(_ context.Context, linkID, documentID, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return ErrNotFound
	}
	if c.RunID == "" {
		c.RunID = runID
		m.kanbanClaims[key] = c
	}
	return nil
}

func (m *MemStore) MarkKanbanNotConfiguredNotified(_ context.Context, linkID, documentID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return false, ErrNotFound
	}
	if c.NotifiedNotConfiguredAt != nil {
		return false, nil
	}
	t := at
	c.NotifiedNotConfiguredAt = &t
	m.kanbanClaims[key] = c
	return true, nil
}

func (m *MemStore) ListKanbanRunsAwaitingWriteback(ctx context.Context) ([]KanbanWriteback, error) {
	m.mu.Lock()
	claims := make([]domain.KanbanClaim, 0)
	for _, c := range m.kanbanClaims {
		if c.RunID != "" && c.WritebackAt == nil {
			claims = append(claims, c)
		}
	}
	linkByID := map[string]domain.KanbanLink{}
	for _, l := range m.kanbanLinks {
		linkByID[l.ID] = l
	}
	m.mu.Unlock()

	sort.Slice(claims, func(i, j int) bool { return claims[i].ClaimedAt.Before(claims[j].ClaimedAt) })
	var out []KanbanWriteback
	for _, c := range claims {
		run, err := m.GetRun(ctx, c.RunID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if !run.Status.Terminal() {
			continue
		}
		link, ok := linkByID[c.LinkID]
		if !ok {
			continue // link removed; nothing to write back to
		}
		out = append(out, KanbanWriteback{Claim: c, Run: *run, Link: link})
	}
	return out, nil
}

func (m *MemStore) MarkKanbanWriteback(_ context.Context, linkID, documentID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return false, ErrNotFound
	}
	if c.WritebackAt != nil {
		return false, nil
	}
	t := at
	c.WritebackAt = &t
	m.kanbanClaims[key] = c
	return true, nil
}

// --- Schedules (F11 / D24) --------------------------------------------------

func (m *MemStore) CreateSchedule(_ context.Context, sc *domain.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[sc.ID] = *sc
	return nil
}

func (m *MemStore) GetSchedule(_ context.Context, id string) (*domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := sc
	return &cp, nil
}

func (m *MemStore) ListSchedulesByService(_ context.Context, serviceID string) ([]domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Schedule, 0)
	for _, sc := range m.schedules {
		if sc.ServiceID == serviceID {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListEnabledSchedules(_ context.Context) ([]domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Schedule
	for _, sc := range m.schedules {
		if sc.Enabled {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateSchedule(_ context.Context, sc *domain.Schedule, resetWindow bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.schedules[sc.ID]
	if !ok {
		return ErrNotFound
	}
	// Only the owner-editable fields change; last_error stays poller-owned.
	// resetWindow (cron changed / re-enabled) moves last_fired_at to NOW so the
	// next fire is computed from the edit instant — never a backfill of a
	// boundary that predates the edit (C1; mirrors the pg CASE expression).
	cur.CronExpr = sc.CronExpr
	cur.Prompt = sc.Prompt
	cur.Enabled = sc.Enabled
	now := time.Now().UTC()
	if resetWindow {
		t := now
		cur.LastFiredAt = &t
	}
	cur.UpdatedAt = now
	m.schedules[sc.ID] = cur
	sc.LastFiredAt = cur.LastFiredAt
	sc.UpdatedAt = cur.UpdatedAt
	return nil
}

func (m *MemStore) DeleteSchedule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.schedules[id]; !ok {
		return ErrNotFound
	}
	delete(m.schedules, id)
	return nil
}

func (m *MemStore) AdvanceSchedule(_ context.Context, id string, prevFired *time.Time, newFired time.Time, lastErr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return false, ErrNotFound
	}
	// Conditional claim: the row's current last_fired_at must equal prevFired
	// (both nil, or same instant) — the SQL `IS NOT DISTINCT FROM` semantics. A
	// racing advance that already moved it loses here (won=false).
	if !timePtrEqual(sc.LastFiredAt, prevFired) {
		return false, nil
	}
	t := newFired
	sc.LastFiredAt = &t
	sc.LastError = lastErr
	sc.UpdatedAt = time.Now().UTC()
	m.schedules[id] = sc
	return true, nil
}

func (m *MemStore) SetScheduleLastError(_ context.Context, id, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return ErrNotFound
	}
	sc.LastError = lastErr
	sc.UpdatedAt = time.Now().UTC()
	m.schedules[id] = sc
	return nil
}

// --- PR review Automations --------------------------------------------------

func cloneAutomation(a domain.Automation) domain.Automation {
	a.Events = append([]domain.AutomationEvent(nil), a.Events...)
	return a
}

func (m *MemStore) CreateAutomation(_ context.Context, a *domain.Automation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.automations[a.ID]; exists {
		return ErrAlreadyExists
	}
	m.automations[a.ID] = cloneAutomation(*a)
	return nil
}

func (m *MemStore) GetAutomation(_ context.Context, id string) (*domain.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.automations[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneAutomation(a)
	return &cp, nil
}

func (m *MemStore) ListAutomationsByService(_ context.Context, serviceID string) ([]domain.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Automation, 0)
	for _, a := range m.automations {
		if a.ServiceID == serviceID {
			out = append(out, cloneAutomation(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateAutomation(_ context.Context, a *domain.Automation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.automations[a.ID]; !ok {
		return ErrNotFound
	}
	m.automations[a.ID] = cloneAutomation(*a)
	return nil
}

func (m *MemStore) DeleteAutomation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.automations[id]; !ok {
		return ErrNotFound
	}
	delete(m.automations, id)
	return nil
}

func (m *MemStore) RecordAutomationDispatch(_ context.Context, id string, at time.Time, runID, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.automations[id]
	if !ok {
		return ErrNotFound
	}
	a.LastTriggeredAt = &at
	a.LastRunID = runID
	a.LastError = lastErr
	a.UpdatedAt = time.Now().UTC()
	m.automations[id] = a
	return nil
}

func (m *MemStore) UpsertWebhookBinding(_ context.Context, b *domain.WebhookBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *b
	if current, ok := m.webhookBindings[b.ServiceID]; ok {
		// Match the Postgres upsert: reconciling the provider hook updates sync
		// state without erasing the last delivery observation.
		next.LastDeliveryAt = current.LastDeliveryAt
		next.LastDeliveryStatus = current.LastDeliveryStatus
	}
	m.webhookBindings[b.ServiceID] = next
	return nil
}

func (m *MemStore) GetWebhookBinding(_ context.Context, serviceID string) (*domain.WebhookBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.webhookBindings[serviceID]
	if !ok {
		return nil, ErrNotFound
	}
	return &b, nil
}

func (m *MemStore) RecordWebhookDelivery(_ context.Context, serviceID string, at time.Time, status, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.webhookBindings[serviceID]
	if !ok {
		return ErrNotFound
	}
	b.LastDeliveryAt = &at
	b.LastDeliveryStatus = status
	b.LastError = lastErr
	b.UpdatedAt = time.Now().UTC()
	m.webhookBindings[serviceID] = b
	return nil
}

// --- API keys (F12 / D24) ---------------------------------------------------

func (m *MemStore) CreateAPIKey(_ context.Context, k *domain.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiKeys[k.ID] = *k
	return nil
}

func (m *MemStore) GetAPIKey(_ context.Context, id string) (*domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := k
	return &cp, nil
}

// GetAPIKeyByHash excludes revoked rows — mirrors PGStore's `revoked_at IS
// NULL` filter so ErrNotFound uniformly covers "unknown hash" and "revoked
// key" (see the Store interface doc).
func (m *MemStore) GetAPIKeyByHash(_ context.Context, keyHash string) (*domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.apiKeys {
		if k.KeyHash == keyHash && k.RevokedAt == nil {
			cp := k
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListAPIKeysByProject(_ context.Context, projectID string) ([]domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.APIKey, 0)
	for _, k := range m.apiKeys {
		if k.ProjectID == projectID {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateAPIKeyLastUsed(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	t := at
	k.LastUsedAt = &t
	m.apiKeys[id] = k
	return nil
}

// RevokeAPIKey is idempotent (mirrors PGStore): a missing id or an
// already-revoked key is a silent no-op, not an error.
func (m *MemStore) RevokeAPIKey(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok || k.RevokedAt != nil {
		return nil
	}
	t := time.Now().UTC()
	k.RevokedAt = &t
	m.apiKeys[id] = k
	return nil
}

// timePtrEqual reports whether two *time.Time are both nil or point to the same
// instant — the in-memory analogue of Postgres `IS NOT DISTINCT FROM` for the
// conditional schedule advance.
func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

var _ Store = (*MemStore)(nil)
