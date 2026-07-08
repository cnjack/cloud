package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// Publisher is notified after a run's status changes so live SSE subscribers
// can be woken. It is optional (may be nil).
type Publisher interface {
	Publish(runID string, ev domain.RunEvent)
}

// Pusher pushes a runner-produced bundle's branch to a provider (the git-CLI
// seam; gitcli.Git implements it). Injected so the push pass is unit-tested with
// a fake and no real git/remote.
type Pusher interface {
	// PushBundleBranch pushes a NEW branch (draft-PR flow). Returns the tip SHA.
	PushBundleBranch(ctx context.Context, remoteURL, bundlePath, branch string) (string, error)
	// PushBundleBranchFFOnly fast-forward pushes a bundle's branch onto an EXISTING
	// branch (M7 webhook update mode). It NEVER force-pushes. It returns the tip
	// SHA and alreadyPresent=true when the remote branch already contains the
	// bundle tip (nothing to push — treat as done); a genuine non-fast-forward
	// divergence returns an error so the reconciler retries/skips rather than
	// clobbering the PR head.
	PushBundleBranchFFOnly(ctx context.Context, remoteURL, bundlePath, branch string) (sha string, alreadyPresent bool, err error)
}

// Reconciler is the control loop.
type Reconciler struct {
	st       store.Store
	launcher k8s.JobLauncher
	cfg      *config.Config
	log      *slog.Logger
	pub      Publisher
	now      func() time.Time // injectable clock for tests

	// M3 draft-PR / review stack. When any is nil the draft-PR and review passes
	// degrade to no-ops (diff-only), so a deployment without a provider configured
	// behaves exactly like J1-J3.
	factory provider.Factory      // builds PR clients per resolved token
	pusher  Pusher                // pushes the runner bundle's branch
	creds   *credentials.Resolver // resolves the acting token (user OAuth / gitea PAT)

	// models resolves (and caches) the effective LLM config at Job launch
	// (Feature A). New seeds a cipher-less default; main.go overrides it with the
	// API server's shared instance (WithModelResolver) so a console PUT/DELETE's
	// cache invalidation is immediately visible here. Never nil.
	models *modelcfg.Resolver

	// Feature E/F6 — kanban writeback. When wired, a terminal kanban-origin run has
	// its result posted back as a card comment (and the card moved to the link's
	// done column when configured). kanbanFor builds a writer bound to a link's PAT
	// (per-link encrypted token, else the cluster fallback; D25). nil => the pass
	// is a no-op (the jtype integration is off).
	kanbanFor         func(token string) KanbanWriter
	jtypeDecrypt      func([]byte) (string, error) // opens a link's encrypted PAT (nil => no cipher)
	jtypeClusterToken string                       // JTYPE_TOKEN fallback
	consoleURL        string
	// jtypeNoted throttles the one-time per-link cluster-fallback deprecation +
	// missing-credential notices so the writeback loop does not log every tick.
	jtypeNoted sync.Map // linkID -> struct{}
	// integCredNoted parks runs whose provider-side pass (PR open / update push /
	// review post / session push) hit ErrIntegrationCredential — a PERSISTENT
	// configuration problem (broken/missing integration credential), not a
	// transient one (F5 review P1). One visible run.failure event is emitted per
	// run per process; subsequent ticks skip the run instead of endlessly
	// re-resolving + re-warning. In-memory by design: a restart re-checks once —
	// if the owner fixed the integration the pass completes, else one more note.
	integCredNoted sync.Map // runID -> struct{}
}

// KanbanWriter is the slice of *jtype.Client the writeback pass uses. Exported so
// main.go can build the token->writer factory; a fake implements it in tests.
type KanbanWriter interface {
	AddComment(ctx context.Context, workspace, docID, body string) error
	MoveCard(ctx context.Context, workspace, docID, newStatus string) error
}

// New builds a Reconciler. pub may be nil. The draft-PR / review stack is set
// separately via WithPRStack so existing callers/tests are unaffected.
func New(st store.Store, launcher k8s.JobLauncher, cfg *config.Config, log *slog.Logger, pub Publisher) *Reconciler {
	return &Reconciler{
		st:       st,
		launcher: launcher,
		cfg:      cfg,
		log:      log,
		pub:      pub,
		now:      func() time.Time { return time.Now().UTC() },
		models:   modelcfg.NewResolver(st, nil, cfg),
	}
}

// WithPRStack wires the token-holding draft-PR / review machinery (M3): a
// provider Factory (builds PR clients per resolved token), a Pusher (git CLI),
// and a credentials Resolver. Any nil leaves the draft-PR / review passes as
// no-ops (diff-only). Returns r for chaining.
func (r *Reconciler) WithPRStack(factory provider.Factory, pusher Pusher, creds *credentials.Resolver) *Reconciler {
	r.factory = factory
	r.pusher = pusher
	r.creds = creds
	return r
}

// derefStr returns the pointed-to string, or "" for a nil pointer.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// integrationCredentialParked reports whether the run was parked by a prior
// integration-credential failure this process lifetime (F5 review P1). Checked
// at the top of every provider-side pass so a parked run costs one map lookup
// per tick instead of a doomed resolve + Warn.
func (r *Reconciler) integrationCredentialParked(runID string) bool {
	_, seen := r.integCredNoted.Load(runID)
	return seen
}

// noteIntegrationCredentialFailure handles credentials.ErrIntegrationCredential
// from a provider-side pass: log it, emit ONE visible run.failure event on the
// run's timeline explaining the cause + the fix (fail-visible, CLAUDE.md red
// line #1), and park the run so subsequent ticks skip it. pass names the step
// for the log/event ("draft PR", "update push", "review post", "session push").
func (r *Reconciler) noteIntegrationCredentialFailure(ctx context.Context, runID, pass string, err error) {
	if _, seen := r.integCredNoted.LoadOrStore(runID, struct{}{}); seen {
		return // already noted this process lifetime
	}
	r.log.Warn("reconcile: integration credential unavailable; parking run (fix the integration, or restart to re-check)",
		"run", runID, "pass", pass, "err", err)
	ev, aerr := r.st.AppendInternalEvent(ctx, runID, domain.EventRunFailure, map[string]any{
		"reason": string(domain.FailurePushFailed),
		"message": pass + " skipped: " + err.Error() +
			" — fix or rotate the integration in Project Settings, then retry",
	})
	if aerr != nil {
		r.log.Error("reconcile: emit integration credential event", "run", runID, "err", aerr)
		return
	}
	if r.pub != nil {
		r.pub.Publish(runID, ev)
	}
}

// WithModelResolver replaces the default model-config resolver with a shared
// instance (the API server's, in main.go) so catalog-write cache invalidation is
// visible to Job scheduling immediately (D21). Returns r for chaining.
func (r *Reconciler) WithModelResolver(m *modelcfg.Resolver) *Reconciler {
	if m != nil {
		r.models = m
	}
	return r
}

// WithKanban wires the jtype writeback factory (Feature E/F6). clientFor builds a
// writer bound to a resolved PAT; decrypt opens a link's encrypted per-link token
// (nil when no cipher); clusterToken is the JTYPE_TOKEN fallback (D25). A nil
// clientFor leaves the pass a no-op. consoleURL is the console root used to build
// a run deep-link in the comment.
func (r *Reconciler) WithKanban(clientFor func(token string) KanbanWriter, decrypt func([]byte) (string, error), clusterToken, consoleURL string) *Reconciler {
	r.kanbanFor = clientFor
	r.jtypeDecrypt = decrypt
	r.jtypeClusterToken = clusterToken
	r.consoleURL = consoleURL
	return r
}

// Run drives the loop until ctx is cancelled, ticking every cfg.ReconcileInterval.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.ReconcileInterval)
	defer ticker.Stop()
	r.log.Info("reconciler started", "interval", r.cfg.ReconcileInterval)
	// Reconcile once immediately so the first queued run does not wait a tick.
	r.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopping")
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}

