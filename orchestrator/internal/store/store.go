// Package store is the persistence layer. The Store interface is the seam the
// API and reconciler depend on; PGStore is the pgx/v5-backed implementation.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// ErrIdentityTaken is returned by AttachIdentity when the (provider, provider_uid)
// is already linked to a DIFFERENT user (the /auth/link conflict case).
var ErrIdentityTaken = errors.New("identity already linked to another user")

// ErrAlreadyExists is returned by creators (e.g. CreateKanbanLink) when a
// uniqueness constraint rejects the insert. The API maps it to a 409.
var ErrAlreadyExists = errors.New("already exists")

// EventInput is a single event to append. For AppendEvents the Seq is the
// authoritative global seq (caller-assigned). For AppendRunnerEvents the Seq is
// only the runner's client-side sequence number, used as a per-source
// idempotency key; the global seq is allocated server-side.
type EventInput struct {
	Seq     int64
	Type    string
	Payload map[string]any
}

// Event source tags. The global seq is allocated server-side per run; the
// client-supplied number is retained per-source as an idempotency key so a
// runner batch re-send is a no-op and never competes with internal events for
// the shared seq space. See migration 0002 and cloud/docs/11-api.md §5.1.
const (
	// SourceRunner tags events ingested from the runner (its own 1..N stream).
	SourceRunner = "runner"
	// SourceInternal tags orchestrator-emitted events (run.status, run.artifact,
	// run.failure).
	SourceInternal = "internal"
)

