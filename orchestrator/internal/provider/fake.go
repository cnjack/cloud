package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// FakeProvider is an in-memory Provider for tests. It records created PRs keyed
// by (owner/repo, head) and lets tests inject errors and pre-seed existing PRs
// to exercise the idempotency path.
type FakeProvider struct {
	mu sync.Mutex

	// prs holds the current PRs keyed by owner/repo|head.
	prs map[string]PR
	// byNumber pre-seeds PRByNumber lookups keyed by owner/repo|number (M7).
	byNumber map[string]PR
	// Created records CreateDraftPR calls in order.
	Created []CreateDraftPRInput
	// CreatedPRs records lifecycle-aware creates, including the requested state.
	CreatedPRs []CreatePRInput
	Readied    []int
	// Reviews records CreatePRReview call bodies keyed by owner/repo|prNumber.
	Reviews []FakeReview
	// Comments records plain and managed issue comments in creation order.
	Comments []FakeComment
	// UpdatedComments records successful managed-comment updates in order.
	UpdatedComments []FakeComment
	Reactions       []FakeReaction
	// nextNum assigns PR numbers.
	nextNum int
	// nextCommentID assigns positive provider-style IDs to managed comments.
	nextCommentID int64

	// CreateErr / FindErr / ReviewErr / CommentErr let tests inject failures.
	CreateErr        error
	FindErr          error
	ReadyErr         error
	ReviewErr        error
	BatchReviewErr   error
	CommentErr       error
	UpdateCommentErr error
	ListCommentsErr  error
	CurrentUserErr   error
	Username         string
	UserID           string
	AppID            string
}

// FakeReview records one CreatePRReview call.
type FakeReview struct {
	Owner, Repo string
	Number      int
	Body        string
	Comments    []PRReviewComment
}

// FakeComment records one plain or managed issue-comment call.
type FakeComment struct {
	Owner, Repo string
	Number      int
	ID          string
	URL         string
	Body        string
	AuthorID    string
	AuthorLogin string
	AuthorAppID string
}

type FakeReaction struct {
	Owner, Repo string
	CommentID   int64
	Content     string
}

// NewFakeProvider returns a ready FakeProvider.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		prs: map[string]PR{}, byNumber: map[string]PR{}, nextNum: 41, nextCommentID: 100,
		Username: "jcode-bot", UserID: "9001", AppID: "12345",
	}
}

func fakeKey(owner, repo, head string) string { return owner + "/" + repo + "|" + head }

func fakeNumKey(owner, repo string, number int) string {
	return fmt.Sprintf("%s/%s|#%d", owner, repo, number)
}

// Seed pre-registers an existing open PR for (owner/repo, head).
func (f *FakeProvider) Seed(owner, repo, head string, pr PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[fakeKey(owner, repo, head)] = pr
}

// SeedByNumber pre-registers a PR that PRByNumber returns (M7 tests).
func (f *FakeProvider) SeedByNumber(owner, repo string, number int, pr PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byNumber[fakeNumKey(owner, repo, number)] = pr
}

