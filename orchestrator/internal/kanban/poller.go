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
// One cluster-wide jtype PAT (env JTYPE_TOKEN) authorises every read/write.
package kanban

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
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

// ModelResolver resolves the effective LLM config so the poller can fail-visible
// (skip + comment) instead of queueing a run that could never execute.
type ModelResolver interface {
	Resolve(ctx context.Context) (modelcfg.Resolved, error)
}

// Poller scans enabled kanban_links for cards in their trigger column and
// dispatches agent runs. It owns an in-memory updatedClock cursor per link
// (a pure optimization — claims dedup is the real idempotency, so a restart
// re-scan from clock 0 is correct, just redundant).
type Poller struct {
	st         store.Store
	api        DocumentAPI
	models     ModelResolver
	log        *slog.Logger
	consoleURL string // for the "LLM not configured" card comment (where to fix it)
	interval   time.Duration
	now        func() time.Time

	mu      sync.Mutex
	cursors map[string]int64 // linkID -> last seen updatedClock
}

// New builds a Poller. interval<=0 still allows a manually-driven Tick
// (Run() itself would busy-loop, so main.go only starts Run when interval>0).
func New(st store.Store, api DocumentAPI, models ModelResolver, log *slog.Logger, consoleURL string, interval time.Duration) *Poller {
	return &Poller{
		st: st, api: api, models: models, log: log,
		consoleURL: consoleURL, interval: interval,
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
// manual trigger) can drive a single deterministic pass.
func (p *Poller) Tick(ctx context.Context) {
	links, err := p.st.ListEnabledKanbanLinks(ctx)
	if err != nil {
		p.log.Error("kanban poll: list links", "err", err)
		return
	}
	for i := range links {
		p.pollLink(ctx, &links[i])
	}
}

// pollLink scans one link's workspace for cards in the trigger column. Errors
// reaching jtype are logged and retried next tick (the poller never crashes the
// process — consistent with the reconciler's transient-error handling).
func (p *Poller) pollLink(ctx context.Context, link *domain.KanbanLink) {
	cursor := p.cursor(link.ID)

	docs, err := p.api.ListDocuments(ctx, link.WorkspaceID)
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
		p.maybeDispatch(ctx, link, d)
	}
	p.setCursor(link.ID, maxClock)
}

// maybeDispatch fetches one document, decides if it is a card in the trigger
// column, and dispatches a run (or posts the not-configured notice).
func (p *Poller) maybeDispatch(ctx context.Context, link *domain.KanbanLink, d jtype.Doc) {
	doc, err := p.api.GetDocument(ctx, link.WorkspaceID, d.ID)
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
	resolved, err := p.models.Resolve(ctx)
	if err != nil {
		p.log.Error("kanban poll: resolve model", "link", link.ID, "doc", d.ID, "err", err)
		return // transient; retry next tick
	}
	if !resolved.Configured() {
		p.notifyNotConfigured(ctx, link, d.ID)
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

// notifyNotConfigured posts the one-time "LLM not configured" notice on the card
// so the operator who dragged it sees an honest reason instead of silence.
func (p *Poller) notifyNotConfigured(ctx context.Context, link *domain.KanbanLink, docID string) {
	ok, err := p.st.MarkKanbanNotConfiguredNotified(ctx, link.ID, docID, p.now())
	if err != nil {
		p.log.Warn("kanban poll: mark notified", "link", link.ID, "doc", docID, "err", err)
		return
	}
	if !ok {
		return // already notified for this card — do not re-comment
	}
	body := "⏸️ jcode did not start: the cluster LLM is not configured. " +
		"A cluster admin must set it (Cluster page"
	if p.consoleURL != "" {
		body += ": " + p.consoleURL
	}
	body += ") and this card will dispatch automatically on the next poll."
	if err := p.api.AddComment(ctx, link.WorkspaceID, docID, body); err != nil {
		p.log.Warn("kanban poll: post not-configured comment", "link", link.ID, "doc", docID, "err", err)
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