// Store is the durable persistence contract for the orchestrator.
type Store interface {
	// Projects
	CreateProject(ctx context.Context, p *domain.Project) error
	GetProject(ctx context.Context, id string) (*domain.Project, error)
	ListProjects(ctx context.Context) ([]domain.Project, error)
	UpdateProject(ctx context.Context, p *domain.Project) error
	DeleteProject(ctx context.Context, id string) error

	// Services. A service is a repository configuration inside a project; runs
	// are created against a service (multitenant blueprint §1).
	CreateService(ctx context.Context, s *domain.Service) error
	GetService(ctx context.Context, id string) (*domain.Service, error)
	ListServices(ctx context.Context, projectID string) ([]domain.Service, error)
	// GetDefaultService returns the project's service named "default" (the one
	// the compatibility shim creates and routes to). ErrNotFound if absent.
	GetDefaultService(ctx context.Context, projectID string) (*domain.Service, error)
	// ListServicesByRepo returns every service (across all projects) that targets
	// the given provider + "owner/name" repo. Used by the M7 webhook to resolve a
	// PR comment's repository to the jcloud project(s) that track it; the handler
	// then picks the first whose commenter is a member. Empty when none match.
	ListServicesByRepo(ctx context.Context, provider domain.GitProvider, repoOwnerName string) ([]domain.Service, error)
	UpdateService(ctx context.Context, s *domain.Service) error
	DeleteService(ctx context.Context, id string) error

	// --- workspace archive layer (F10 / D23 ③) ------------------------------
	// ListArchiveCandidates returns services eligible for cold archival: not
	// already archived, and with at least one run whose most-recent activity
	// (created_at) is strictly before idleBefore, AND no run currently in a
	// non-terminal state (queued/scheduling/running/awaiting_input) — an in-flight
	// run still needs the PVC. Ordered oldest-idle first. The caller (reconciler)
	// additionally verifies the PVC physically exists before archiving; the store
	// only knows run activity. A service with zero runs is never a candidate (no
	// PVC was ever created).
	ListArchiveCandidates(ctx context.Context, idleBefore time.Time) ([]ArchiveCandidate, error)
	// MarkServiceArchived stamps archived_at + archive_key once the reconciler has
	// confirmed the tarball is in object storage and the PVC deleted. Idempotent
	// overwrite (last write wins); a missing service is ErrNotFound.
	MarkServiceArchived(ctx context.Context, serviceID, archiveKey string, at time.Time) error
	// ClearServiceArchive clears archived_at + archive_key when a new run restores
	// the workspace. Idempotent (clearing an already-clear service is a no-op that
	// still returns nil); a missing service is ErrNotFound.
	ClearServiceArchive(ctx context.Context, serviceID string) error

	// Runs
	CreateRun(ctx context.Context, r *domain.Run) error
	GetRun(ctx context.Context, id string) (*domain.Run, error)
	GetRunByTokenHash(ctx context.Context, tokenHash string) (*domain.Run, error)
	// GetRunByOriginCommentID returns the run a webhook comment already triggered
	// (the M7 de-dup key), or ErrNotFound when the comment has not been seen. An
	// empty id returns ErrNotFound (never matches api-origin runs).
	GetRunByOriginCommentID(ctx context.Context, commentID string) (*domain.Run, error)
	ListRuns(ctx context.Context, projectID string, limit int) ([]domain.Run, error)
	// ListRunsByService lists runs for a single service, newest first.
	ListRunsByService(ctx context.Context, serviceID string, limit int) ([]domain.Run, error)

	// Run mutators. Each of these re-reads the committed row inside a
	// transaction (SELECT ... FOR UPDATE), validates the state-machine
	// transition against the CURRENT stored status (not a caller snapshot), and
	// applies ONLY the fields it names — so two concurrent writers can never
	// clobber each other's untouched fields (the "stale full-row" lost-update
	// hazard). Each returns the freshly-committed row so the caller never has to
	// write a stale in-memory copy back. An illegal transition returns
	// ErrInvalidTransition; a missing run returns ErrNotFound.

	// ScheduleRun moves a queued run to scheduling and is the ONLY method that
	// writes k8s_job_name and token_hash. phase is set to the given value.
	ScheduleRun(ctx context.Context, id, jobName, tokenHash, phase string) (*domain.Run, error)
	// MarkRunning moves a scheduling run to running, stamping started_at only if
	// it is currently null.
	MarkRunning(ctx context.Context, id, phase string, startedAt time.Time) (*domain.Run, error)
	// MarkSucceeded moves a run to succeeded, stamping finished_at if null.
	MarkSucceeded(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error)
	// MarkFailed moves a run to failed, stamping finished_at if null. It
	// PRESERVES any already-set failure_reason/failure_message (e.g. a specific
	// runner-reported reason): the given reason/message are written only where
	// the stored value is empty. error mirrors the resulting failure_message.
	MarkFailed(ctx context.Context, id, phase string, reason domain.FailureReason, msg string, finishedAt time.Time) (*domain.Run, error)
	// SetRunnerFailure records a runner-reported failure_reason/message WITHOUT
	// changing status, and only while the run is still non-terminal. It is
	// first-writer-wins: it writes only where the stored fields are empty, so a
	// later generic classification cannot overwrite a specific runner reason and
	// vice-versa. A no-op (already terminal, or fields already set) is not an
	// error. Returns the committed row (possibly unchanged).
	SetRunnerFailure(ctx context.Context, id string, reason domain.FailureReason, msg string) (*domain.Run, error)
	// CancelRun moves a run to canceled and stamps finished_at. It does NOT
	// touch k8s_job_name/token_hash, so the committed row it returns still names
	// the Job the caller must delete.
	CancelRun(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error)
	// MarkJobCleaned stamps job_cleaned_at once the reconciler has confirmed a
	// terminal run's Job is deleted from the cluster. It KEEPS k8s_job_name —
	// the Job name is part of the run's historical record (audit + e2e
	// verification; see 11-api.md Run schema). Idempotent: an already-set
	// job_cleaned_at is preserved. It does not change status and is a no-op if
	// the run is missing.
	MarkJobCleaned(ctx context.Context, id string) error
	// SetRunGit records the branch/commit the runner pushed (from a run.git
	// event) WITHOUT changing status. First-writer-wins per field: it writes only
	// where the stored value is empty, so a duplicate event is a no-op. It never
	// touches status/pr_url and is a no-op on a missing run. Returns the committed
	// row (possibly unchanged). This is the ONLY writer of git_branch/commit_sha.
	SetRunGit(ctx context.Context, id, branch, commitSHA string) (*domain.Run, error)
	// SetRunResult records a runner-reported run OUTCOME (from a run.result event,
	// e.g. "no_changes") WITHOUT changing status. First-writer-wins: it writes
	// only where result is still NULL, so a duplicate event is a no-op. It never
	// touches status — the reconciler still drives the terminal status from the
	// Job (an empty-diff run exits 0 → succeeded; D18) — and is a no-op on a
	// missing run. Returns the committed row (possibly unchanged). This is the
	// ONLY writer of result.
	SetRunResult(ctx context.Context, id string, result domain.RunResult) (*domain.Run, error)
	// SetRunACPSession records the run's ACP session id (from a run.session event,
	// F9b) WITHOUT changing status. First-writer-wins: it writes only where
	// acp_session_id is still empty, so a duplicate event — or a resume run whose
	// id was pre-filled at creation — is a no-op that keeps the existing id. An
	// empty id is ignored (never clears a recorded id). A no-op on a missing run.
	// Returns the committed row (possibly unchanged).
	SetRunACPSession(ctx context.Context, id, acpSessionID string) (*domain.Run, error)
	// MarkPRCreated stamps pr_url/pr_number once the orchestrator has opened (or
	// found) the draft PR (ST-1). It is IDEMPOTENT and first-writer-wins: it
	// writes only where pr_url is currently empty, so a retry that raced another
	// tick cannot double-open or clobber an already-recorded PR. It does not
	// change status and is a no-op on a missing run. Returns the committed row.
	MarkPRCreated(ctx context.Context, id, prURL string, prNumber int) (*domain.Run, error)

	// --- Session runs (D22) --------------------------------------------------

	// SetRunAwaitingInput moves a running session run to awaiting_input, stamping
	// awaiting_since (the idle-reclaim epoch) only where it is still NULL so a
	// duplicate turn-complete does not reset the idle timer. Idempotent: an
	// already-awaiting_input run is a no-op (from==to allowed). Returns the row;
	// ErrInvalidTransition from a non-running/non-awaiting state.
	SetRunAwaitingInput(ctx context.Context, id string, at time.Time) (*domain.Run, error)
	// ResumeRun moves an awaiting_input session run back to running (a delivered
	// message) and clears awaiting_since. Idempotent (already-running is a no-op).
	ResumeRun(ctx context.Context, id, phase string) (*domain.Run, error)
	// MarkSessionFinalizing sets session_finalizing so next-prompt answers 410 and
	// the runner exits gracefully. Idempotent + only while non-terminal; a no-op on
	// a terminal run is not an error. Returns the committed row.
	MarkSessionFinalizing(ctx context.Context, id string) (*domain.Run, error)
	// FinalizeIdleSession is the CONDITIONAL finalize the idle-timeout reconcile
	// pass uses (no TOCTOU): it sets session_finalizing ONLY IF the run is still
	// awaiting_input, not already finalizing, and its awaiting_since is at or
	// before cutoff — all checked atomically in the store. Returns finalized=true
	// only for the call that flipped the flag (so the caller emits the
	// session.finish event exactly once); a run that was resumed/finalized/ended
	// in between returns false, never an error.
	FinalizeIdleSession(ctx context.Context, id string, cutoff time.Time) (bool, error)

	// AppendRunMessage enqueues a follow-up prompt for a session run, allocating
	// the next per-run seq. Returns the stored message (with its seq). ErrNotFound
	// if the run does not exist.
	AppendRunMessage(ctx context.Context, runID, prompt, createdBy string) (*domain.RunMessage, error)
	// OfferNextMessage is phase 1 of the two-phase delivery. Atomically (per run):
	// if an offered-but-not-consumed message exists it is returned AGAIN verbatim
	// (fresh=false — idempotent re-delivery after a lost response); otherwise the
	// OLDEST unoffered message is stamped offered_at and returned (fresh=true).
	// Two concurrent offers can never hand out two DIFFERENT messages — they
	// serialise per run and converge on the same row. ErrNotFound when the queue
	// has nothing deliverable.
	OfferNextMessage(ctx context.Context, runID string, at time.Time) (msg *domain.RunMessage, fresh bool, err error)
	// ConsumeOfferedMessage is phase 2: stamps consumed_at on the currently
	// offered message (the turn it started has completed). consumed=false when no
	// message was offered (e.g. the first TASK_PROMPT turn) — a no-op, not an
	// error. Idempotent.
	ConsumeOfferedMessage(ctx context.Context, runID string, at time.Time) (consumed bool, err error)
	// ListRunMessages returns a run's queued messages, oldest first (tests/audit).
	ListRunMessages(ctx context.Context, runID string) ([]domain.RunMessage, error)

	// --- Session permission approval (F8b / D22) ------------------------------

	// UpsertRunPermission records a forwarded permission request (from an
	// agent.permission_request event). IDEMPOTENT and insert-only: a request_id
	// already present is left completely untouched (at-least-once event delivery
	// must never reset an already-decided/resolved row). ErrNotFound if the run
	// does not exist.
	UpsertRunPermission(ctx context.Context, p *domain.RunPermission) error
	// GetRunPermission returns one request by (run_id, request_id). ErrNotFound
	// when absent — the decision endpoint maps that to 204 (pending), NEVER 404
	// (the F8a hard constraint).
	GetRunPermission(ctx context.Context, runID, requestID string) (*domain.RunPermission, error)
	// DecideRunPermission records the user's answer, CONDITIONALLY: only while
	// the row is neither decided nor resolved (checked atomically in the store,
	// so two racing answers serialise — exactly one wins). decided=true only for
	// the call that wrote; the loser gets decided=false and the committed row
	// (never an error). ErrNotFound if the row is absent.
	DecideRunPermission(ctx context.Context, runID, requestID, optionID, decidedBy string, at time.Time) (*domain.RunPermission, bool, error)
	// ResolveRunPermission stamps the runner-reported final outcome (from an
	// agent.permission_resolved event). First-writer-wins: an already-resolved
	// row is untouched (duplicate events are no-ops); a missing row is also a
	// no-op, NOT an error — the resolved event rides a best-effort async pipeline
	// and may reference a request whose request event was never delivered.
	ResolveRunPermission(ctx context.Context, runID, requestID, optionID, resolution string, at time.Time) error
	// ListRunPermissions returns a run's permission requests, oldest first
	// (tests/audit).
	ListRunPermissions(ctx context.Context, runID string) ([]domain.RunPermission, error)

	// BumpBundleRev increments bundle_rev (a new bundle upload awaits a push) and
	// returns the committed row. Session per-turn draft-PR push cursor (D22).
	BumpBundleRev(ctx context.Context, id string) (*domain.Run, error)
	// SetPushedRev advances pushed_rev to at-least rev (monotonic) and records the
	// pushed commit_sha (the session branch tip moves each turn). An EMPTY sha
	// preserves the stored value (the PR-already-exists recovery path pushes
	// nothing, so it must not wipe the last recorded tip). Session-only; never
	// changes status. Returns the committed row.
	SetPushedRev(ctx context.Context, id string, rev int64, commitSHA string) (*domain.Run, error)
	// ListSessionRunsAwaitingPush returns session AGENT runs with a recorded branch
	// and a bundle newer than what was pushed (bundle_rev > pushed_rev), in a
	// non-final state, so the reconciler opens/updates the draft PR per turn.
	// Ordered oldest-first.
	ListSessionRunsAwaitingPush(ctx context.Context) ([]domain.Run, error)
	// ListAwaitingInputRuns returns every run currently in awaiting_input, so the
	// reconciler's idle-timeout pass can finalize the stale ones. Oldest-first.
	ListAwaitingInputRuns(ctx context.Context) ([]domain.Run, error)

	// Reconciler queries
	ListRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]domain.Run, error)
	// ListTerminalRunsWithJob returns terminal runs whose Job has not yet been
	// confirmed deleted (k8s_job_name != '' AND job_cleaned_at IS NULL), so the
	// reconciler can reap orphaned Jobs — e.g. a cancel that raced Job creation
	// — exactly once. Reaped runs keep their Job name; MarkJobCleaned is what
	// removes them from this scan.
	ListTerminalRunsWithJob(ctx context.Context) ([]domain.Run, error)
	CountActiveRuns(ctx context.Context) (int, error)
	// CountRunsByStatus returns a per-status count for the given statuses across
	// ALL projects, as a map keyed by status. Statuses not present in the store
	// are reported as 0 (every requested status is present as a key). Read-only;
	// used by the admin system snapshot (GET /api/v1/system).
	CountRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) (map[domain.RunStatus]int, error)
	// ListRunsAwaitingPR returns succeeded AGENT runs that have a recorded branch
	// (git_branch <> '', stamped when the bundle was received) but no PR yet
	// (pr_url = ''), so the reconciler can push the branch + open the draft PR
	// (M3). The mode/provider gate is applied by the reconciler after joining the
	// service. Ordered oldest-first.
	ListRunsAwaitingPR(ctx context.Context) ([]domain.Run, error)
	// ListReviewRunsAwaitingPost returns succeeded review runs with non-empty
	// review_output whose comment has not been posted yet (review_posted_at IS
	// NULL), so the reconcile review pass can post it. Ordered oldest-first.
	ListReviewRunsAwaitingPost(ctx context.Context) ([]domain.Run, error)
	// ListRunsAwaitingUpdatePush returns succeeded webhook AGENT runs whose bundle
	// was received (git_branch <> '') onto an EXISTING PR (pr_url <> '') but whose
	// ff-only push has not completed yet (commit_sha = ''). The reconciler pushes
	// the branch back to the PR head and stamps commit_sha, removing the run from
	// this scan (M7 update mode). The mode/provider gate is applied after joining
	// the service. Ordered oldest-first.
	ListRunsAwaitingUpdatePush(ctx context.Context) ([]domain.Run, error)
	// SetReviewOutput records a review run's markdown output (runner POST
	// .../review) without changing status, first-writer-wins. Returns the row.
	SetReviewOutput(ctx context.Context, id, md string) (*domain.Run, error)
	// MarkReviewPosted stamps review_posted_at once the review comment is posted.
	// Idempotent + first-writer-wins: returns posted=true only for the tick that
	// stamped it, so two ticks never double-post.
	MarkReviewPosted(ctx context.Context, id string) (bool, error)

	// Events
	// AppendEvents inserts events idempotently by (run_id, seq); duplicates are
	// ignored. Returns the number of newly-inserted rows. The caller owns the
	// global seq. Retained for internal callers/tests that assign seq directly;
	// production ingest and emission use the server-allocating methods below.
	AppendEvents(ctx context.Context, runID string, events []EventInput) (int, error)
	// AppendRunnerEvents ingests a batch of runner events. Each event's Seq is
	// treated as a per-source idempotency key (SourceRunner): events already
	// present under (run_id, SourceRunner, client_seq) are skipped. New events
	// are assigned the next server-allocated global seq (monotonic per run) so
	// they can never collide with internally-emitted events. Returns the
	// newly-inserted events (with their allocated seq/ts) in insertion order.
	AppendRunnerEvents(ctx context.Context, runID string, events []EventInput) ([]domain.RunEvent, error)
	// AppendInternalEvent appends one orchestrator-emitted event (run.status,
	// run.artifact, run.failure), allocating the next global seq transactionally.
	// It replaces the racy NextEventSeq+AppendEvents pattern. Returns the stored
	// event so the caller can publish it to the live hub.
	AppendInternalEvent(ctx context.Context, runID, typ string, payload map[string]any) (domain.RunEvent, error)
	// NextEventSeq returns the next unused seq for a run (max(seq)+1, or 1).
	// Used by tests; production emitters use AppendInternalEvent which allocates
	// atomically.
	NextEventSeq(ctx context.Context, runID string) (int64, error)
	ListEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error)

	// Artifacts
	PutArtifact(ctx context.Context, a *domain.RunArtifact) error
	GetArtifact(ctx context.Context, runID string, kind domain.ArtifactKind) (*domain.RunArtifact, error)
	// PutRunBundle / GetRunBundle store and read a run's git bundle (kind=bundle)
	// as raw bytes (bytea) — the diff stays a text artifact, but a bundle is
	// binary. The orchestrator fetches the bundle to push the branch (M3).
	PutRunBundle(ctx context.Context, runID string, data []byte) error
	GetRunBundle(ctx context.Context, runID string) ([]byte, error)

	// --- Model catalog + project grants (D21) --------------------------------
	// The single-row cluster_model_config is superseded by a catalog of models a
	// cluster admin registers and grants to individual projects. api_key_enc stays
	// encrypted on every read — the plaintext is NEVER returned over the API.

	// CreateModel inserts a catalog model. A duplicate name returns
	// ErrAlreadyExists (mapped to 409). The caller pre-fills id/created_at.
	CreateModel(ctx context.Context, m *domain.Model) error
	// GetModel returns a catalog model by id (ErrNotFound if absent).
	GetModel(ctx context.Context, id string) (*domain.Model, error)
	// ListModels returns the whole catalog, newest first (cluster-admin view).
	ListModels(ctx context.Context) ([]domain.Model, error)
	// CountModels returns the number of catalog models. Used by the resolution
	// chain to decide whether the MODEL_* env fallback applies (only when the
	// catalog is EMPTY — local rig compatibility).
	CountModels(ctx context.Context) (int, error)
	// UpdateModel updates a catalog model's mutable fields (name/base_url/
	// model_name/api_key_enc/updated_by). A duplicate name returns ErrAlreadyExists;
	// a missing row returns ErrNotFound.
	UpdateModel(ctx context.Context, m *domain.Model) error
	// DeleteModel removes a catalog model. Its grants cascade; services.default_
	// model_id and runs.model_id referencing it are set NULL. ErrNotFound if absent.
	DeleteModel(ctx context.Context, id string) error
	// ListModelsForProject returns the models GRANTED to a project (the member-
	// visible set + the resolution chain's authorization set), newest first.
	ListModelsForProject(ctx context.Context, projectID string) ([]domain.Model, error)
	// ListProjectIDsForModel returns the project ids a model is granted to (admin
	// grant-management view).
	ListProjectIDsForModel(ctx context.Context, modelID string) ([]string, error)
	// GrantModel authorizes a project to use a model. Idempotent (a repeat grant is
	// a no-op). ErrNotFound if the model or project does not exist.
	GrantModel(ctx context.Context, modelID, projectID string) error
	// RevokeModel removes a project's grant. A missing grant is a no-op (not an
	// error) so revoke is idempotent.
	RevokeModel(ctx context.Context, modelID, projectID string) error

	// --- Integrations (D19 / F5) ---------------------------------------------
	// A project-level git host binding with a BOT service credential. token_enc
	// stays encrypted on every read — the plaintext is NEVER returned over the API
	// and is decrypted only in the credentials resolver.

	// CreateIntegration inserts an integration. A duplicate (project_id, name)
	// returns ErrAlreadyExists (mapped to 409). The caller pre-fills id/created_at.
	CreateIntegration(ctx context.Context, in *domain.Integration) error
	// GetIntegration returns an integration by id (ErrNotFound if absent).
	GetIntegration(ctx context.Context, id string) (*domain.Integration, error)
	// ListIntegrationsByProject returns a project's integrations, newest first.
	ListIntegrationsByProject(ctx context.Context, projectID string) ([]domain.Integration, error)
	// UpdateIntegration rotates the mutable fields (name / token_enc / bot_username,
	// stamping updated_at). Host/provider/cred_type are immutable (delete + recreate
	// to change a host). A duplicate name returns ErrAlreadyExists; a missing row
	// returns ErrNotFound.
	UpdateIntegration(ctx context.Context, in *domain.Integration) error
	// DeleteIntegration removes an integration. services.integration_id referencing
	// it is set NULL (those services fall back to the legacy credential path).
	// ErrNotFound if absent.
	DeleteIntegration(ctx context.Context, id string) error
	// CountServicesUsingIntegration returns how many services are bound to an
	// integration (owner-facing "N services will fall back" confirmation on delete).
	CountServicesUsingIntegration(ctx context.Context, integrationID string) (int, error)

	// --- Auth: users & identities (M2) ---------------------------------------
	// CreateUserWithIdentity creates a new user together with its first identity
	// in one transaction. It decides is_cluster_admin atomically: the user becomes
	// cluster-admin iff it is the FIRST user in the system, determined under a
	// lock so two concurrent first logins cannot both become admin. Returns
	// firstUser=true when it minted the cluster admin. Callers pre-fill both ids.
	CreateUserWithIdentity(ctx context.Context, u *domain.User, id *domain.UserIdentity) (firstUser bool, err error)
	// GetUser returns a user by id (ErrNotFound if absent).
	GetUser(ctx context.Context, id string) (*domain.User, error)
	// GetIdentity looks up an identity by its provider + provider_uid (the login
	// key). ErrNotFound if no such identity.
	GetIdentity(ctx context.Context, provider domain.GitProvider, providerUID string) (*domain.UserIdentity, error)
	// GetIdentityForUser returns a user's identity on a specific provider (the
	// token the M3 draft-PR / review passes act with). ErrNotFound if the user has
	// not linked that provider.
	GetIdentityForUser(ctx context.Context, userID string, provider domain.GitProvider) (*domain.UserIdentity, error)
	// ListIdentities returns a user's linked identities.
	ListIdentities(ctx context.Context, userID string) ([]domain.UserIdentity, error)
	// UpdateIdentityToken re-encrypts an identity's stored tokens after a fresh
	// login/refresh. refreshEnc may be nil; expiresAt may be nil.
	UpdateIdentityToken(ctx context.Context, identityID string, accessEnc, refreshEnc []byte, expiresAt *time.Time) error
	// AttachIdentity links id to userID (the /auth/link flow). If the
	// (provider, provider_uid) already belongs to userID it refreshes the tokens;
	// if it belongs to another user it returns ErrIdentityTaken.
	AttachIdentity(ctx context.Context, userID string, id *domain.UserIdentity) error
	// CountUsers returns the number of users (admin snapshot / tests).
	CountUsers(ctx context.Context) (int, error)
	// SearchUsers returns up to limit users matching q (case-insensitive) on
	// display_name or any linked identity username. Empty q returns the first
	// limit users. Used by the add-member picker.
	SearchUsers(ctx context.Context, q string, limit int) ([]domain.User, error)
	// GetUserByProviderUsername resolves a user from a (provider, username) pair,
	// backing the add-member "{provider,username,role}" form. ErrNotFound if no
	// identity matches.
	GetUserByProviderUsername(ctx context.Context, provider domain.GitProvider, username string) (*domain.User, error)

	// --- Auth: sessions ------------------------------------------------------
	// CreateSession stores a new session (only its token_hash is persisted).
	CreateSession(ctx context.Context, s *domain.Session) error
	// GetUserBySessionToken returns the user for a currently-valid session
	// (revoked_at IS NULL AND expires_at > now()) identified by token hash.
	// ErrNotFound when there is no valid session for the hash.
	GetUserBySessionToken(ctx context.Context, tokenHash string) (*domain.User, error)
	// RevokeSession stamps revoked_at on the session with the given token hash.
	// A missing/already-revoked session is a no-op (not an error).
	RevokeSession(ctx context.Context, tokenHash string) error

	// --- Auth: project members & ownership -----------------------------------
	// ListMembers returns a project's members (any order; the API sorts/enriches).
	ListMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error)
	// GetMember returns one membership (ErrNotFound if the user is not a member).
	GetMember(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error)
	// UpsertMember inserts or updates a membership role.
	UpsertMember(ctx context.Context, m *domain.ProjectMember) error
	// RemoveMember deletes a membership. ErrNotFound if it did not exist.
	RemoveMember(ctx context.Context, projectID, userID string) error
	// CountProjectOwners counts members with role='owner' (last-owner guard).
	CountProjectOwners(ctx context.Context, projectID string) (int, error)
	// ListProjectsForUser returns the projects the user is a member of, newest
	// first (the non-admin project list).
	ListProjectsForUser(ctx context.Context, userID string) ([]domain.Project, error)

	// --- Kanban integration (Feature E) --------------------------------------
	// KanbanLink CRUD (admin-managed bindings of a jtype board column to a
	// project/service). CreateKanbanLink enforces UNIQUE(workspace_id, board_ref).
	CreateKanbanLink(ctx context.Context, l *domain.KanbanLink) error
	GetKanbanLink(ctx context.Context, id string) (*domain.KanbanLink, error)
	ListKanbanLinks(ctx context.Context) ([]domain.KanbanLink, error)
	// ListKanbanLinksByProject returns a project's links (F6 / D25 — the owner's
	// project-scoped management view), newest first.
	ListKanbanLinksByProject(ctx context.Context, projectID string) ([]domain.KanbanLink, error)
	// ListEnabledKanbanLinks returns only enabled links (the poller's scan set).
	ListEnabledKanbanLinks(ctx context.Context) ([]domain.KanbanLink, error)
	// SetKanbanLinkToken replaces ONLY a link's per-link encrypted jtype PAT
	// (P2 token rotation): nil clears it (back to the cluster fallback). The
	// link's binding and its claims are untouched — a rotation never
	// re-dispatches already-claimed cards.
	SetKanbanLinkToken(ctx context.Context, id string, tokenEnc []byte) error
	DeleteKanbanLink(ctx context.Context, id string) error

	// EnsureKanbanClaim inserts a (link_id, document_id) claim row with run_id
	// NULL if none exists yet, and returns the committed row. The caller decides
	// what to do from Claim.RunID: empty => the card is dispatch-eligible this
	// tick; non-empty => already dispatched, skip. This is the idempotency +
	// "card seen" primitive (UNIQUE(link_id, document_id)).
	EnsureKanbanClaim(ctx context.Context, linkID, documentID, documentPath string) (*domain.KanbanClaim, error)
	// SetKanbanClaimRun stamps run_id on a claim whose run_id is still empty —
	// the dispatch commit. It is a no-op (not an error) if the claim already has
	// a run_id (a racing tick), so the poller need not pre-check.
	SetKanbanClaimRun(ctx context.Context, linkID, documentID, runID string) error
	// MarkKanbanNotConfiguredNotified stamps notified_not_configured_at where it
	// is still NULL, returning notified=true only for the tick that stamped it so
	// the poller posts the "LLM not configured" card comment at most once.
	MarkKanbanNotConfiguredNotified(ctx context.Context, linkID, documentID string, at time.Time) (bool, error)
	// ListKanbanRunsAwaitingWriteback returns claims whose run has reached a
	// terminal state and whose writeback_at is still NULL, each joined with its
	// run + link so the reconciler can post the result comment / move the card in
	// one pass without extra lookups. Ordered oldest-first.
	ListKanbanRunsAwaitingWriteback(ctx context.Context) ([]KanbanWriteback, error)
	// MarkKanbanWriteback stamps writeback_at, returning wrote=true only for the
	// tick that stamped it so two ticks never double-post. First-writer-wins.
	MarkKanbanWriteback(ctx context.Context, linkID, documentID string, at time.Time) (bool, error)

	// --- Schedules (F11 / D24) ------------------------------------------------
	// Schedule CRUD (owner-managed service-level cron triggers). CreateSchedule
	// inserts a fresh row; GetSchedule / ListSchedulesByService serve the API;
	// ListEnabledSchedules is the poller's scan set (enabled only).
	CreateSchedule(ctx context.Context, sc *domain.Schedule) error
	GetSchedule(ctx context.Context, id string) (*domain.Schedule, error)
	ListSchedulesByService(ctx context.Context, serviceID string) ([]domain.Schedule, error)
	ListEnabledSchedules(ctx context.Context) ([]domain.Schedule, error)
	// UpdateSchedule persists an owner's edit to cron_expr / prompt / enabled
	// (and bumps updated_at). resetWindow=true additionally resets last_fired_at
	// to the CURRENT time, atomically with the edit — the API sets it when the
	// cron expression CHANGED or the schedule was re-enabled (false→true), so the
	// next fire is computed from the edit instant and a boundary that predates
	// the edit is never backfilled (same "no backfill" philosophy as restart).
	// last_error stays poller-owned and untouched either way. sc's
	// LastFiredAt/UpdatedAt are refreshed from the committed row. ErrNotFound
	// when the row is gone.
	UpdateSchedule(ctx context.Context, sc *domain.Schedule, resetWindow bool) error
	DeleteSchedule(ctx context.Context, id string) error
	// AdvanceSchedule atomically CLAIMS a due window: it sets last_fired_at=newFired
	// and last_error=lastErr (and updated_at=now) ONLY when the row's current
	// last_fired_at IS NOT DISTINCT FROM prevFired. It returns won=true for the
	// single instance that matched (the loser gets won=false and must NOT dispatch)
	// — this conditional update is the poller's anti-double-dispatch guard. lastErr
	// is "" on a successful dispatch (clearing any prior error) or the block reason
	// when the window is abandoned by a gate.
	AdvanceSchedule(ctx context.Context, id string, prevFired *time.Time, newFired time.Time, lastErr string) (won bool, err error)
	// SetScheduleLastError records a fail-visible reason on a schedule without
	// touching last_fired_at — used only for the rare post-claim dispatch failure
	// (the window was already advanced). Best-effort; ErrNotFound when the row is
	// gone (a concurrent delete), which the poller ignores.
	SetScheduleLastError(ctx context.Context, id, lastErr string) error

	// --- API keys (F12 / D24) --------------------------------------------------
	// Project-scoped, revocable automation credentials. CreateAPIKey inserts a
	// fresh row; the store never sees plaintext, only the already-computed
	// KeyHash/Prefix (see auth.GenerateAPIKey / auth.HashToken).
	CreateAPIKey(ctx context.Context, k *domain.APIKey) error
	// GetAPIKey loads a key by id (the owner-management path: DELETE verifies
	// the key belongs to the project named in the URL before revoking it).
	// ErrNotFound when the row is gone.
	GetAPIKey(ctx context.Context, id string) (*domain.APIKey, error)
	// GetAPIKeyByHash resolves a presented Bearer token's SHA-256 to its owning
	// key for principal resolution (api/principal.go). It excludes revoked rows
	// — ErrNotFound covers BOTH an unknown hash and a revoked key, so revocation
	// is effective on the very next lookup with no cache to invalidate, and the
	// caller never needs to distinguish "wrong key" from "revoked key" (both are
	// simply "unauthenticated").
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*domain.APIKey, error)
	// ListAPIKeysByProject serves the owner's management view, newest first.
	// Revoked keys are included (so status/history is visible); the plaintext
	// and hash are never part of domain.APIKey's JSON encoding regardless.
	ListAPIKeysByProject(ctx context.Context, projectID string) ([]domain.APIKey, error)
	// UpdateAPIKeyLastUsed best-effort stamps last_used_at. Called off the
	// principal-resolution hot path, throttled by the caller (see
	// api/principal.go touchAPIKeyLastUsed) so a hammered key does not write on
	// every single request. ErrNotFound (a racing revoke/delete) is not worth
	// surfacing — callers treat it as best-effort and ignore the error.
	UpdateAPIKeyLastUsed(ctx context.Context, id string, at time.Time) error
	// RevokeAPIKey sets revoked_at=now() where still NULL. Idempotent: revoking
	// an already-revoked (or, for the memory store, already-absent) key is a
	// no-op, not an error — so DELETE .../apikeys/{id} is safely retryable and
	// effective immediately (the next GetAPIKeyByHash for that key 404s).
	RevokeAPIKey(ctx context.Context, id string) error

	// Lifecycle
	Close()
}

// KanbanWriteback is a claim joined with its terminal run + link, the unit the
// reconciler's writeback pass consumes (Feature E). Populated by
// ListKanbanRunsAwaitingWriteback.
type KanbanWriteback struct {
	Claim domain.KanbanClaim
	Run   domain.Run
	Link  domain.KanbanLink
}

// ArchiveCandidate is a service eligible for cold workspace archival (F10 /
// D23 ③): its ID/ProjectID (for the archive Job's PVC + labels) plus the
// timestamp of its most-recent run, from which idleness was judged. Populated by
// ListArchiveCandidates.
type ArchiveCandidate struct {
	ServiceID    string
	ProjectID    string
	LastActivity time.Time
}

// ErrInvalidTransition is returned by the run mutators when a status change is
// not permitted by the domain state machine.
var ErrInvalidTransition = errors.New("invalid run status transition")