// SeedIssueComment pre-registers a provider comment for recovery-path tests.
func (f *FakeProvider) SeedIssueComment(owner, repo string, issueNumber int, comment IssueComment) error {
	id, err := parseIssueCommentID(comment.ID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Comments = append(f.Comments, FakeComment{
		Owner: owner, Repo: repo, Number: issueNumber,
		ID: comment.ID, URL: comment.URL, Body: comment.Body,
		AuthorID: canonicalProviderNumericIDString(comment.AuthorID), AuthorLogin: strings.TrimSpace(comment.AuthorLogin),
		AuthorAppID: canonicalProviderNumericIDString(comment.AuthorAppID),
	})
	if id > f.nextCommentID {
		f.nextCommentID = id
	}
	return nil
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

func (f *FakeProvider) CreateDraftPR(ctx context.Context, in CreateDraftPRInput) (*PR, error) {
	return f.CreatePR(ctx, CreatePRInput{
		Owner: in.Owner, Repo: in.Repo, Head: in.Head, Base: in.Base,
		Title: in.Title, Body: in.Body, Draft: true,
	})
}

func (f *FakeProvider) CreatePR(_ context.Context, in CreatePRInput) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	f.nextNum++
	pr := PR{
		Number: f.nextNum,
		URL:    fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", in.Owner, in.Repo, f.nextNum),
		State:  "open",
		Draft:  in.Draft,
	}
	f.prs[fakeKey(in.Owner, in.Repo, in.Head)] = pr
	f.byNumber[fakeNumKey(in.Owner, in.Repo, pr.Number)] = pr
	f.CreatedPRs = append(f.CreatedPRs, in)
	f.Created = append(f.Created, CreateDraftPRInput{Owner: in.Owner, Repo: in.Repo, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body})
	return &pr, nil
}

func (f *FakeProvider) MarkPRReady(_ context.Context, owner, repo string, prNumber int) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReadyErr != nil {
		return nil, f.ReadyErr
	}
	key := fakeNumKey(owner, repo, prNumber)
	pr, ok := f.byNumber[key]
	if !ok {
		pr = PR{Number: prNumber, URL: fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", owner, repo, prNumber), State: "open", Draft: true}
	}
	if pr.State == "closed" || pr.State == "merged" || !pr.Draft {
		return &pr, nil
	}
	pr.Draft = false
	f.byNumber[key] = pr
	f.Readied = append(f.Readied, prNumber)
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

func (f *FakeProvider) CreatePRReviewBatch(_ context.Context, owner, repo string, prNumber int, review PRReview) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BatchReviewErr != nil {
		return f.BatchReviewErr
	}
	if f.ReviewErr != nil {
		return f.ReviewErr
	}
	comments := append([]PRReviewComment(nil), review.Comments...)
	f.Reviews = append(f.Reviews, FakeReview{Owner: owner, Repo: repo, Number: prNumber, Body: review.Body, Comments: comments})
	return nil
}

// PRStatus returns a synthetic open PR (or the seeded one) for tests.
func (f *FakeProvider) PRStatus(_ context.Context, owner, repo string, prNumber int) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &PR{Number: prNumber, URL: fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", owner, repo, prNumber), State: "open"}, nil
}

// PRByNumber returns the seeded PR (with head/base refs) or a synthetic one.
func (f *FakeProvider) PRByNumber(_ context.Context, owner, repo string, prNumber int) (*PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr, ok := f.byNumber[fakeNumKey(owner, repo, prNumber)]; ok {
		cp := pr
		return &cp, nil
	}
	return &PR{Number: prNumber, URL: fmt.Sprintf("http://gitea.test/%s/%s/pulls/%d", owner, repo, prNumber),
		State: "open", HeadRef: fmt.Sprintf("pr-%d-head", prNumber), BaseRef: "main",
		HeadSHA: fmt.Sprintf("%040x", prNumber+1), BaseSHA: fmt.Sprintf("%040x", prNumber)}, nil
}

// CreateIssueComment records a comment (or returns the injected error).
func (f *FakeProvider) CreateIssueComment(_ context.Context, owner, repo string, issueNumber int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommentErr != nil {
		return f.CommentErr
	}
	f.Comments = append(f.Comments, FakeComment{Owner: owner, Repo: repo, Number: issueNumber, Body: body})
	return nil
}

func (f *FakeProvider) CreateManagedIssueComment(_ context.Context, owner, repo string, issueNumber int, body string) (*IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommentErr != nil {
		return nil, f.CommentErr
	}
	f.nextCommentID++
	id := fmt.Sprint(f.nextCommentID)
	comment := FakeComment{
		Owner: owner, Repo: repo, Number: issueNumber, ID: id,
		URL:  fmt.Sprintf("http://gitea.test/%s/%s/issues/%d#issuecomment-%s", owner, repo, issueNumber, id),
		Body: body, AuthorID: canonicalProviderNumericIDString(f.UserID), AuthorLogin: strings.TrimSpace(f.Username),
		AuthorAppID: canonicalProviderNumericIDString(f.AppID),
	}
	f.Comments = append(f.Comments, comment)
	return &IssueComment{ID: comment.ID, URL: comment.URL, Body: comment.Body,
		AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin, AuthorAppID: comment.AuthorAppID}, nil
}

func (f *FakeProvider) UpdateIssueComment(_ context.Context, owner, repo string, issueNumber int, commentID, body string) (*IssueComment, error) {
	if _, err := parseIssueCommentID(commentID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UpdateCommentErr != nil {
		return nil, f.UpdateCommentErr
	}
	for i := len(f.Comments) - 1; i >= 0; i-- {
		comment := f.Comments[i]
		if comment.Owner != owner || comment.Repo != repo || comment.Number != issueNumber || comment.ID != commentID {
			continue
		}
		comment.Body = body
		f.Comments[i] = comment
		f.UpdatedComments = append(f.UpdatedComments, comment)
		return &IssueComment{ID: comment.ID, URL: comment.URL, Body: comment.Body,
			AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin, AuthorAppID: comment.AuthorAppID}, nil
	}
	return nil, fmt.Errorf("fake issue comment %s not found", commentID)
}

func (f *FakeProvider) ListIssueComments(_ context.Context, owner, repo string, issueNumber, limit int) ([]IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListCommentsErr != nil {
		return nil, f.ListCommentsErr
	}
	limit = managedIssueCommentLimit(limit)
	type numberedComment struct {
		comment IssueComment
		id      int64
	}
	numbered := make([]numberedComment, 0, limit)
	for _, comment := range f.Comments {
		if comment.Owner == owner && comment.Repo == repo && comment.Number == issueNumber && comment.ID != "" {
			id, err := parseIssueCommentID(comment.ID)
			if err != nil {
				return nil, err
			}
			numbered = append(numbered, numberedComment{
				comment: IssueComment{ID: comment.ID, URL: comment.URL, Body: comment.Body,
					AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin, AuthorAppID: comment.AuthorAppID},
				id: id,
			})
		}
	}
	sort.Slice(numbered, func(i, j int) bool { return numbered[i].id > numbered[j].id })
	if len(numbered) > limit {
		numbered = numbered[:limit]
	}
	comments := make([]IssueComment, len(numbered))
	for i, item := range numbered {
		comments[i] = item.comment
	}
	return comments, nil
}

func (f *FakeProvider) CreateIssueCommentReaction(_ context.Context, owner, repo string, commentID int64, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reactions = append(f.Reactions, FakeReaction{Owner: owner, Repo: repo, CommentID: commentID, Content: content})
	return nil
}

func (f *FakeProvider) CurrentUser(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CurrentUserErr != nil {
		return "", f.CurrentUserErr
	}
	login := strings.TrimSpace(f.Username)
	if login == "" {
		return "", errors.New("provider returned no usable current-user login")
	}
	return login, nil
}

func (f *FakeProvider) CurrentUserIdentity(_ context.Context) (ProviderUserIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CurrentUserErr != nil {
		return ProviderUserIdentity{}, f.CurrentUserErr
	}
	identity := ProviderUserIdentity{
		ID:    canonicalProviderNumericIDString(f.UserID),
		Login: strings.TrimSpace(f.Username),
		AppID: canonicalProviderNumericIDString(f.AppID),
	}
	if identity.ID == "" && identity.Login == "" && identity.AppID == "" {
		return ProviderUserIdentity{}, errors.New("provider returned no usable current-user identity")
	}
	return identity, nil
}

// CommentCount returns how many issue comments were posted (test helper).
func (f *FakeProvider) CommentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Comments)
}

// CreatedCount returns how many PRs were created (test helper).
func (f *FakeProvider) CreatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.CreatedPRs)
}

func (f *FakeProvider) ReadyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Readied)
}

// ReviewCount returns how many review comments were posted (test helper).
func (f *FakeProvider) ReviewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Reviews)
}

var _ Provider = (*FakeProvider)(nil)
var _ BatchReviewProvider = (*FakeProvider)(nil)
var _ IssueCommentReactor = (*FakeProvider)(nil)
var _ ManagedIssueCommentProvider = (*FakeProvider)(nil)
var _ CurrentUser = (*FakeProvider)(nil)
var _ CurrentUserIdentity = (*FakeProvider)(nil)
