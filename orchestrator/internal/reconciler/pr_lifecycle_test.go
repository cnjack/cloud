package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
)

func TestLifecycleAwareOneShotCreatesReadyPR(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)

	project := &domain.Project{ID: domain.NewID(), Name: "ready", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", DefaultBranch: "main", GitMode: domain.GitModeDraftPR,
		PRReadyPolicy: domain.PRReadyPolicyLifecycleAware, CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: project.ID, ServiceID: svc.ID, Prompt: "ready",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunBundle(ctx, run.ID, []byte("bundle")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunGit(ctx, run.ID, "jcode/run-ready", ""); err != nil {
		t.Fatal(err)
	}

	rec.reconcilePRs(ctx)

	if len(fake.CreatedPRs) != 1 || fake.CreatedPRs[0].Draft {
		t.Fatalf("created=%+v want one ready PR", fake.CreatedPRs)
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRDraft == nil || *got.PRDraft {
		t.Fatalf("pr_draft=%v want false", got.PRDraft)
	}
}

func TestLifecycleAwareSessionBecomesReadyOnlyAfterLatestPush(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	svc, _ := st.GetService(ctx, sid)
	svc.PRReadyPolicy = domain.PRReadyPolicyLifecycleAware
	if err := st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}

	draft := true
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "finish chat",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Session: true,
		GitBranch: "jcode/run-session-ready", BundleRev: 2, PushedRev: 1,
		PRURL: "http://gitea.test/jcloud/seed/pulls/42", PRNumber: 42, PRDraft: &draft,
		Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	fake.SeedByNumber("jcloud", "seed", 42, provider.PR{Number: 42, URL: run.PRURL, State: "open", Draft: true})

	rec.reconcileSessionPRReady(ctx)
	if fake.ReadyCount() != 0 {
		t.Fatal("session became ready before its latest bundle was pushed")
	}
	if _, err := st.SetPushedRev(ctx, run.ID, 2, "sha2"); err != nil {
		t.Fatal(err)
	}
	rec.reconcileSessionPRReady(ctx)
	if fake.ReadyCount() != 1 {
		t.Fatalf("ready calls=%d want 1", fake.ReadyCount())
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRDraft == nil || *got.PRDraft || got.PRReadyAt == nil {
		t.Fatalf("ready state not persisted: draft=%v ready_at=%v", got.PRDraft, got.PRReadyAt)
	}

	// Idempotent local marker: another tick does not call the provider again.
	rec.reconcileSessionPRReady(ctx)
	if fake.ReadyCount() != 1 {
		t.Fatalf("ready retried after persistence: %d", fake.ReadyCount())
	}
}

func TestSessionStopsUpdatingAfterProviderPRWasClosed(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	draft := true
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "more work",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		GitBranch: "jcode/run-closed", BundleRev: 2, PushedRev: 1,
		PRURL: "http://gitea.test/jcloud/seed/pulls/44", PRNumber: 44, PRDraft: &draft, PRState: "open",
		Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunBundle(ctx, run.ID, []byte("turn2")); err != nil {
		t.Fatal(err)
	}
	fake.SeedByNumber("jcloud", "seed", 44, provider.PR{Number: 44, URL: run.PRURL, State: "closed", Draft: true})

	rec.reconcileSessionPushes(ctx)
	if len(pusher.ffPushed) != 0 {
		t.Fatalf("closed PR branch was updated: %v", pusher.ffPushed)
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRState != "closed" {
		t.Fatalf("pr_state=%q want closed", got.PRState)
	}
	pending, _ := st.ListSessionRunsAwaitingPush(ctx)
	for _, candidate := range pending {
		if candidate.ID == run.ID {
			t.Fatal("closed PR remained in the push retry scan")
		}
	}
}

func TestMultipleOpenPRsParksDeliveryVisibly(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	fake.FindErr = provider.ErrMultipleOpenPRs
	pusher := wirePRStack(rec, st, fake)
	_, run := seedDraftPRRun(t, st, "jcode/run-conflict")

	rec.reconcilePRs(ctx)
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRState != "conflict" {
		t.Fatalf("pr_state=%q want conflict", got.PRState)
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("ambiguous remote state pushed branch: %v", pusher.pushed)
	}
	pending, _ := st.ListRunsAwaitingPR(ctx)
	for _, candidate := range pending {
		if candidate.ID == run.ID {
			t.Fatal("conflicted run remained in the automatic retry scan")
		}
	}
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	visible := false
	for _, event := range events {
		if event.Type == domain.EventRunStatus && event.Payload["pr_state"] == "conflict" {
			visible = true
		}
	}
	if !visible {
		t.Fatalf("conflict was not emitted to the run stream: %+v", events)
	}
}

func TestUnsupportedReadyTransitionParksSession(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	fake.ReadyErr = provider.ErrUnsupportedPRTransition
	wirePRStack(rec, st, fake)
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	svc, _ := st.GetService(ctx, sid)
	svc.PRReadyPolicy = domain.PRReadyPolicyLifecycleAware
	if err := st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	draft := true
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "finish",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Session: true,
		GitBranch: "jcode/run-unsupported", BundleRev: 1, PushedRev: 1,
		PRURL: "https://gitlab.test/o/r/-/merge_requests/3", PRNumber: 3,
		PRDraft: &draft, PRState: "open", Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rec.reconcileSessionPRReady(ctx)
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRState != "provider_unsupported" || got.PRReadyAt != nil {
		t.Fatalf("unsupported transition not parked: state=%q ready_at=%v", got.PRState, got.PRReadyAt)
	}
	pending, _ := st.ListSessionRunsAwaitingPRReady(ctx)
	for _, candidate := range pending {
		if candidate.ID == run.ID {
			t.Fatal("unsupported transition remained in automatic retry scan")
		}
	}
}
