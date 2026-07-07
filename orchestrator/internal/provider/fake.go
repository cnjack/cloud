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
	// Reviews records CreatePRReview call bodies keyed by owner/repo|prNumber.
	Reviews []FakeReview
	// nextNum assigns PR numbers.
	nextNum int

	// CreateErr / FindErr / ReviewErr let tests inject failures.
	CreateErr error
	FindErr   error
	ReviewErr error
}

// FakeReview records one CreatePRReview call.
type FakeReview struct {
	Owner, Repo string
	Number      int
	Body        string
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

// CreatePRReview records a review comment (or returns the injected error).
func (f *FakeProvider) CreatePRReview(_ context.Context, owner, repo string, prNumber int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReviewErr != nil {
		return f.ReviewErr
	}
	f.Reviews = append(f.Reviews, FakeReview{Owner: owner, Repo: repo, Number: prNumber, Body: body})
	return nil
}

// PRStatus returns a synthetic open PR (or the seeded one) for tests.
func (f *FakeProvider) PRStatus(_ context.Context, owner, repo string, prNumber int) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &PR{Number: prNumber, URL: fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", owner, repo, prNumber), State: "open"}, nil
}

// CreatedCount returns how many PRs were created (test helper).
func (f *FakeProvider) CreatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Created)
}

// ReviewCount returns how many review comments were posted (test helper).
func (f *FakeProvider) ReviewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Reviews)
}

var _ Provider = (*FakeProvider)(nil)
