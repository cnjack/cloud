package store

import (
	"context"
	"sort"
	"sync"

	"github.com/cnjack/jcloud/internal/domain"
)

// MemStore is an in-memory Store for tests. It enforces the same state-machine
// and idempotency semantics as PGStore so tests exercise real behaviour without
// a database. It is safe for concurrent use.
type MemStore struct {
	mu        sync.Mutex
	projects  map[string]domain.Project
	runs      map[string]domain.Run
	events    map[string][]domain.RunEvent  // keyed by runID, kept sorted by seq
	artifacts map[string]domain.RunArtifact // keyed by runID+"/"+kind
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		projects:  map[string]domain.Project{},
		runs:      map[string]domain.Run{},
		events:    map[string][]domain.RunEvent{},
		artifacts: map[string]domain.RunArtifact{},
	}
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
	return nil
}

// --- runs ---

func (m *MemStore) CreateRun(_ context.Context, r *domain.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.ID] = *r
	return nil
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

func (m *MemStore) UpdateRun(_ context.Context, r *domain.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[r.ID]
	if !ok {
		return ErrNotFound
	}
	if !domain.CanTransition(cur.Status, r.Status) {
		return ErrInvalidTransition
	}
	m.runs[r.ID] = *r
	return nil
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
		existing[e.Seq] = true
		inserted++
	}
	sort.Slice(m.events[runID], func(i, j int) bool {
		return m.events[runID][i].Seq < m.events[runID][j].Seq
	})
	return inserted, nil
}

func (m *MemStore) NextEventSeq(_ context.Context, runID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var max int64
	for _, e := range m.events[runID] {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max + 1, nil
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

var _ Store = (*MemStore)(nil)