// Tick performs one reconcile pass over all non-terminal runs. Exported so tests
// (and the integration test) can drive a single deterministic pass.
func (r *Reconciler) Tick(ctx context.Context) {
	runs, err := r.st.ListRunsByStatus(ctx,
		domain.StatusQueued, domain.StatusScheduling, domain.StatusRunning,
		// Session (D22): awaiting_input runs still own a pod, so the loop must
		// observe their Job state (decide() treats a live Job as normal, an exited
		// one as the session end/failure) and count them for the live-session gate.
		domain.StatusAwaitingInput)
	if err != nil {
		r.log.Error("reconcile: list runs", "err", err)
		return
	}

	// Compute remaining capacity once per tick. Active = scheduling+running.
	active, err := r.st.CountActiveRuns(ctx)
	if err != nil {
		r.log.Error("reconcile: count active", "err", err)
		return
	}
	capacity := r.cfg.MaxConcurrentRuns - active
	unlimited := r.cfg.MaxConcurrentRuns <= 0

	// Feature B — per-project concurrency guardrail. Seed each project's active
	// (scheduling+running) count from THIS tick's run set (its sum equals the
	// cluster `active` above, since ListRunsByStatus returned exactly those). A
	// run scheduled this tick then increments its project's count the same way it
	// decrements cluster capacity, so a single tick never over-schedules a project
	// past its max_concurrent_runs. pc memoizes GetProject to one hit per project
	// per tick; the loaded project is carried into createJob (no second lookup).
	projActive := map[string]int{}
	// Feature C — per-service serialization. When PERSISTENT_WORKSPACE is on, a
	// service's runs share ONE ReadWriteOnce workspace PVC, which can attach to a
	// single pod at a time — so at most ONE non-terminal run per service may hold a
	// Job. svcActive counts each service's in-flight (scheduling+running) runs the
	// same way projActive counts a project's, and a queued run whose service is
	// already in-flight stays queued. The gate is OFF (svcActive unused) when
	// PERSISTENT_WORKSPACE is off, so the ephemeral path schedules exactly as
	// before. It composes with the per-project cap: a run must clear BOTH gates.
	//
	// Stale escape: a run stuck in Scheduling/Running forever (Job Pending
	// unschedulable, orchestrator lost the Job's terminal state, etc.) would
	// permanently block all future runs of its service — there is no other
	// consumer of STALL_TIMEOUT in the codebase today, so without this escape the
	// per-service gate (hard limit 1) deadlocks the service. svcLive counts only
	// NON-stale in-flight runs: if the sole in-flight run exceeds the stall
	// threshold the gate lets the next queued run through. The ephemeral path
	// does NOT need this — without per-service serialization a stuck run only
	// consumes one cluster slot, not a permanent service-wide lock.
	svcActive := map[string]int{}
	svcLive := map[string]int{} // non-stale in-flight count (drives the gate)
	stallLimit := staleEscapeThreshold(r.cfg.StallTimeout)
	// Session (D22): count each project's LIVE session runs (scheduling+running+
	// awaiting_input, kind=agent+session) so a new session over max_live_sessions
	// stays queued — the session analogue of the max_concurrent_runs gate.
	projLiveSessions := map[string]int{}
	for i := range runs {
		switch runs[i].Status {
		case domain.StatusScheduling, domain.StatusRunning:
			projActive[runs[i].ProjectID]++
			svcActive[runs[i].ServiceID]++
			if !isStaleRun(&runs[i], r.now(), stallLimit) {
				svcLive[runs[i].ServiceID]++
			}
		case domain.StatusAwaitingInput:
			// Session (D22): an awaiting_input run still HOLDS its pod (long-polling
			// next-prompt) and therefore the service's RWO workspace PVC — the
			// per-service serialization gate must keep counting it, or a second run
			// of the same service would schedule and hang Pending on the volume.
			// Counted as unconditionally LIVE (never stale): the pod is demonstrably
			// alive, and the idle timeout / session TTL bound how long it holds the
			// slot. It does NOT consume cluster/project concurrency
			// (max_concurrent_runs) — session capacity is governed by the dedicated
			// max_live_sessions gate below.
			svcActive[runs[i].ServiceID]++
			svcLive[runs[i].ServiceID]++
		}
		if runs[i].Session {
			switch runs[i].Status {
			case domain.StatusScheduling, domain.StatusRunning, domain.StatusAwaitingInput:
				projLiveSessions[runs[i].ProjectID]++
			}
		}
	}
	persistent := r.cfg.PersistentWorkspace
	pc := newProjectCache(r.st)

	for i := range runs {
		run := runs[i]
		var jobState k8s.JobState = k8s.JobUnknown
		if run.K8sJobName != "" {
			jobState, err = r.launcher.GetJobState(ctx, run.K8sJobName)
			if err != nil {
				r.log.Warn("reconcile: get job state", "run", run.ID, "job", run.K8sJobName, "err", err)
				continue // transient; retry next tick
			}
		}

		hasCapacity := unlimited || capacity > 0
		// Load the run's project ONCE here for a queued run we might schedule, and
		// carry it into createJob (guardrails: concurrency, timeout, injected env).
		// A load failure means we do NOT schedule blind — the run stays queued and
		// retries next tick (consistent, fail-visible; never a silent default).
		// proj stays nil for scheduling/running runs (createJob is not reached).
		var proj *domain.Project
		if run.Status == domain.StatusQueued && hasCapacity {
			p, perr := pc.get(ctx, run.ProjectID)
			if perr != nil {
				r.log.Warn("reconcile: load project for guardrails — leaving run queued", "run", run.ID, "project", run.ProjectID, "err", perr)
				continue // transient; retry next tick (do not schedule blind)
			}
			proj = p
			if lim := proj.MaxConcurrentRuns; lim != nil && *lim > 0 && projActive[run.ProjectID] >= *lim {
				r.log.Info("reconcile: project at concurrency limit — leaving run queued",
					"run", run.ID, "project", run.ProjectID, "limit", *lim, "active", projActive[run.ProjectID])
				continue
			}
			// Feature C — per-service serialization (hard limit 1) when the
			// persistent RWO workspace PVC is in play. A second queued run of the
			// same service waits for the in-flight one to reach a terminal state.
			// Uses svcLive (non-stale count): if the in-flight run has exceeded the
			// stall threshold it is no longer counted, letting the queued run escape
			// the permanent deadlock (see staleEscapeThreshold / isStaleRun).
			if persistent && svcLive[run.ServiceID] >= 1 {
				r.log.Info("reconcile: service workspace busy — leaving run queued (per-service serialization)",
					"run", run.ID, "service", run.ServiceID)
				continue
			}
			// Session (D22): the per-project live-session cap. A session run holds a
			// pod across turns (running + awaiting_input), so cap the number of them
			// per project independently of max_concurrent_runs; over the cap the new
			// session stays queued (fail-visible: it is a queued run, not a silent
			// drop), mirroring the concurrency gate above.
			if run.Session {
				limit := r.cfg.MaxLiveSessions
				if proj.MaxLiveSessions != nil && *proj.MaxLiveSessions > 0 {
					limit = *proj.MaxLiveSessions
				}
				if limit > 0 && projLiveSessions[run.ProjectID] >= limit {
					r.log.Info("reconcile: project at live-session limit — leaving session run queued",
						"run", run.ID, "project", run.ProjectID, "limit", limit, "live", projLiveSessions[run.ProjectID])
					continue
				}
			}
		}
		d := decide(run, jobState, hasCapacity)
		if d.Action == ActionNone {
			continue
		}
		if r.apply(ctx, &run, d, proj) && d.Action == ActionCreateJob {
			capacity--                  // consumed a cluster slot this tick
			projActive[run.ProjectID]++ // …and a per-project slot
			svcActive[run.ServiceID]++  // …and the service's total in-flight (Feature C)
			svcLive[run.ServiceID]++    // …a freshly scheduled run is never stale
			if run.Session {
				projLiveSessions[run.ProjectID]++ // …and a per-project live session (D22)
			}
		}
	}

	// Reap Jobs left attached to terminal runs (canceled-racing-schedule, or a
	// cancel whose best-effort delete failed). See cleanupTerminalJobs.
	r.cleanupTerminalJobs(ctx)

	// Push branches + open draft PRs for succeeded draft_pr agent runs that
	// uploaded a bundle but have no PR yet (M3). Idempotent: a run stays in the
	// scan until pr_url is stamped.
	r.reconcilePRs(ctx)

	// Fast-forward push webhook @mention task bundles back onto their existing PR
	// head branch (M7 update mode). Idempotent via commit_sha.
	r.reconcileUpdatePushes(ctx)

	// Post AI-review comments for succeeded review runs whose output has not been
	// posted to their target PR yet (M3/M5). Idempotent via review_posted_at.
	r.reconcileReviews(ctx)

	// Feature E — write finished kanban-origin runs back to their cards (comment
	// + optional move to the done column). Idempotent via writeback_at. No-op
	// when the jtype client is not wired.
	r.reconcileKanbanWriteback(ctx)

	// Session (D22) — push each session run's per-turn bundle: open the draft PR
	// on the first turn, ff-update the same branch on later turns. Idempotent via
	// bundle_rev/pushed_rev. No-op when the draft-PR stack is not configured.
	r.reconcileSessionPushes(ctx)

	// Session (D22) — finalize awaiting_input runs that have sat idle past the
	// effective session_idle_timeout (idle reclaim: sets the finalize flag so the
	// runner exits and the run converges to succeeded).
	r.reconcileSessionIdle(ctx)
}

