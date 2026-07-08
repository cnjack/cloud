package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// MemStore is an in-memory Store for tests. It enforces the same state-machine
// and idempotency semantics as PGStore so tests exercise real behaviour without
// a database. It is safe for concurrent use.
type MemStore struct {
	mu         sync.Mutex
	projects   map[string]domain.Project
	services   map[string]domain.Service
	runs       map[string]domain.Run
	events     map[string][]domain.RunEvent    // keyed by runID, kept sorted by seq
	dedupe     map[string]bool                 // keyed by runID+"|"+source+"|"+client_seq
	artifacts  map[string]domain.RunArtifact   // keyed by runID+"/"+kind
	users      map[string]domain.User          // keyed by user id
	identities map[string]domain.UserIdentity  // keyed by identity id
	sessions   map[string]domain.Session       // keyed by session id
	members      map[string]domain.ProjectMember // keyed by projectID+"|"+userID
	modelCfg     *domain.ModelConfig             // single-row cluster model config (nil = unset)
	kanbanLinks  map[string]domain.KanbanLink    // keyed by link id
	kanbanClaims map[string]domain.KanbanClaim   // keyed by linkID+"|"+documentID
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		projects:   map[string]domain.Project{},
		services:   map[string]domain.Service{},
		runs:       map[string]domain.Run{},
		events:     map[string][]domain.RunEvent{},
		dedupe:     map[string]bool{},
		artifacts:  map[string]domain.RunArtifact{},
		users:      map[string]domain.User{},
		identities: map[string]domain.UserIdentity{},
		sessions:   map[string]domain.Session{},
		members:    map[string]domain.ProjectMember{},
		kanbanLinks:  map[string]domain.KanbanLink{},
		kanbanClaims: map[string]domain.KanbanClaim{},
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

// ListRunsAwaitingPR returns succeeded agent runs with a recorded branch but no
// PR yet.
func (m *MemStore) ListRunsAwaitingPR(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Kind == domain.RunKindAgent &&
			r.GitBranch != "" && r.PRURL == "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
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

// --- cluster model config (Feature A) ---

// GetModelConfig returns the single-row model config, or ErrNotFound when unset.
func (m *MemStore) GetModelConfig(_ context.Context) (*domain.ModelConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelCfg == nil {
		return nil, ErrNotFound
	}
	cp := *m.modelCfg
	if m.modelCfg.APIKeyEnc != nil {
		cp.APIKeyEnc = append([]byte(nil), m.modelCfg.APIKeyEnc...)
	}
	return &cp, nil
}

// SetModelConfig upserts the single row.
func (m *MemStore) SetModelConfig(_ context.Context, c *domain.ModelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	if c.APIKeyEnc != nil {
		cp.APIKeyEnc = append([]byte(nil), c.APIKeyEnc...)
	}
	m.modelCfg = &cp
	return nil
}

// ClearModelConfig deletes the row (no-op when already unset).
func (m *MemStore) ClearModelConfig(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelCfg = nil
	return nil
}

// --- kanban integration (Feature E) ---

// claimKey is the kanban_claims natural key (linkID, documentID).
func claimKey(linkID, documentID string) string { return linkID + "|" + documentID }

func (m *MemStore) CreateKanbanLink(_ context.Context, l *domain.KanbanLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.kanbanLinks {
		if e.WorkspaceID == l.WorkspaceID && e.BoardRef == l.BoardRef {
			return fmt.Errorf("create kanban link: %w", ErrAlreadyExists)
		}
	}
	m.kanbanLinks[l.ID] = *l
	return nil
}

func (m *MemStore) GetKanbanLink(_ context.Context, id string) (*domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := l
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

var _ Store = (*MemStore)(nil)
