// Package kanban implements the jtype kanban → run trigger poller (Feature E).
//
// Architecture (docs/01): dragging a jtype card into a link's trigger column
// dispatches an agent run. jtype's outbound webhook is guarded by SSRF
// protection (https-only,拒绝内网) which the in-cluster orchestrator cannot
// satisfy, and the board SSE stream needs a `full`-scope token — so we POLL the
// jtype document API. The poll is level-based and idempotent (kanban_claims
// dedup), restart-safe (claims survive), and driven by updatedClock so a card
// move surfaces on the next tick — matching the existing reconciler philosophy.
//
// Each link authorises with its OWN jtype PAT (F6 / D25): the per-link encrypted
// token when set, else the cluster JTYPE_TOKEN env fallback; a link with neither
// is skipped fail-visibly (never a call with an empty credential).
package kanban

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/kanbancfg"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

// DocumentAPI is the slice of *jtype.Client the poller uses. Extracted as an
// interface so the poller is unit-tested with an in-memory fake (no HTTP).
type DocumentAPI interface {
	ListDocuments(ctx context.Context, workspace string) ([]jtype.Doc, error)
	GetDocument(ctx context.Context, workspace, id string) (*jtype.Document, error)
	AddComment(ctx context.Context, workspace, docID, body string) error
}

// ModelResolver runs the D21 resolution chain for a project/service so the poller
// can fail-visible (skip + comment) instead of queueing a run that could never
// execute. requested is always "" on this headless path (no composer pick).
type ModelResolver interface {
	SelectModel(ctx context.Context, projectID, defaultModelID, requested string) (modelcfg.Selection, modelcfg.SelectOutcome, error)
}

// Poller scans enabled kanban_links for cards in their trigger column and
// dispatches agent runs. It owns an in-memory updatedClock cursor per link
// (a pure optimization — claims dedup is the real idempotency, so a restart
// re-scan from clock 0 is correct, just redundant).
type Poller struct {
	st store.Store
	// resolver resolves the EFFECTIVE cluster jtype config (base URL + cluster
	// fallback token) once per tick (D27): the console-managed DB row or the
	// JTYPE_* env. An unconfigured cluster is a clean visible no-op, and a config
	// set in the console takes effect on the next tick — no restart, no silent
	// no-op (fail-visible red line).
	resolver *kanbancfg.Resolver
	// clientFor builds a DocumentAPI bound to a resolved PAT off the tick's jtype
	// Factory (F6 / D25). Production wraps *jtype.Factory.Client; tests inject a
	// fake (ignoring the factory).
	clientFor  func(f *jtype.Factory, token string) DocumentAPI
	decrypt    func([]byte) (string, error) // opens a link's encrypted PAT (nil => no cipher)
	models     ModelResolver
	log        *slog.Logger
	consoleURL string // for the "LLM not configured" card comment (where to fix it)
	interval   time.Duration
	now        func() time.Time

	mu      sync.Mutex
	cursors map[string]int64 // linkID -> last seen updatedClock
	// noted throttles the one-time notices (integration-off, per-link cluster-
	// fallback deprecation, missing-credential) so the scan does not log every tick.
	noted sync.Map // key -> struct{}
}

// New builds a Poller. resolver resolves the effective cluster jtype config per
// tick (D27); clientFor builds a jtype client from the resolved factory + PAT;
// decrypt opens a link's encrypted per-link token (nil when no cipher).
// interval<=0 still allows a manually-driven Tick (Run() itself would busy-loop,
// so main.go only starts Run when interval>0).
func New(st store.Store, resolver *kanbancfg.Resolver, clientFor func(f *jtype.Factory, token string) DocumentAPI, decrypt func([]byte) (string, error), models ModelResolver, log *slog.Logger, consoleURL string, interval time.Duration) *Poller {
	return &Poller{
		st: st, resolver: resolver, clientFor: clientFor, decrypt: decrypt,
		models: models, log: log, consoleURL: consoleURL, interval: interval,
		now:     func() time.Time { return time.Now().UTC() },
		cursors: map[string]int64{},
	}
}

