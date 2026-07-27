package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/cnjack/jcloud/internal/provider"
)

type pagedInstallationRepoLister struct {
	pages   map[int][]provider.Repo
	queries []string
	pagesIn []int
}

func (l *pagedInstallationRepoLister) ListInstallationRepos(
	_ context.Context,
	query string,
	page, _ int,
) ([]provider.Repo, error) {
	l.queries = append(l.queries, query)
	l.pagesIn = append(l.pagesIn, page)
	return l.pages[page], nil
}

func TestListGitHubInstallationRepositoryCatalogLoadsEveryPageBeforeFiltering(t *testing.T) {
	t.Parallel()

	first := make([]provider.Repo, githubInstallationRepoPageSize)
	for i := range first {
		first[i] = provider.Repo{ID: int64(i + 1), FullName: fmt.Sprintf("cnjack/repo-%03d", i+1)}
	}
	second := []provider.Repo{
		{ID: 101, FullName: "cnjack/repo-101"},
		{ID: 102, FullName: "cnjack/other-repository"},
	}
	lister := &pagedInstallationRepoLister{pages: map[int][]provider.Repo{1: first, 2: second}}

	repos, err := listGitHubInstallationRepositoryCatalog(context.Background(), lister, "OTHER")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "cnjack/other-repository" {
		t.Fatalf("repos = %#v", repos)
	}
	if got := fmt.Sprint(lister.pagesIn); got != "[1 2]" {
		t.Fatalf("pages = %s, want [1 2]", got)
	}
	if got := fmt.Sprint(lister.queries); got != "[ ]" {
		t.Fatalf("provider queries = %s, want empty queries until all pages are loaded", got)
	}
}
