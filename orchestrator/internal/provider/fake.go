package provider

import (
	"context"
	"fmt"
	"sync"
)

// FakeProvider is an in-memory Provider for tests. It records created PRs keyed
// by (owner/repo, head) and lets tests inject errors and pre-seed existing PRs
// to exercise the idempotency path.
type FakeProvider struct {
	mu sync.Mutex

	// prs holds the current PRs keyed by owner/repo|head.
	prs map[string]PR
	// Created records CreateDraftPR calls in order.
	Created []CreateDraftPRInput
	// nextNum assigns PR numbers.
	nextNum int

	// CreateErr / FindErr let tests inject failures.
	CreateErr error
	FindErr   error
}

// NewFakeProvider returns a ready FakeProvider.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{prs: map[string]PR{}, nextNum: 41}
}

func fakeKey(owner, repo, head string) string { return owner + "/" + repo + "|" + head }

// Seed pre-registers an existing open PR for (owner/repo, head).
func (f *FakeProvider) Seed(owner, repo, head string, pr PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[fakeKey(owner, repo, head)] = pr
}

func (f *FakeProvider) FindOpenPRByHead(_ context.Context, owner, repo, head string) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FindErr != nil {
		return nil, f.FindErr
	}
	if pr, ok := f.prs[fakeKey(owner, repo, head)]; ok {
		cp := pr
		return &cp, nil
	}
	return nil, nil
}

func (f *FakeProvider) CreateDraftPR(_ context.Context, in CreateDraftPRInput) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	f.nextNum++
	pr := PR{
		Number: f.nextNum,
		URL:    fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", in.Owner, in.Repo, f.nextNum),
	}
	f.prs[fakeKey(in.Owner, in.Repo, in.Head)] = pr
	f.Created = append(f.Created, in)
	return &pr, nil
}

// CreatedCount returns how many PRs were created (test helper).
func (f *FakeProvider) CreatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Created)
}

var _ Provider = (*FakeProvider)(nil)
