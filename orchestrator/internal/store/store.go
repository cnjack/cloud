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
	// MarkPRCreated stamps pr_url/pr_number once the orchestrator has opened (or
	// found) the draft PR (ST-1). It is IDEMPOTENT and first-writer-wins: it
	// writes only where pr_url is currently empty, so a retry that raced another
	// tick cannot double-open or clobber an already-recorded PR. It does not
	// change status and is a no-op on a missing run. Returns the committed row.
	MarkPRCreated(ctx context.Context, id, prURL string, prNumber int) (*domain.Run, error)

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
	// ListEnabledKanbanLinks returns only enabled links (the poller's scan set).
	ListEnabledKanbanLinks(ctx context.Context) ([]domain.KanbanLink, error)
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

// ErrInvalidTransition is returned by the run mutators when a status change is
// not permitted by the domain state machine.
var ErrInvalidTransition = errors.New("invalid run status transition")