// Run drives the loop until ctx is cancelled, ticking every interval. It is the
// poller analogue of reconciler.Reconciler.Run.
func (p *Poller) Run(ctx context.Context) {
	if p.interval <= 0 {
		p.log.Warn("kanban poller disabled: JTYPE_POLL_INTERVAL<=0")
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	p.log.Info("kanban poller started", "interval", p.interval)
	p.Tick(ctx) // one immediate pass so a waiting card doesn't wait a full tick
	for {
		select {
		case <-ctx.Done():
			p.log.Info("kanban poller stopping")
			return
		case <-ticker.C:
			p.Tick(ctx)
		}
	}
}

// Tick performs one scan over every enabled link. Exported so tests (and a
// manual trigger) can drive a single deterministic pass. The effective cluster
// jtype config is resolved ONCE per tick (D27): when the integration is not
// configured the whole tick is a clean visible no-op (logged once), so a config
// set in the console activates on the next tick without a restart.
func (p *Poller) Tick(ctx context.Context) {
	f, clusterToken, ok := p.resolver.Factory(ctx)
	if !ok {
		// Unconfigured (or a broken config, e.g. a DB fallback token with no cipher).
		// Log once so the idle state is visible without spamming every tick; reset
		// on the next configured tick below so a later disable logs again.
		if _, seen := p.noted.LoadOrStore("off", struct{}{}); !seen {
			p.log.Info("kanban poll: jtype integration not configured; poller idle (set it on the Cluster page)")
		}
		return
	}
	p.noted.Delete("off")

	links, err := p.st.ListEnabledKanbanLinks(ctx)
	if err != nil {
		p.log.Error("kanban poll: list links", "err", err)
		return
	}
	for i := range links {
		p.pollLink(ctx, f, clusterToken, &links[i])
	}
}

// pollLink scans one link's workspace for cards in the trigger column. Errors
// reaching jtype are logged and retried next tick (the poller never crashes the
// process — consistent with the reconciler's transient-error handling). f +
// clusterToken are the tick's resolved jtype Factory + effective cluster fallback
// token (source-coupled; D27).
func (p *Poller) pollLink(ctx context.Context, f *jtype.Factory, clusterToken string, link *domain.KanbanLink) {
	// Resolve this link's PAT (D25 three-state): per-link encrypted token, else the
	// cluster fallback, else fail-visibly skip (never a jtype call with an empty
	// credential). The cursor is untouched on skip so the link re-scans in full the
	// moment a token is configured. Notices are throttled to once per link.
	token, source, err := jtype.ResolveToken(link.TokenEnc, p.decrypt, clusterToken)
	if err != nil {
		if _, seen := p.noted.LoadOrStore("err:"+link.ID, struct{}{}); !seen {
			p.log.Error("kanban poll: no jtype credential for link; skipping",
				"link", link.ID, "workspace", link.WorkspaceID, "err", err)
		}
		return
	}
	if source == jtype.TokenClusterFallback {
		if _, seen := p.noted.LoadOrStore("dep:"+link.ID, struct{}{}); !seen {
			p.log.Warn("kanban poll: link uses the deprecated cluster JTYPE_TOKEN fallback; set a per-link token",
				"link", link.ID)
		}
	}
	api := p.clientFor(f, token)

	cursor := p.cursor(link.ID)
	docs, err := api.ListDocuments(ctx, link.WorkspaceID)
	if err != nil {
		p.log.Warn("kanban poll: list documents", "link", link.ID, "workspace", link.WorkspaceID, "err", err)
		return
	}
	maxClock := cursor
	for _, d := range docs {
		if d.UpdatedClock <= cursor {
			continue // seen (or pre-existing at startup); claims dedup still guards
		}
		if d.UpdatedClock > maxClock {
			maxClock = d.UpdatedClock
		}
		// Only `.md` documents can be cards; cheaply skip boards/assets.
		if !isMarkdown(d.Path) {
			continue
		}
		p.maybeDispatch(ctx, api, link, d)
	}
	p.setCursor(link.ID, maxClock)
}

// maybeDispatch fetches one document, decides if it is a card in the trigger
// column, and dispatches a run (or posts the not-configured notice). api is the
// link's token-bound jtype client (resolved in pollLink).
func (p *Poller) maybeDispatch(ctx context.Context, api DocumentAPI, link *domain.KanbanLink, d jtype.Doc) {
	doc, err := api.GetDocument(ctx, link.WorkspaceID, d.ID)
	if err != nil {
		p.log.Warn("kanban poll: get document", "link", link.ID, "doc", d.ID, "err", err)
		return
	}
	card := jtype.ParseCard(doc.Content)
	if card.Board != link.BoardRef || card.Status != link.TriggerColumn {
		return // not a card on this board, or not in the trigger column
	}
	prompt := buildPrompt(card)
	claim, err := p.st.EnsureKanbanClaim(ctx, link.ID, d.ID, d.Path)
	if err != nil {
		p.log.Error("kanban poll: ensure claim", "link", link.ID, "doc", d.ID, "err", err)
		return
	}
	if claim.RunID != "" {
		return // already dispatched for this card — idempotent
	}

	// Fail-visible gate: refuse to queue a run the runner could not execute.
	// Decision (commented): when the LLM is NOT configured we leave the claim's
	// run_id EMPTY and post ONE "LLM not configured" comment (notify-once via
	// MarkKanbanNotConfiguredNotified). Leaving run_id empty means the card is
	// retried on subsequent ticks — the moment an admin configures the model the
	// pending card auto-dispatches. This is both fail-visible AND recoverable AND
	// spam-free, which a permanent claim would not be.
	// Resolve which model this card's run uses (D21). The service default feeds the
	// chain; the poller never supplies a composer pick. NotSelected/NotConfigured
	// both mean "can't dispatch" here → notify-once + retry next tick.
	var defaultModelID string
	if svc, serr := p.st.GetService(ctx, link.ServiceID); serr == nil {
		defaultModelID = derefStr(svc.DefaultModelID)
	} else {
		p.log.Warn("kanban poll: load service for default model", "link", link.ID, "service", link.ServiceID, "err", serr)
	}
	sel, outcome, err := p.models.SelectModel(ctx, link.ProjectID, defaultModelID, "")
	if err != nil {
		p.log.Error("kanban poll: resolve model", "link", link.ID, "doc", d.ID, "err", err)
		return // transient; retry next tick
	}
	if outcome != modelcfg.SelectOK {
		// Both "can't dispatch" states are notify-once + retried on later ticks, but
		// the card comment DIFFERS (P5): NotSelected points the service owner at the
		// default-model setting, NotConfigured points at cluster-admin authorization.
		p.notifyBlocked(ctx, api, link, d.ID, outcome)
		return
	}

	run := &domain.Run{
		ID:        domain.NewID(),
		ProjectID: link.ProjectID,
		ServiceID: link.ServiceID,
		Prompt:    prompt,
		Status:    domain.StatusQueued,
		Kind:      domain.RunKindAgent,
		Phase:     "Queued",
		Origin:    domain.RunOriginKanban,
		Attempt:   1,
		CreatedAt: p.now(),
	}
	run.ModelName = sel.ModelName
	if sel.ModelID != "" {
		run.ModelID = &sel.ModelID
	}
	if err := p.st.CreateRun(ctx, run); err != nil {
		p.log.Error("kanban poll: create run", "link", link.ID, "doc", d.ID, "err", err)
		return // run_id stays empty; retried next tick (no claim leak — no run yet)
	}
	// Commit the dispatch: stamp the claim's run_id. A racing tick that already
	// stamped a run loses (SetKanbanClaimRun is conditional), and the orphan run
	// simply completes as a normal kanban run (its own claim's writeback marker
	// is what dedups writeback, not the run) — acceptable and rare.
	if err := p.st.SetKanbanClaimRun(ctx, link.ID, d.ID, run.ID); err != nil {
		p.log.Error("kanban poll: stamp claim run", "link", link.ID, "doc", d.ID, "run", run.ID, "err", err)
		return
	}
	p.log.Info("kanban poll: dispatched run", "link", link.ID, "doc", d.ID, "run", run.ID, "card", card.Title)
}

// derefStr returns the pointed-to string, or "" for a nil pointer.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// notifyBlocked posts the one-time "did not start" notice on the card so the
// operator who dragged it sees an honest reason instead of silence. The message
// differs by outcome (P5): NotSelected asks the SERVICE OWNER to set a default
// model (several are granted, a headless card can't pick); NotConfigured asks a
// CLUSTER ADMIN to grant a model to the project. Both are throttled to at most one
// comment per card and the card auto-dispatches once the fix lands.
func (p *Poller) notifyBlocked(ctx context.Context, api DocumentAPI, link *domain.KanbanLink, docID string, outcome modelcfg.SelectOutcome) {
	ok, err := p.st.MarkKanbanNotConfiguredNotified(ctx, link.ID, docID, p.now())
	if err != nil {
		p.log.Warn("kanban poll: mark notified", "link", link.ID, "doc", docID, "err", err)
		return
	}
	if !ok {
		return // already notified for this card — do not re-comment
	}
	var body string
	if outcome == modelcfg.SelectNotSelected {
		body = "⏸️ jcode did not start: several models are available for this project, so a card-triggered run can't pick one. " +
			"The service owner must set a default model on the service"
		if p.consoleURL != "" {
			body += " (" + p.consoleURL + ")"
		}
		body += ", after which this card dispatches automatically on the next poll."
	} else {
		body = "⏸️ jcode did not start: no model is configured for this project. " +
			"A cluster admin must grant one (Cluster page"
		if p.consoleURL != "" {
			body += ": " + p.consoleURL
		}
		body += ") and this card will dispatch automatically on the next poll."
	}
	if err := api.AddComment(ctx, link.WorkspaceID, docID, body); err != nil {
		p.log.Warn("kanban poll: post blocked comment", "link", link.ID, "doc", docID, "err", err)
	}
}

// buildPrompt is the card→run prompt: the title, a blank line, then the card's
// markdown body (the task description). Trimmed so an empty body is just the title.
func buildPrompt(card jtype.Card) string {
	title := card.Title
	body := strings.TrimSpace(card.Body)
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

// isMarkdown reports whether path ends with .md (cards are .md documents).
func isMarkdown(path string) bool {
	return len(path) > 3 && path[len(path)-3:] == ".md"
}

func (p *Poller) cursor(linkID string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursors[linkID]
}

func (p *Poller) setCursor(linkID string, clock int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if clock > p.cursors[linkID] {
		p.cursors[linkID] = clock
	}
}

// _ asserts *jtype.Client satisfies DocumentAPI at compile time.
var _ DocumentAPI = (*jtype.Client)(nil)
