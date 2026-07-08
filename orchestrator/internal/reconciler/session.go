package reconciler

import (
	"context"
	"os"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
)

// Session reconcile passes (D22; docs/14-cloud-v2-design.md §3).
//
//   - reconcileSessionPushes drives the per-turn draft-PR flow for session runs:
//     the first turn's bundle opens the draft PR; every later turn ff-updates the
//     same branch. Idempotent via bundle_rev/pushed_rev.
//   - reconcileSessionIdle finalizes an awaiting_input run that has sat idle past
//     the effective session_idle_timeout (idle reclaim): it sets the finalize
//     flag so next-prompt answers 410, the runner exits, and the run converges to
//     succeeded — the reconciler equivalent of the user clicking Finish.

// reconcileSessionPushes pushes each session run's pending bundle. It is a no-op
// when the draft-PR stack is not configured (diff-only). Each run stays in the
// scan (bundle_rev > pushed_rev) until the push lands, so a transient failure
// just retries next tick.
func (r *Reconciler) reconcileSessionPushes(ctx context.Context) {
	if r.factory == nil || r.pusher == nil || r.creds == nil {
		return
	}
	runs, err := r.st.ListSessionRunsAwaitingPush(ctx)
	if err != nil {
		r.log.Error("reconcile: list session runs awaiting push", "err", err)
		return
	}
	for i := range runs {
		run := runs[i]
		svc, err := r.st.GetService(ctx, run.ServiceID)
		if err != nil {
			r.log.Warn("reconcile session push: get service", "run", run.ID, "err", err)
			continue
		}
		if !sessionPushEligible(*svc) {
			// readonly / raw session run: nothing to push. Advance pushed_rev so it
			// drops out of the scan (the diff artifact still uploaded per turn).
			if _, err := r.st.SetPushedRev(ctx, run.ID, run.BundleRev, run.CommitSHA); err != nil {
				r.log.Warn("reconcile session push: clear rev (readonly)", "run", run.ID, "err", err)
			}
			continue
		}
		r.pushSessionRun(ctx, &run, svc)
	}
}

// sessionPushEligible reports whether a session run's service pushes a draft PR
// (draft_pr mode on a provider repo with owner/name). readonly/raw sessions are
// diff-only.
func sessionPushEligible(svc domain.Service) bool {
	return svc.GitMode == domain.GitModeDraftPR &&
		svc.RepoKind == domain.RepoKindProvider &&
		domain.ValidProvider(svc.Provider) &&
		svc.RepoOwnerName != ""
}

