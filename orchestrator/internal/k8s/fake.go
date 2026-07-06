package k8s

import (
	"context"
	"sync"
)

// FakeLauncher is an in-memory JobLauncher for tests. It records created and
// deleted Jobs and lets tests drive each Job's observed state.
type FakeLauncher struct {
	mu sync.Mutex

	// States is the state returned by GetJobState per Job name. Tests set this
	// to simulate the cluster observing a Job transition.
	States map[string]JobState
	// Created records CreateJob calls in order (by name).
	Created []JobSpec
	// Deleted records DeleteJob calls in order (by name).
	Deleted []string

	// CreateErr / GetErr / DeleteErr let tests inject failures.
	CreateErr error
	GetErr    error
	DeleteErr error
}

// NewFakeLauncher returns a ready FakeLauncher.
func NewFakeLauncher() *FakeLauncher {
	return &FakeLauncher{States: map[string]JobState{}}
}

// CreateJob records the spec and marks the Job pending unless a state is preset.
func (f *FakeLauncher) CreateJob(_ context.Context, spec JobSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Created = append(f.Created, spec)
	if _, ok := f.States[spec.Name]; !ok {
		f.States[spec.Name] = JobPending
	}
	return nil
}

// GetJobState returns the preset state, or JobMissing if unknown.
func (f *FakeLauncher) GetJobState(_ context.Context, name string) (JobState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetErr != nil {
		return JobUnknown, f.GetErr
	}
	if s, ok := f.States[name]; ok {
		return s, nil
	}
	return JobMissing, nil
}

// DeleteJob records the deletion and forgets the Job's state.
func (f *FakeLauncher) DeleteJob(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	f.Deleted = append(f.Deleted, name)
	delete(f.States, name)
	return nil
}

// SetState is a test helper to drive an observed Job transition.
func (f *FakeLauncher) SetState(name string, s JobState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.States[name] = s
}

// CreatedNames returns the names of created Jobs in order.
func (f *FakeLauncher) CreatedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Created))
	for i, s := range f.Created {
		out[i] = s.Name
	}
	return out
}

var _ JobLauncher = (*FakeLauncher)(nil)