// reconcilePRs pushes the branch and opens a draft PR for each succeeded
// draft_pr agent run that uploaded a bundle but has no PR yet (M3). It is a
// no-op when the draft-PR stack is not configured. Each run is idempotent: an
// existing open PR for the head branch wins (covering a crash between
// push/create and persist), the push is up-to-date-tolerant, and MarkPRCreated
// is first-writer-wins so two ticks cannot double-record.
func (r *Reconciler) reconcilePRs(ctx context.Context) {
	if r.factory == nil || r.pusher == nil || r.creds == nil {
		return // draft-PR flow disabled — diff-only.
	}
	runs, err := r.st.ListRunsAwaitingPR(ctx)
	if err != nil {
		r.log.Error("reconcile: list runs awaiting pr", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile pr: get service", "run", run.ID, "err", err)
			continue
		}
		if !shouldOpenPR(run, *svc, true) {
			continue
		}
		r.openDraftPR(ctx, &run, svc)
	}
}

// openDraftPR resolves the acting token, pushes the run's bundle branch, then
// looks up (idempotency) or creates the draft PR on the triggering user's
// behalf, persists pr_url/pr_number, and emits a run.status refresh. Any step
// failing leaves the run succeeded with pr_url empty so the next tick retries.
// NEVER merges, NEVER triggers CI (hard gate).
func (r *Reconciler) openDraftPR(ctx context.Context, run *domain.Run, svc *domain.Service) {
	if r.integrationCredentialParked(run.ID) {
		return // parked (P1): credential problem already surfaced once
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile pr: bad repo_owner_name", "run", run.ID, "repo", svc.RepoOwnerName)
		return
	}
	branch := run.GitBranch // recorded when the bundle was received (jcode/run-<id>)

	// Resolve the credential to act with: the service's integration bot token when
	// bound (D19 / F5), else user OAuth / gitea PAT. The token value is never
	// logged — only its source label.
	tok, err := r.creds.ResolveForService(ctx, svc, run.TriggeredByUserID)
	if err != nil {
		if errors.Is(err, credentials.ErrIntegrationCredential) {
			// Persistent config error (P1): surface once + park, never spin.
			r.noteIntegrationCredentialFailure(ctx, run.ID, "draft PR", err)
			return
		}
		r.log.Warn("reconcile pr: no credential; leaving diff-only", "run", run.ID, "provider", svc.Provider, "err", err)
		return // retry next tick (a user may bind the provider later)
	}
	prov, err := r.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
	if err != nil {
		r.log.Warn("reconcile pr: build client", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}

	// Idempotency: an existing open PR for this head branch wins (covers a crash
	// after push/create but before persist, and a manually opened PR).
	pr, err := prov.FindOpenPRByHead(ctx, owner, repo, branch)
	if err != nil {
		r.log.Warn("reconcile pr: find existing", "run", run.ID, "err", err)
		return
	}
	if pr == nil {
		// Push the branch from the stored bundle, then open the PR.
		sha, perr := r.pushRunBundle(ctx, run, svc, tok, branch)
		if perr != nil {
			r.log.Warn("reconcile pr: push branch", "run", run.ID, "src", tok.Source, "err", perr)
			return // transient; retry next tick
		}
		if sha != "" {
			if _, err := r.st.SetRunGit(ctx, run.ID, branch, sha); err != nil {
				r.log.Warn("reconcile pr: record commit sha", "run", run.ID, "err", err)
			}
			run.CommitSHA = sha
		}
		pr, err = prov.CreateDraftPR(ctx, provider.CreateDraftPRInput{
			Owner: owner, Repo: repo, Head: branch, Base: svc.DefaultBranch,
			Title: prTitle(run.Prompt), Body: prBody(run, r.prTriggerAttribution(ctx, run, svc)),
		})
		if err != nil {
			// The branch is pushed; a create failure may be a race with another
			// tick that already opened it — re-find before giving up.
			if found, ferr := prov.FindOpenPRByHead(ctx, owner, repo, branch); ferr == nil && found != nil {
				pr = found
			} else {
				r.log.Warn("reconcile pr: create draft", "run", run.ID, "err", err)
				return
			}
		} else {
			r.log.Info("reconcile pr: opened draft PR", "run", run.ID, "pr", pr.Number, "url", pr.URL, "src", tok.Source)
		}
	}

	committed, err := r.st.MarkPRCreated(ctx, run.ID, pr.URL, pr.Number)
	if err != nil {
		r.log.Error("reconcile pr: mark pr created", "run", run.ID, "err", err)
		return
	}
	// Re-emit run.status so the SSE stream carries pr_url to a live console.
	r.emitStatus(ctx, committed)
}

// pushRunBundle writes the run's stored bundle to a temp file and pushes its
// branch to the provider using an authenticated clone/push URL. The temp file
// (and the URL's credential) never persist. Returns the pushed branch tip SHA.
func (r *Reconciler) pushRunBundle(ctx context.Context, run *domain.Run, svc *domain.Service, tok credentials.Token, branch string) (string, error) {
	bundle, err := r.st.GetRunBundle(ctx, run.ID)
	if err != nil {
		return "", fmt.Errorf("load bundle: %w", err)
	}
	f, err := os.CreateTemp("", "jcloud-bundle-*.bundle")
	if err != nil {
		return "", fmt.Errorf("temp bundle: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		return "", fmt.Errorf("write bundle: %w", err)
	}
	f.Close()

	rawURL := domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
	if rawURL == "" {
		return "", fmt.Errorf("could not derive push URL for service %s", svc.ID)
	}
	authed := tok.AuthedURL(rawURL, svc.Provider)
	return r.pusher.PushBundleBranch(ctx, authed, f.Name(), branch)
}

// reconcileUpdatePushes fast-forward pushes each succeeded webhook @mention task
// bundle back onto its existing PR head branch (M7 update mode). No-op when the
// draft-PR stack is not configured. Idempotent: a run stays in the scan until its
// commit_sha is stamped (on a successful push, or when the remote already carries
// the change). It NEVER opens a new PR — the existing PR auto-updates.
func (r *Reconciler) reconcileUpdatePushes(ctx context.Context) {
	if r.factory == nil || r.pusher == nil || r.creds == nil {
		return
	}
	runs, err := r.st.ListRunsAwaitingUpdatePush(ctx)
	if err != nil {
		r.log.Error("reconcile: list runs awaiting update push", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile update: get service", "run", run.ID, "err", err)
			continue
		}
		if !shouldUpdatePush(run, *svc, true) {
			continue
		}
		r.updatePushRun(ctx, &run, svc)
	}
}

// updatePushRun ff-only pushes the run's bundle branch onto the existing PR head
// and stamps commit_sha (which removes it from the scan). A non-fast-forward
// divergence (someone pushed to the PR head after the run cloned) is logged Warn
// and left for a later tick / manual resolution — it never force-pushes and never
// spins (it stays in the scan but each tick just re-Warns until it ff-applies or
// the remote already contains the change). NEVER opens a PR.
func (r *Reconciler) updatePushRun(ctx context.Context, run *domain.Run, svc *domain.Service) {
	if r.integrationCredentialParked(run.ID) {
		return // parked (P1): credential problem already surfaced once
	}
	branch := run.GitBranch // recorded when the bundle was received (= PR head branch)
	tok, err := r.creds.ResolveForService(ctx, svc, run.TriggeredByUserID)
	if err != nil {
		if errors.Is(err, credentials.ErrIntegrationCredential) {
			r.noteIntegrationCredentialFailure(ctx, run.ID, "update push", err)
			return
		}
		r.log.Warn("reconcile update: no credential; leaving for retry", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}

	bundle, err := r.st.GetRunBundle(ctx, run.ID)
	if err != nil {
		r.log.Warn("reconcile update: load bundle", "run", run.ID, "err", err)
		return
	}
	f, err := os.CreateTemp("", "jcloud-update-*.bundle")
	if err != nil {
		r.log.Warn("reconcile update: temp bundle", "run", run.ID, "err", err)
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		r.log.Warn("reconcile update: write bundle", "run", run.ID, "err", err)
		return
	}
	f.Close()

	rawURL := domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
	if rawURL == "" {
		r.log.Warn("reconcile update: could not derive push URL", "run", run.ID)
		return
	}
	authed := tok.AuthedURL(rawURL, svc.Provider)

	sha, alreadyPresent, err := r.pusher.PushBundleBranchFFOnly(ctx, authed, f.Name(), branch)
	if err != nil {
		// Non-fast-forward (or transient): leave in the scan and retry next tick.
		// This does NOT spin CPU — the reconciler ticks on its interval.
		r.log.Warn("reconcile update: ff-only push failed (retry next tick)", "run", run.ID, "branch", branch, "src", tok.Source, "err", err)
		return
	}
	if alreadyPresent {
		r.log.Info("reconcile update: PR head already contains the change", "run", run.ID, "branch", branch)
	} else {
		r.log.Info("reconcile update: pushed onto PR head branch", "run", run.ID, "branch", branch, "src", tok.Source)
	}
	// Stamp commit_sha so the run drops out of the update scan (idempotency).
	if _, err := r.st.SetRunGit(ctx, run.ID, branch, sha); err != nil {
		r.log.Warn("reconcile update: record commit sha", "run", run.ID, "err", err)
		return
	}
	run.CommitSHA = sha
	r.emitStatus(ctx, run)
}

// reconcileReviews posts the AI-review comment for each succeeded review run
// whose output has not been posted to its target PR yet (M3/M5). No-op when the
// review stack is not configured. Idempotent: after a successful post the run is
// stamped review_posted_at and drops out of the scan.
func (r *Reconciler) reconcileReviews(ctx context.Context) {
	if r.factory == nil || r.creds == nil {
		return
	}
	runs, err := r.st.ListReviewRunsAwaitingPost(ctx)
	if err != nil {
		r.log.Error("reconcile: list review runs", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile review: get service", "run", run.ID, "err", err)
			continue
		}
		r.postReview(ctx, &run, svc)
	}
}

// postReview finds the run's target PR (by its head branch) and posts the review
// output as a comment, then stamps review_posted_at. A failure leaves the run in
// the scan for the next tick.
func (r *Reconciler) postReview(ctx context.Context, run *domain.Run, svc *domain.Service) {
	if r.integrationCredentialParked(run.ID) {
		return // parked (P1): credential problem already surfaced once
	}
	if svc.RepoKind != domain.RepoKindProvider || strings.TrimSpace(run.PRHeadBranch) == "" {
		return // not associated with a provider PR (misconfigured review run)
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile review: bad repo_owner_name", "run", run.ID)
		return
	}
	tok, err := r.creds.ResolveForService(ctx, svc, run.TriggeredByUserID)
	if err != nil {
		if errors.Is(err, credentials.ErrIntegrationCredential) {
			r.noteIntegrationCredentialFailure(ctx, run.ID, "review post", err)
			return
		}
		r.log.Warn("reconcile review: no credential", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}
	prov, err := r.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
	if err != nil {
		r.log.Warn("reconcile review: build client", "run", run.ID, "err", err)
		return
	}
	pr, err := prov.FindOpenPRByHead(ctx, owner, repo, run.PRHeadBranch)
	if err != nil {
		r.log.Warn("reconcile review: find target PR", "run", run.ID, "err", err)
		return
	}
	if pr == nil {
		r.log.Warn("reconcile review: target PR not found (closed?)", "run", run.ID, "head", run.PRHeadBranch)
		return // retry next tick; PR may reopen or this stays unposted
	}
	if err := prov.CreatePRReview(ctx, owner, repo, pr.Number, run.ReviewOutput); err != nil {
		r.log.Warn("reconcile review: post comment", "run", run.ID, "pr", pr.Number, "err", err)
		return
	}
	if _, err := r.st.MarkReviewPosted(ctx, run.ID); err != nil {
		r.log.Warn("reconcile review: mark posted", "run", run.ID, "err", err)
		return
	}
	r.log.Info("reconcile review: posted review comment", "run", run.ID, "pr", pr.Number, "src", tok.Source)
}

// reconcileKanbanWriteback posts the result of each finished kanban-origin run
// back to its jtype card as a comment, and (when the link has a done column)
// moves the card there. It is a no-op when the jtype client is not wired
// (integration off). Idempotent via kanban_claims.writeback_at: a run stays in
// the scan until the marker is stamped, and MarkKanbanWriteback is
// first-writer-wins so two ticks never double-post (the only double-comment edge
// is a DB error between AddComment and the marker, which mirrors the
// reconcileReviews pattern and is rare).
func (r *Reconciler) reconcileKanbanWriteback(ctx context.Context) {
	if r.kanbanFor == nil {
		return
	}
	pending, err := r.st.ListKanbanRunsAwaitingWriteback(ctx)
	if err != nil {
		r.log.Error("reconcile kanban: list pending writebacks", "err", err)
		return
	}
	for i := range pending {
		wb := pending[i]
		r.writebackCard(ctx, &wb)
	}
}

// writebackCard posts the result comment for one terminal run and (for a
// SUCCEEDED run with a configured done column) moves the card there. Failed /
// canceled runs get the comment but STAY in place — a failure needs human
// attention and auto-advancing it to "done"/"review" would hide it. Any failure
// leaves the claim unmarked so the next tick retries (writeback_at is the only
// thing that removes it from the scan): a transient jtype error therefore just
// retries — it never loses the result silently.
func (r *Reconciler) writebackCard(ctx context.Context, wb *store.KanbanWriteback) {
	// Resolve this link's PAT (D25 three-state): per-link encrypted token, else the
	// cluster fallback, else fail-visibly skip. On the missing-credential path the
	// claim is left unmarked so the writeback resumes the moment an owner adds a
	// token — never silently dropped. Notices are throttled to once per link.
	token, source, err := jtype.ResolveToken(wb.Link.TokenEnc, r.jtypeDecrypt, r.jtypeClusterToken)
	if err != nil {
		if _, seen := r.jtypeNoted.LoadOrStore("err:"+wb.Link.ID, struct{}{}); !seen {
			r.log.Error("reconcile kanban: no jtype credential for link; writeback deferred",
				"link", wb.Link.ID, "run", wb.Run.ID, "err", err)
		}
		return // retry next tick (unmarked); resolves once a token is configured
	}
	if source == jtype.TokenClusterFallback {
		if _, seen := r.jtypeNoted.LoadOrStore("dep:"+wb.Link.ID, struct{}{}); !seen {
			r.log.Warn("reconcile kanban: link uses the deprecated cluster JTYPE_TOKEN fallback; set a per-link token",
				"link", wb.Link.ID)
		}
	}
	writer := r.kanbanFor(token)
	body := kanbanCommentBody(&wb.Run, r.consoleURL)

	// Move first, ONLY for a succeeded run with a done column: if it fails we
	// retry the whole pass before commenting, so the comment never lands on a
	// card that did not move. Failed/canceled runs skip the move (see doc comment).
	moveTo := ""
	if wb.Run.Status == domain.StatusSucceeded && wb.Link.DoneColumn != "" {
		moveTo = wb.Link.DoneColumn
	}
	if moveTo != "" {
		if err := writer.MoveCard(ctx, wb.Link.WorkspaceID, wb.Claim.DocumentID, moveTo); err != nil {
			r.log.Warn("reconcile kanban: move card", "run", wb.Run.ID, "doc", wb.Claim.DocumentID, "err", err)
			return // retry next tick
		}
	}
	if err := writer.AddComment(ctx, wb.Link.WorkspaceID, wb.Claim.DocumentID, body); err != nil {
		r.log.Warn("reconcile kanban: post result comment", "run", wb.Run.ID, "doc", wb.Claim.DocumentID, "err", err)
		return // retry next tick
	}
	if wrote, err := r.st.MarkKanbanWriteback(ctx, wb.Claim.LinkID, wb.Claim.DocumentID, r.now()); err != nil {
		r.log.Error("reconcile kanban: mark writeback", "run", wb.Run.ID, "err", err)
		return // retry next tick (may double-comment; matches reconcileReviews)
	} else if !wrote {
		return // a racing tick already wrote this one
	}
	r.log.Info("reconcile kanban: wrote result back to card",
		"run", wb.Run.ID, "doc", wb.Claim.DocumentID, "status", wb.Run.Status,
		"moved_to", moveTo)
}

// kanbanCommentBody renders the card comment for a terminal run. Succeeded runs
// link the draft PR (if any) + the console run view; failed/canceled runs state
// the reason. Always includes a console deep-link when consoleURL is set so the
// operator can jump to the run.
func kanbanCommentBody(run *domain.Run, consoleURL string) string {
	runLink := ""
	if consoleURL != "" {
		runLink = strings.TrimRight(consoleURL, "/") + "/runs/" + run.ID
	}
	var b strings.Builder
	switch run.Status {
	case domain.StatusSucceeded:
		b.WriteString("✅ jcode finished run `")
		b.WriteString(run.ID)
		if run.NoChanges() {
			// D18: an empty-diff run is a first-class success — say so explicitly
			// so the card reader knows nothing changed (no PR follows).
			b.WriteString("` — no code changes were needed.")
		} else {
			b.WriteString("`.")
			if run.PRURL != "" {
				b.WriteString("\n\nDraft PR: ")
				b.WriteString(run.PRURL)
			}
		}
	case domain.StatusFailed:
		b.WriteString("❌ jcode run `")
		b.WriteString(run.ID)
		b.WriteString("` failed")
		if run.FailureReason != "" {
			b.WriteString(" (")
			b.WriteString(string(run.FailureReason))
			b.WriteString(")")
		}
		if run.FailureMessage != "" {
			b.WriteString(": ")
			b.WriteString(run.FailureMessage)
		} else {
			b.WriteString(".")
		}
	case domain.StatusCanceled:
		b.WriteString("↩️ jcode run `")
		b.WriteString(run.ID)
		b.WriteString("` was canceled.")
	default:
		b.WriteString("jcode run `")
		b.WriteString(run.ID)
		b.WriteString("` reached status ")
		b.WriteString(string(run.Status))
		b.WriteString(".")
	}
	if runLink != "" {
		b.WriteString("\n\nRun details: ")
		b.WriteString(runLink)
	}
	return b.String()
}

// prBody is the PR description linking the run for traceability. attribution, when
// non-empty, is the "Triggered by …" line the bot-identity path (integration-bound
// service) adds so the PR — opened as the bot, not the human — stays traceable to
// its real trigger (D19 / F5).
func prBody(run *domain.Run, attribution string) string {
	var b strings.Builder
	b.WriteString("Draft PR opened by jcode Cloud Agent for run `")
	b.WriteString(run.ID)
	b.WriteString("`.\n\n")
	if attribution != "" {
		b.WriteString(attribution)
		b.WriteString("\n\n")
	}
	b.WriteString("**Task**\n\n")
	b.WriteString(strings.TrimSpace(run.Prompt))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Branch `%s` @ `%s`.\n\n", run.GitBranch, run.CommitSHA)
	b.WriteString("_Not auto-merged and CI is not auto-triggered — review and iterate._\n")
	return b.String()
}

// prTriggerAttribution builds the "Triggered by …" line for a PR opened under a
// service's BOT identity (integration bound; D19 / F5). For a legacy service (no
// integration) the PR is already opened as the triggering user, so no annotation
// is needed and this returns "". Webhook/kanban origins name their source; an
// API/console run names the triggering jcloud user.
func (r *Reconciler) prTriggerAttribution(ctx context.Context, run *domain.Run, svc *domain.Service) string {
	if svc == nil || svc.IntegrationID == nil || *svc.IntegrationID == "" {
		return ""
	}
	switch run.Origin {
	case domain.RunOriginWebhook:
		return "Triggered by a PR comment @mention."
	case domain.RunOriginKanban:
		return "Triggered by a jtype kanban card."
	default:
		if run.TriggeredByUserID != nil && *run.TriggeredByUserID != "" {
			if u, err := r.st.GetUser(ctx, *run.TriggeredByUserID); err == nil && u.DisplayName != "" {
				return "Triggered by @" + u.DisplayName
			}
		}
		return "Triggered via jcode Cloud."
	}
}

// projectCache memoizes GetProject for the span of ONE tick so the per-project
// guardrail lookups (concurrency limit in Tick, timeout + injected_env in
// createJob/jobEnv) cost a single store hit per project regardless of how many of
// its runs are in the pass. It is not shared across ticks — a fresh one is built
// each Tick so a mid-flight PATCH is picked up on the next pass.
type projectCache struct {
	st    store.Store
	cache map[string]*domain.Project
}

func newProjectCache(st store.Store) *projectCache {
	return &projectCache{st: st, cache: map[string]*domain.Project{}}
}

// get returns the project (memoized). Errors are NOT cached so a transient DB
// blip is retried on the next call/tick.
func (pc *projectCache) get(ctx context.Context, id string) (*domain.Project, error) {
	if p, ok := pc.cache[id]; ok {
		return p, nil
	}
	p, err := pc.st.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	pc.cache[id] = p
	return p, nil
}

// apply performs the side effects for one decision. Returns true if it made a
// scheduling change worth decrementing capacity for. Every persistence step goes
// through a targeted store mutator that re-reads the committed row and writes
// only its own fields, so a concurrent cancel/ingest can never be clobbered. proj
// is the run's project (loaded once in Tick), non-nil for the ActionCreateJob
// path so createJob applies the guardrails without a second store hit.
func (r *Reconciler) apply(ctx context.Context, run *domain.Run, d Decision, proj *domain.Project) bool {
	switch d.Action {
	case ActionCreateJob:
		return r.createJob(ctx, run, proj)

	case ActionMarkRunning:
		if committed, err := r.st.MarkRunning(ctx, run.ID, "StreamingTurn", r.now()); err != nil {
			r.log.Error("reconcile: mark running", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionMarkSucceeded:
		if committed, err := r.st.MarkSucceeded(ctx, run.ID, "Succeeded", r.now()); err != nil {
			r.log.Error("reconcile: mark succeeded", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionMarkFailed:
		// MarkFailed preserves any runner-reported failure_reason/message set via
		// a concurrent ingest; d.* fill only where the stored fields are empty.
		committed, err := r.st.MarkFailed(ctx, run.ID, "Failed", d.FailureReason, d.FailureMsg, r.now())
		if err != nil {
			r.log.Error("reconcile: mark failed", "run", run.ID, "err", err)
		} else {
			r.emitStatus(ctx, committed)
		}

	case ActionDeleteJob:
		if run.K8sJobName != "" {
			if err := r.launcher.DeleteJob(ctx, run.K8sJobName); err != nil {
				r.log.Error("reconcile: delete job", "run", run.ID, "err", err)
			}
		}
	}
	return false
}

// createJob mints a per-run token, creates the runner Job, and persists the job
// name + token hash via ScheduleRun.
//
// Token/idempotency correctness: this path runs only for a QUEUED run, which
// should have no live Job. But if a PRIOR tick created a Job with token1 and
// then failed to persist (transient DB error / crash before commit), the run is
// still queued with NO hash while a token1 Job lingers. Because CreateJob is
// idempotent-by-name and an existing Job keeps its ORIGINAL env, a naive retry
// would generate token2, no-op against the token1 Job, and persist hash(token2)
// — so the runner (token1) would 401 forever. To make the persisted hash always
// authoritative, we DELETE any same-named Job first so CreateJob always produces
// a fresh Job carrying the token we are about to persist. Deleting a
// non-existent Job is a no-op, so this is safe on the normal first-create path.
// See finding "token regen + idempotent CreateJob mismatch".
func (r *Reconciler) createJob(ctx context.Context, run *domain.Run, proj *domain.Project) bool {
	// Guardrails are a hard input: Tick loads the run's project before scheduling
	// and passes it here. A nil project would mean scheduling blind (no timeout /
	// injected-env / concurrency guardrail) — a silent downgrade (CLAUDE.md red
	// line #1), so refuse and retry next tick instead. This is unreachable on the
	// normal path (ActionCreateJob ⟹ Tick loaded the project) but guards a future
	// caller.
	if proj == nil {
		r.log.Error("reconcile: createJob without a loaded project — refusing to schedule blind", "run", run.ID)
		return false
	}
	// Fail-visible gate (CLAUDE.md red line #1): resolve the EFFECTIVE model
	// config at Job-launch time. The API gate blocks creation, but a run can be
	// queued while configured and then have its config cleared before it is
	// scheduled — in that window we must NOT silently launch a runner with no
	// model. A definitively-unconfigured model fails the run visibly; a resolve
	// ERROR is transient (DB blip, key rotation mid-flight) and is retried next
	// tick, consistent with the other transient-error paths in Tick.
	// Materialise the model the run was dispatched with (D21). run.ModelID was
	// stamped by the create-time resolution chain; nil resolves to the env
	// fallback. A model that was DELETED between queue and schedule resolves to
	// "not configured" → fail the run visibly rather than launch key-less.
	model, err := r.models.ResolveModel(ctx, derefStr(run.ModelID))
	if err != nil {
		// A cipher-not-configured error (the model has a stored key but
		// AUTH_TOKEN_KEY is unset) is a PERMANENT operator misconfiguration, not a
		// transient blip — retrying every tick forever would leave the run stuck in
		// queued invisibly. Fail it visibly (P1); every other resolve error stays
		// transient (DB blip, key rotation mid-flight).
		if errors.Is(err, auth.ErrCipherNotConfigured) {
			msg := "the model's API key cannot be decrypted — the orchestrator's AUTH_TOKEN_KEY is not configured"
			if committed, merr := r.st.MarkFailed(ctx, run.ID, "Failed", domain.FailureSetupFailed, msg, r.now()); merr != nil {
				r.log.Error("reconcile: mark failed (cipher not configured)", "run", run.ID, "err", merr)
			} else {
				r.log.Error("reconcile: run failed — model key cannot be decrypted (AUTH_TOKEN_KEY unset)", "run", run.ID)
				r.emitStatus(ctx, committed)
			}
			return false
		}
		r.log.Error("reconcile: resolve model config", "run", run.ID, "err", err)
		return false // transient; retry next tick
	}
	if !model.Configured() {
		msg := modelcfg.NotConfiguredMessage("")
		if committed, merr := r.st.MarkFailed(ctx, run.ID, "Failed", domain.FailureSetupFailed, msg, r.now()); merr != nil {
			r.log.Error("reconcile: mark failed (model not configured)", "run", run.ID, "err", merr)
		} else {
			r.log.Warn("reconcile: run failed — model not configured", "run", run.ID)
			r.emitStatus(ctx, committed)
		}
		return false
	}
	// Feature D — the runner reaches the LLM ONLY through the in-process reverse
	// proxy (it never holds the real key). Building that proxy URL requires
	// ORCH_BASE_URL; without it we cannot point the runner anywhere honest, so
	// refuse to schedule and leave the run queued (fail-visible; never inject a
	// bogus base). ORCH_BASE_URL is required in production (config.Load rejects
	// an empty value when K8s is enabled); this guards dev/API-only shapes.
	if r.cfg.OrchBaseURL == "" {
		r.log.Warn("reconcile: ORCH_BASE_URL unset — cannot build LLM proxy URL; leaving run queued", "run", run.ID)
		return false
	}

	// Feature C — per-service persistent workspace (D05). When enabled, ensure the
	// service's RWO PVC exists BEFORE launching the Job (idempotent) and mount it
	// via spec.WorkspacePVC. An ensure failure is transient: leave the run queued
	// and retry next tick rather than launch a Job that would fail to bind its
	// volume (fail-visible; no blind schedule). OFF => empty PVC name => ephemeral.
	var workspacePVC string
	if r.cfg.PersistentWorkspace {
		if err := r.launcher.EnsureWorkspacePVC(ctx, run.ServiceID, run.ProjectID); err != nil {
			r.log.Error("reconcile: ensure workspace pvc — leaving run queued", "run", run.ID, "service", run.ServiceID, "err", err)
			return false // transient; retry next tick
		}
		workspacePVC = k8s.WorkspacePVCName(run.ServiceID)
	}

	token, err := auth.GenerateRunToken()
	if err != nil {
		r.log.Error("reconcile: gen run token", "run", run.ID, "err", err)
		return false
	}
	jobName := k8s.JobName(run.ID)

	// Clear any leftover same-named Job from a prior failed-persist tick so the
	// Job we create carries the token whose hash we persist below.
	if err := r.launcher.DeleteJob(ctx, jobName); err != nil {
		r.log.Warn("reconcile: delete leftover job before create", "run", run.ID, "err", err)
	}

	// Feature B — project guardrails (timeout + injected env). run_timeout_secs
	// overrides the cluster default only when set and positive (NULL/≤0 means
	// "inherit"). `timeout` is the runner's INTERNAL budget (RUN_TIMEOUT, which
	// bounds `jcode acp`).
	timeout := r.cfg.RunTimeoutSecs
	if proj.RunTimeoutSecs != nil && *proj.RunTimeoutSecs > 0 {
		timeout = *proj.RunTimeoutSecs
	}
	// Session (D22): a session's budget is the whole-session TTL (idle waits
	// included), not a single-turn timeout. acpdrive's --timeout wraps the entire
	// session loop, so RUN_TIMEOUT and the Job deadline use session_ttl (project
	// override, else the cluster default).
	if run.Session {
		timeout = r.cfg.SessionTTLSecs
		if proj.SessionTTLSecs != nil && *proj.SessionTTLSecs > 0 {
			timeout = *proj.SessionTTLSecs
		}
	}
	// The Job's activeDeadlineSeconds is a HARD backstop and counts from pod start
	// (including clone/setup), whereas RUN_TIMEOUT only bounds the agent turn. If we
	// set them equal, k8s would SIGKILL the pod at the same instant the runner's own
	// graceful timeout fires — pre-empting the "timeout" failure classification and
	// discarding the partial diff.patch / REVIEW.md. So give the Job deadline a
	// grace margin on top of RUN_TIMEOUT (clone/setup + graceful-exit headroom).
	jobDeadline := timeout
	if timeout > 0 {
		jobDeadline = timeout + timeoutGrace(timeout)
	}

	spec := k8s.JobSpec{
		Name:           jobName,
		RunID:          run.ID,
		Env:            r.jobEnv(ctx, run, token, model, proj, timeout),
		TimeoutSeconds: jobDeadline,
		WorkspacePVC:   workspacePVC,
	}
	if err := r.launcher.CreateJob(ctx, spec); err != nil {
		r.log.Error("reconcile: create job", "run", run.ID, "err", err)
		return false
	}
	committed, err := r.st.ScheduleRun(ctx, run.ID, jobName, auth.HashToken(token), "PreparingWorkspace")
	if err != nil {
		// A concurrent cancel may have committed queued->canceled first; the Job
		// we just created is now orphaned. Delete it so it does not run to
		// completion unreferenced. The terminal-with-job cleanup path also covers
		// this, but deleting eagerly is cheap and immediate.
		r.log.Error("reconcile: schedule run", "run", run.ID, "err", err)
		if delErr := r.launcher.DeleteJob(ctx, jobName); delErr != nil {
			r.log.Warn("reconcile: delete orphaned job after schedule failure", "run", run.ID, "err", delErr)
		}
		return false
	}
	r.emitStatus(ctx, committed)
	return true
}

// cleanupTerminalJobs deletes Jobs still attached to terminal runs (e.g. a
// cancel that raced Job creation, or a cancel whose best-effort DeleteJob failed
// transiently) and stamps job_cleaned_at once the Job is confirmed gone.
// k8s_job_name is preserved as the run's historical record. This is the only
// path that reaps orphaned Jobs, since decide() never returns ActionDeleteJob
// for terminal runs.
func (r *Reconciler) cleanupTerminalJobs(ctx context.Context) {
	runs, err := r.st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		r.log.Error("reconcile: list terminal runs with job", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		if err := r.launcher.DeleteJob(ctx, run.K8sJobName); err != nil {
			r.log.Warn("reconcile: cleanup delete job", "run", run.ID, "job", run.K8sJobName, "err", err)
			continue // retry next tick; job_cleaned_at stays null so we try again
		}
		state, err := r.launcher.GetJobState(ctx, run.K8sJobName)
		if err != nil {
			r.log.Warn("reconcile: cleanup get job state", "run", run.ID, "err", err)
			continue
		}
		if state == k8s.JobMissing {
			// Stamp the cleanup marker; k8s_job_name is KEPT as historical record
			// (audit + e2e verification) — the marker alone removes the run from
			// the next tick's cleanup scan.
			if err := r.st.MarkJobCleaned(ctx, run.ID); err != nil {
				r.log.Warn("reconcile: cleanup mark job cleaned", "run", run.ID, "err", err)
			}
		}
	}
}

// emitStatus appends a run.status event (and run.failure when failed).
func (r *Reconciler) emitStatus(ctx context.Context, run *domain.Run) {
	payload := map[string]any{
		"status": string(run.Status),
		"phase":  run.Phase,
	}
	if run.Status == domain.StatusFailed {
		payload["failure_reason"] = string(run.FailureReason)
		payload["failure_message"] = run.FailureMessage
	}
	// Carry the draft-PR link so a live console updates its header without an
	// extra GET (ST-1). Only present once the PR has been opened.
	if run.PRURL != "" {
		payload["pr_url"] = run.PRURL
		payload["pr_number"] = run.PRNumber
	}
	r.emit(ctx, run.ID, domain.EventRunStatus, payload)
	if run.Status == domain.StatusFailed {
		r.emit(ctx, run.ID, domain.EventRunFailure, map[string]any{
			"reason":  string(run.FailureReason),
			"message": run.FailureMessage,
		})
	}
}

// emit appends one internally-generated event, letting the store allocate the
// next global seq atomically (no NextEventSeq race with runner ingest), and
// notifies the publisher.
func (r *Reconciler) emit(ctx context.Context, runID, typ string, payload map[string]any) {
	ev, err := r.st.AppendInternalEvent(ctx, runID, typ, payload)
	if err != nil {
		r.log.Error("reconcile: emit event", "run", runID, "type", typ, "err", err)
		return
	}
	if r.pub != nil {
		r.pub.Publish(runID, ev)
	}
}

// jobEnv assembles the runner container environment per the M3 runner contract
// (blueprint §3). token is the freshly-minted plaintext RUN_TOKEN; model is the
// EFFECTIVE model config resolved at Job-launch (Feature A) — its name still
// drives jcode's model id. Feature D: the real model.BaseURL/APIKey are NEVER
// injected. Instead MODEL_BASE_URL points at the in-process LLM reverse proxy
// (which injects the real key at forward time) and MODEL_API_KEY IS the
// RUN_TOKEN — so the decrypted key never enters the pod env and cannot be
// exfiltrated by a prompt injection. NO provider token is ever injected either:
// the runner reads (via a source bundle it fetches over the RUN_TOKEN) and
// writes a bundle it uploads; the orchestrator pushes.
func (r *Reconciler) jobEnv(ctx context.Context, run *domain.Run, token string, model modelcfg.Resolved, proj *domain.Project, timeoutSecs int64) map[string]string {
	kind := run.Kind
	if kind == "" {
		kind = domain.RunKindAgent
	}
	env := map[string]string{
		"RUN_ID":         run.ID,
		"TASK_PROMPT":    run.Prompt,
		"ORCH_BASE_URL":  r.cfg.OrchBaseURL,
		"MODEL_BASE_URL": r.llmProxyBaseURL(run.ID),
		"MODEL_API_KEY":  token, // RUN_TOKEN — the proxy's runToken gate verifies it.
		"MODEL_NAME":     model.ModelName,
		"RUN_TOKEN":      token,
		"RUN_KIND":       string(kind),
	}
	// Feature B — set the runner's internal RUN_TIMEOUT (bounds `jcode acp`) to the
	// effective per-run budget. The Job's activeDeadlineSeconds is set SEPARATELY to
	// this value PLUS a grace margin (see createJob/timeoutGrace) so the runner's
	// own graceful timeout fires first and the "timeout" failure + partial artifact
	// survive. entrypoint.sh otherwise defaults RUN_TIMEOUT to 300s.
	if timeoutSecs > 0 {
		env["RUN_TIMEOUT"] = fmt.Sprintf("%ds", timeoutSecs)
	}
	// Session (D22): switch the runner into the multi-turn acpdrive loop. Absent
	// for a single-shot run (unchanged behaviour). timeoutSecs already carries the
	// session TTL for these runs (see createJob), so acpdrive's --timeout wraps the
	// whole session including idle waits.
	if run.Session {
		env["RUN_SESSION"] = "1"
		// Resume (F9b / D23 ①②): a session run created by POST /runs/{id}/resume
		// carries ResumedFrom + the original run's AcpSessionID (copied at
		// creation). Inject it as RESUME_SESSION_ID so entrypoint.sh passes
		// acpdrive --resume and the runner drives ACP session/load instead of
		// session/new. Read from THIS run's own field (never a lookup of the
		// original, which may since be deleted); a plain session run's
		// AcpSessionID is still empty at Job-launch (it is only recorded once the
		// runner emits run.session), so it correctly gets NO resume var. Gated on
		// ResumedFrom too so a future in-place warm-wake path can't be conflated
		// with a resume-into-a-new-run.
		if run.ResumedFrom != nil && run.AcpSessionID != "" {
			env["RESUME_SESSION_ID"] = run.AcpSessionID
		}
		// Permission approval (F8b): switch acpdrive's RequestPermission into
		// forwarding mode and bound each approval wait. Only for approval-mode
		// session runs — anything else gets NEITHER var, so the runner keeps its
		// full_access default (behaviour unchanged). timeoutSecs is the session
		// TTL here, and the F8a contract requires the per-request approval
		// timeout to sit WELL below it (the whole turn blocks inside
		// RequestPermission): min(300s, TTL/4), floored at 1s (see
		// permissionTimeoutSecs) so it can never silently expand back to the
		// runner's own 300s default via a 0.
		if run.PermissionMode == domain.PermissionModeApproval {
			env["RUN_PERMISSION_MODE"] = domain.PermissionModeApproval
			env["PERMISSION_TIMEOUT_SECONDS"] = strconv.FormatInt(permissionTimeoutSecs(timeoutSecs), 10)
		}
	}
	// Feature C — tell the runner to reuse the persistent workspace: with the PVC
	// mounted at /workspace + $HOME/.jcode, entrypoint.sh fetches + hard-resets an
	// existing checkout instead of re-cloning, and enables jcode memory. Set only
	// when the cluster switch is on (the PVC is only mounted then).
	if r.cfg.PersistentWorkspace {
		env["PERSISTENT_WORKSPACE"] = "1"
	}
	svc, err := r.st.GetService(ctx, run.ServiceID)
	if err != nil {
		r.log.Error("reconcile: get service for env", "run", run.ID, "err", err)
		return env
	}
	r.addGitEnv(env, run, svc)
	// Feature B — project-scoped injected env, applied LAST (on top of the system
	// contract). Reserved keys are refused at the PATCH API; jobEnv drops (and
	// Warns on) any that slipped through a stale/legacy row so a system variable
	// can never be overridden (double insurance; CLAUDE.md fail-visible).
	r.applyInjectedEnv(env, run, proj)
	return env
}

// llmProxyBaseURL is the runner-facing LLM endpoint: the in-process reverse
// proxy mounted at /internal/v1/runs/{id}/llm (Feature D). It is deliberately
// WITHOUT a trailing /v1 — the entrypoint appends /v1 so jcode (which treats
// base_url as already including /v1) composes the same relative path it does
// against a real base, and the proxy re-attaches /v1 transparently.
func (r *Reconciler) llmProxyBaseURL(runID string) string {
	return strings.TrimRight(r.cfg.OrchBaseURL, "/") + "/internal/v1/runs/" + runID + "/llm"
}

// permissionTimeoutSecs is the per-request approval budget injected as
// PERMISSION_TIMEOUT_SECONDS for an approval-mode session (F8b):
// min(300s, sessionTTL/4). The F8a contract requires this to sit WELL below
// the session TTL — the whole turn blocks inside RequestPermission, so a
// too-large value would let one stalled approval burn the run into a hard
// RUN_TIMEOUT failure instead of a clean per-request timeout-deny. Floors:
//   - ttl <= 0 (unbounded session) → the plain 300s default;
//   - a degenerate tiny ttl → at least 1s, NEVER 0 (the runner treats an
//     absent/zero value as its own 300s default, which would silently defeat
//     the "timeout << TTL" invariant).
func permissionTimeoutSecs(sessionTTLSecs int64) int64 {
	const ceiling = 300
	if sessionTTLSecs <= 0 {
		return ceiling
	}
	t := sessionTTLSecs / 4
	if t > ceiling {
		return ceiling
	}
	if t < 1 {
		return 1
	}
	return t
}

// timeoutGrace is the headroom added to the runner's RUN_TIMEOUT to form the
// Job's hard activeDeadlineSeconds. The Job clock includes clone/setup (which
// RUN_TIMEOUT does not) plus the runner's own graceful-exit window, so the
// backstop must sit strictly later than the internal timeout. max(120s,
// timeout/10) scales with long runs while keeping a floor for short ones.
func timeoutGrace(timeoutSecs int64) int64 {
	grace := timeoutSecs / 10
	if grace < 120 {
		grace = 120
	}
	return grace
}

// staleEscapeThreshold is the per-service serialization deadlock escape
// (Feature C). STALL_TIMEOUT was defined but never consumed in the codebase; the
// per-service gate (hard limit 1) turns a run stuck in Scheduling/Running into a
// PERMANENT block on all future runs of that service. This threshold is how long
// a non-terminal run is trusted to still be making progress before the gate lets
// the next queued run through. The floor is 30m so an admin cannot accidentally
// set it dangerously low via STALL_TIMEOUT; a higher STALL_TIMEOUT (e.g. 2h) is
// honoured for long-running agent turns. 0 (disabled) => the 30m floor.
func staleEscapeThreshold(stall time.Duration) time.Duration {
	const floor = 30 * time.Minute
	if stall <= 0 || stall < floor {
		return floor
	}
	return stall
}

// isStaleRun reports whether a Scheduling/Running run has been non-terminal for
// longer than threshold — the condition under which the per-service serialization
// gate releases its hold (Feature C stale escape). The epoch is StartedAt when
// available (Running); for Scheduling (StartedAt nil) CreatedAt is a tight upper
// bound because in the persistent path the gate serializes per service, so a run
// is scheduled within a few ticks of creation once its service slot opens — there
// is no multi-minute queue wait behind same-service runs.
//
// Session runs (D22) are NEVER stale: they are long-lived BY DESIGN (a session
// legitimately runs/parks for hours within its TTL) and their pod verifiably
// holds the RWO workspace PVC the whole time — releasing the gate would only
// schedule a second pod that hangs Pending on the volume. Their lifetime is
// bounded by the session idle timeout + TTL instead.
func isStaleRun(run *domain.Run, now time.Time, threshold time.Duration) bool {
	if run.Session {
		return false
	}
	if threshold <= 0 {
		return false
	}
	epoch := run.CreatedAt
	if run.StartedAt != nil {
		epoch = *run.StartedAt
	}
	return now.Sub(epoch) > threshold
}

// applyInjectedEnv merges a project's injected_env into env, skipping any
// reserved system key (defensive — the API rejects these) and any key that would
// collide with a system variable already set (belt-and-braces: all system keys
// are reserved, so this is unreachable in practice). Nil proj / empty map is a
// no-op.
func (r *Reconciler) applyInjectedEnv(env map[string]string, run *domain.Run, proj *domain.Project) {
	if proj == nil {
		return
	}
	for k, v := range proj.InjectedEnv {
		if domain.IsReservedEnvKey(k) {
			r.log.Warn("reconcile: dropping reserved injected_env key", "run", run.ID, "project", proj.ID, "key", k)
			continue
		}
		if _, exists := env[k]; exists {
			r.log.Warn("reconcile: injected_env key collides with a system variable — skipping", "run", run.ID, "key", k)
			continue
		}
		env[k] = v
	}
}

// addGitEnv injects the M3 clone/produce contract (blueprint §3). It sets:
//
//   - BASE_BRANCH: the service default branch (checkout target / bundle base).
//   - SOURCE_MODE: "fetch" for every PROVIDER service (a UNIFIED path — the
//     orchestrator pre-clones and serves a source bundle over the RUN_TOKEN, so
//     the runner never needs a git credential and public/private is not guessed);
//     "clone" for RAW services (git://, file://, opaque http — the runner clones
//     the raw URL directly, as J1-J3). REPO_URL is set only for clone mode.
//   - GIT_MODE: "draft_pr" for a draft_pr provider service (the runner will
//     produce a bundle after a good diff), else "readonly". BRANCH_NAME is the
//     deterministic push branch for draft_pr.
//   - PR_HEAD / PR_BASE: for a review run, the branches the runner diffs.
//
// NO provider token is injected — the runner is credential-free (blueprint §0).
func (r *Reconciler) addGitEnv(env map[string]string, run *domain.Run, svc *domain.Service) {
	env["BASE_BRANCH"] = svc.DefaultBranch
	// M7 webhook @mention task: the baseline IS the PR head branch, so the agent
	// builds on the existing PR and the produced branch pushes back to it (§8).
	if run.Kind == domain.RunKindAgent && run.PRHeadBranch != "" {
		env["BASE_BRANCH"] = run.PRHeadBranch
	}

	if svc.RepoKind == domain.RepoKindProvider {
		// Unified path: fetch a source bundle from the orchestrator (no token in pod).
		env["SOURCE_MODE"] = "fetch"
	} else {
		// Raw repo: clone the opaque URL directly (anonymous / native protocol).
		env["SOURCE_MODE"] = "clone"
		env["REPO_URL"] = domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
		env["REPO_BRANCH"] = env["BASE_BRANCH"]
	}

	// Produce a bundle only for a draft_pr PROVIDER service (raw can only be
	// readonly by construction). readonly stays diff-only. For a webhook task the
	// push branch is the PR head (BRANCH_NAME == BASE_BRANCH → the entrypoint
	// bundles startSHA..HEAD onto that same branch); otherwise it is jcode/run-<id>.
	env["GIT_MODE"] = string(domain.GitModeReadonly)
	if svc.GitMode == domain.GitModeDraftPR && svc.RepoKind == domain.RepoKindProvider {
		env["GIT_MODE"] = string(domain.GitModeDraftPR)
		env["BRANCH_NAME"] = domain.RunPushBranch(run)
	}

	// Review runs diff PR_BASE...PR_HEAD (blueprint §3). Empty for agent runs.
	if run.Kind == domain.RunKindReview {
		env["PR_HEAD"] = run.PRHeadBranch
		env["PR_BASE"] = run.PRBaseBranch
	}
}