// pushSessionRun pushes the run's latest bundle onto its branch: opening the
// draft PR on the first turn (no pr_url yet) or ff-updating the same branch on
// later turns. On success it advances pushed_rev to the revision it just pushed
// (idempotency), so a run only re-pushes when a newer bundle (bundle_rev) lands.
func (r *Reconciler) pushSessionRun(ctx context.Context, run *domain.Run, svc *domain.Service) {
	rev := run.BundleRev // capture: a newer bundle after this leaves pushed_rev behind → re-push next tick
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		r.log.Warn("reconcile session push: bad repo_owner_name", "run", run.ID, "repo", svc.RepoOwnerName)
		return
	}
	branch := run.GitBranch
	tok, err := r.creds.Resolve(ctx, svc.Provider, run.TriggeredByUserID)
	if err != nil {
		r.log.Warn("reconcile session push: no credential; leaving for retry", "run", run.ID, "provider", svc.Provider, "err", err)
		return
	}

	bundle, err := r.st.GetRunBundle(ctx, run.ID)
	if err != nil {
		r.log.Warn("reconcile session push: load bundle", "run", run.ID, "err", err)
		return
	}
	f, err := os.CreateTemp("", "jcloud-session-*.bundle")
	if err != nil {
		r.log.Warn("reconcile session push: temp bundle", "run", run.ID, "err", err)
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		r.log.Warn("reconcile session push: write bundle", "run", run.ID, "err", err)
		return
	}
	f.Close()

	rawURL := domain.ServiceCloneURL(*svc, r.cfg.GiteaURL)
	if rawURL == "" {
		r.log.Warn("reconcile session push: could not derive push URL", "run", run.ID)
		return
	}
	authed := tok.AuthedURL(rawURL, svc.Provider)

	if run.PRURL == "" {
		// First turn (or PR not opened yet): open the draft PR. An already-open PR
		// for this head wins (crash between push/create and persist).
		prov, err := r.factory.PRClient(svc.Provider, tok.Value, tok.Scheme)
		if err != nil {
			r.log.Warn("reconcile session push: build client", "run", run.ID, "err", err)
			return
		}
		pr, err := prov.FindOpenPRByHead(ctx, owner, repo, branch)
		if err != nil {
			r.log.Warn("reconcile session push: find existing", "run", run.ID, "err", err)
			return
		}
		sha := ""
		if pr == nil {
			pushed, perr := r.pusher.PushBundleBranch(ctx, authed, f.Name(), branch)
			if perr != nil {
				r.log.Warn("reconcile session push: push branch", "run", run.ID, "src", tok.Source, "err", perr)
				return
			}
			sha = pushed
			pr, err = prov.CreateDraftPR(ctx, provider.CreateDraftPRInput{
				Owner: owner, Repo: repo, Head: branch, Base: svc.DefaultBranch,
				Title: prTitle(run.Prompt), Body: prBody(run),
			})
			if err != nil {
				if found, ferr := prov.FindOpenPRByHead(ctx, owner, repo, branch); ferr == nil && found != nil {
					pr = found
				} else {
					r.log.Warn("reconcile session push: create draft", "run", run.ID, "err", err)
					return
				}
			} else {
				r.log.Info("reconcile session push: opened draft PR", "run", run.ID, "pr", pr.Number, "url", pr.URL, "src", tok.Source)
			}
		}
		if _, err := r.st.MarkPRCreated(ctx, run.ID, pr.URL, pr.Number); err != nil {
			r.log.Error("reconcile session push: mark pr created", "run", run.ID, "err", err)
			return
		}
		committed, err := r.st.SetPushedRev(ctx, run.ID, rev, sha)
		if err != nil {
			r.log.Warn("reconcile session push: set pushed rev", "run", run.ID, "err", err)
			return
		}
		r.emitStatus(ctx, committed)
		return
	}

	// Later turns: ff-only update the existing PR head branch (never force-push,
	// never open a new PR).
	sha, alreadyPresent, err := r.pusher.PushBundleBranchFFOnly(ctx, authed, f.Name(), branch)
	if err != nil {
		r.log.Warn("reconcile session push: ff-only push failed (retry next tick)", "run", run.ID, "branch", branch, "src", tok.Source, "err", err)
		return
	}
	if alreadyPresent {
		r.log.Info("reconcile session push: branch already contains the change", "run", run.ID, "branch", branch)
	} else {
		r.log.Info("reconcile session push: ff-updated PR branch", "run", run.ID, "branch", branch, "src", tok.Source)
	}
	if _, err := r.st.SetPushedRev(ctx, run.ID, rev, sha); err != nil {
		r.log.Warn("reconcile session push: set pushed rev", "run", run.ID, "err", err)
	}
}

// reconcileSessionIdle finalizes awaiting_input runs that have sat idle longer
// than the effective session_idle_timeout (project override, else the cluster
// default). Setting the finalize flag makes next-prompt answer 410 so the runner
// exits gracefully and the run converges to succeeded — the same wind-down path
// as the user's Finish button, but automatic.
func (r *Reconciler) reconcileSessionIdle(ctx context.Context) {
	runs, err := r.st.ListAwaitingInputRuns(ctx)
	if err != nil {
		r.log.Error("reconcile: list awaiting-input runs", "err", err)
		return
	}
	now := r.now()
	for i := range runs {
		run := runs[i]
		if run.SessionFinalizing || run.AwaitingSince == nil {
			continue // already winding down, or no idle epoch recorded yet (cheap pre-filter)
		}
		idle := r.cfg.SessionIdleTimeoutSecs
		if proj, err := r.st.GetProject(ctx, run.ProjectID); err != nil {
			r.log.Warn("reconcile session idle: load project", "run", run.ID, "err", err)
			continue // transient; retry next tick (never finalize on a blind default)
		} else if proj.SessionIdleTimeoutSecs != nil && *proj.SessionIdleTimeoutSecs > 0 {
			idle = *proj.SessionIdleTimeoutSecs
		}
		if idle <= 0 {
			continue // idle reclaim disabled
		}
		// CONDITIONAL finalize (no TOCTOU): status / flag / awaiting_since are all
		// re-checked atomically inside the store — a message that resumed the run
		// between our list and this call leaves it untouched (finalized=false).
		cutoff := now.Add(-time.Duration(idle) * time.Second)
		finalized, err := r.st.FinalizeIdleSession(ctx, run.ID, cutoff)
		if err != nil {
			r.log.Warn("reconcile session idle: finalize", "run", run.ID, "err", err)
			continue
		}
		if !finalized {
			continue // resumed / already finalizing / not idle long enough — leave it
		}
		r.log.Info("reconcile session idle: finalizing idle session", "run", run.ID, "idle_secs", idle)
		r.emit(ctx, run.ID, domain.EventSessionFinish, map[string]any{"reason": "idle_timeout"})
	}
}
