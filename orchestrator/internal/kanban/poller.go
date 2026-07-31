// Package kanban implements the jtype kanban → run trigger poller (Feature E).
//
// Architecture (docs/01): dragging a jtype card into a link's trigger column
// dispatches an agent run. jtype's outbound webhook is guarded by SSRF
// protection (https-only,拒绝内网) which the in-cluster orchestrator cannot
// satisfy, and the board SSE stream needs a `full`-scope token — so we pull
// jtype's durable board event sequence. Each link persists its last successfully
// handled sequence; kanban_claims provide replay-safe dispatch idempotency. A
// one-time level scan bootstraps links whose cursor predates the event feed.
//
// Each link authorises with its OWN jtype PAT (F6 / D25; D36 removed the
// cluster fallback token): a link without a per-link token is skipped
// fail-visibly (never a call with an empty credential).
package kanban

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
	PullBoardEvents(ctx context.Context, workspace, boardRef string, afterSequence int64, limit int) (*jtype.KanbanEventPage, error)
	GetDocument(ctx context.Context, workspace, id string) (*jtype.Document, error)
	ListComments(ctx context.Context, workspace, docID string) ([]jtype.Comment, error)
	AddComment(ctx context.Context, workspace, docID, body string) error
	// GetBoard resolves a board by name/ref and returns its config id + columns.
	// Used by the runtime fail-visible re-validation of an unvalidated/invalid link
	// (D30); the normal card scan does NOT call it (it matches by frontmatter).
	GetBoard(ctx context.Context, workspace, boardRef string) (*jtype.Board, error)
}

type boardConfigInspector interface {
	GetBoardByConfigID(ctx context.Context, workspace, configID string) (*jtype.Board, error)
}

// ModelResolver runs the D21 resolution chain for a project/service so the poller
// can fail-visible (skip + comment) instead of queueing a run that could never
// execute. requested is always "" on this headless path (no composer pick).
type ModelResolver interface {
	SelectModel(ctx context.Context, projectID, defaultModelID, requested string) (modelcfg.Selection, modelcfg.SelectOutcome, error)
}

// Poller pulls durable jtype events for enabled kanban_links and dispatches agent
// runs. The per-link event cursor is stored in kanban_links, so process restarts
// resume without a lossy full-list watermark.
type Poller struct {
	st store.Store
	// resolver resolves the EFFECTIVE cluster jtype config (the base URL, D36)
	// once per tick (D27): the console-managed DB row or the JTYPE_BASE_URL env.
	// An unconfigured cluster is a clean visible no-op, and a config set in the
	// console takes effect on the next tick — no restart, no silent no-op
	// (fail-visible red line).
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

	// noted throttles the one-time notices (integration-off, per-link missing-
	// credential) so the scan does not log every tick.
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
		now: func() time.Time { return time.Now().UTC() },
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
	p.tickPluginAutomations(ctx)

	f, ok := p.resolver.Factory(ctx)
	if !ok {
		// Unconfigured (or a broken config). Log once so the idle state is visible
		// without spamming every tick; reset on the next configured tick below so a
		// later disable logs again.
		if _, seen := p.noted.LoadOrStore("off", struct{}{}); !seen {
			p.log.Info("kanban poll: jtype integration not configured; poller idle (set the base URL on the Cluster page)")
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
		p.pollLink(ctx, f, &links[i])
	}
}

// tickPluginAutomations is the clean-cut Project Plugin path. It deliberately
// does not depend on the retired cluster_kanban_config/kanban_links tables:
// instance URL and token come from provider_configs + plugin_installations.
func (p *Poller) tickPluginAutomations(ctx context.Context) {
	specs, err := p.st.ListEnabledKanbanAutomations(ctx)
	if err != nil {
		p.log.Error("Kanban Automation poll: list", "err", err)
		return
	}
	if len(specs) == 0 {
		return
	}
	cfg, err := p.st.GetProviderConfig(ctx, domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" {
		p.log.Warn("Kanban Automation poll: JType Provider is unavailable")
		return
	}
	factory := jtype.NewFactory(cfg.BaseURL, 20*time.Second)
	if factory == nil {
		return
	}
	for i := range specs {
		p.pollPluginAutomation(ctx, factory, &specs[i], cfg.ConfigRevision)
	}
}

func (p *Poller) pollPluginAutomation(ctx context.Context, factory *jtype.Factory, spec *domain.PluginAutomationSpec, configRevision int64) {
	if spec == nil || spec.Kanban == nil {
		return
	}
	installation, err := p.st.GetPluginInstallation(ctx, spec.Kanban.InstallationID)
	if err != nil || installation.Provider != domain.PluginJType ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" ||
		installation.WorkspaceID == "" || installation.ConfigRevision != configRevision {
		return
	}
	token, _, err := jtype.ResolveToken(installation.AccessTokenEnc, p.decrypt)
	if err != nil {
		return
	}
	api := p.clientFor(factory, token)
	// Existing occurrences and their receipts use frozen routing and remain
	// retryable even while the live board configuration is drifting. Board
	// validation fences only bootstrap/event consumption, i.e. new triggers.
	p.retryPluginKanbanOccurrences(ctx, api, spec)
	p.retryPluginKanbanReceipts(ctx, api, spec)
	if !p.validatePluginKanbanBoard(ctx, api, spec, installation.WorkspaceID) {
		return
	}
	if spec.Kanban.BootstrappedAt == nil {
		p.bootstrapPluginAutomation(ctx, api, spec, installation)
		return
	}
	p.consumePluginAutomationEvents(ctx, api, spec, installation)
}

func (p *Poller) validatePluginKanbanBoard(
	ctx context.Context,
	api DocumentAPI,
	spec *domain.PluginAutomationSpec,
	workspaceID string,
) bool {
	inspector, ok := api.(boardConfigInspector)
	if !ok {
		// Focused test doubles can implement only DocumentAPI. Production's
		// *jtype.Client always implements the config-id lookup.
		return true
	}
	board, err := inspector.GetBoardByConfigID(ctx, workspaceID, spec.Kanban.BoardRef)
	if err != nil {
		code := "board_validation_unavailable"
		message := "JType could not validate the configured board."
		if errors.Is(err, jtype.ErrDocNotFound) {
			code = "board_drift"
			message = "The configured JType board no longer exists."
		}
		spec.Automation.LastError = code + ": " + message
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		return false
	}
	if !boardHasColumn(board, spec.Kanban.TriggerColumn) ||
		(spec.Kanban.DoneColumn != "" && !boardHasColumn(board, spec.Kanban.DoneColumn)) {
		spec.Automation.LastError = "board_drift: A configured Kanban column no longer exists."
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		return false
	}
	if strings.HasPrefix(spec.Automation.LastError, "board_drift:") ||
		strings.HasPrefix(spec.Automation.LastError, "board_validation_unavailable:") {
		spec.Automation.LastError = ""
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
	}
	return true
}

func (p *Poller) bootstrapPluginAutomation(ctx context.Context, api DocumentAPI, spec *domain.PluginAutomationSpec, installation *domain.PluginInstallation) {
	head, err := pluginAutomationEventHead(ctx, api, installation.WorkspaceID, spec.Kanban.BoardRef, spec.Kanban.EventCursor)
	if err != nil {
		spec.Automation.LastError = "event_feed_unavailable: JType board events could not be read."
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		return
	}
	docs, err := api.ListDocuments(ctx, installation.WorkspaceID)
	if err != nil {
		spec.Automation.LastError = "bootstrap_unavailable: JType cards could not be listed."
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		return
	}
	for _, doc := range docs {
		if !isMarkdown(doc.Path) {
			continue
		}
		full, err := api.GetDocument(ctx, installation.WorkspaceID, doc.ID)
		if err != nil {
			spec.Automation.LastError = "bootstrap_unavailable: A JType Card could not be read."
			_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
			return
		}
		card := jtype.ParseCard(full.Content)
		if card.Board != spec.Kanban.BoardRef || strings.TrimSpace(card.Status) == "" {
			continue
		}
		result, err := p.st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
			AutomationID: spec.Automation.ID, ServiceID: spec.Automation.ServiceID,
			InstallationID: installation.ID, WorkspaceID: installation.WorkspaceID,
			DocumentID: doc.ID, DocumentPath: doc.Path, TriggerColumn: spec.Kanban.TriggerColumn,
			DoneColumn: spec.Kanban.DoneColumn, ObservedColumn: card.Status,
			EventKey: "bootstrap:" + spec.Automation.ID + ":" + doc.ID, ObservedAt: p.now(),
		})
		if err != nil {
			return
		}
		if result.Created && result.Occurrence != nil {
			p.dispatchPluginKanbanOccurrence(ctx, api, spec, result.Occurrence, &card)
		} else {
			p.projectPluginKanbanSuppressionReceipt(ctx, api, result)
		}
	}
	now := p.now()
	if advanced, err := p.st.AdvancePluginKanbanTrigger(ctx, spec.Automation.ID, spec.Kanban.EventCursor, head, &now); err != nil || !advanced {
		return
	}
	spec.Kanban.EventCursor = head
	spec.Kanban.BootstrappedAt = &now
	p.clearPluginKanbanPollingError(ctx, spec)
}

func pluginAutomationEventHead(ctx context.Context, api DocumentAPI, workspaceID, boardRef string, after int64) (int64, error) {
	head := after
	for {
		page, err := api.PullBoardEvents(ctx, workspaceID, boardRef, head, 100)
		if err != nil {
			return after, err
		}
		if page.NextSequence < head {
			return after, fmt.Errorf("JType event cursor moved backwards from %d to %d", head, page.NextSequence)
		}
		head = page.NextSequence
		if !page.HasMore {
			return head, nil
		}
		if len(page.Events) == 0 || page.NextSequence == after {
			return after, errors.New("JType event feed did not advance")
		}
		after = head
	}
}

func (p *Poller) consumePluginAutomationEvents(ctx context.Context, api DocumentAPI, spec *domain.PluginAutomationSpec, installation *domain.PluginInstallation) {
	cursor := spec.Kanban.EventCursor
	for {
		page, err := api.PullBoardEvents(ctx, installation.WorkspaceID, spec.Kanban.BoardRef, cursor, 100)
		if err != nil {
			spec.Automation.LastError = "event_feed_unavailable: JType board events could not be read."
			_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
			return
		}
		if len(page.Events) == 0 {
			p.clearPluginKanbanPollingError(ctx, spec)
			return
		}
		docs, err := api.ListDocuments(ctx, installation.WorkspaceID)
		if err != nil {
			spec.Automation.LastError = "card_index_unavailable: JType Card index could not be read."
			_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
			return
		}
		docsByPath := make(map[string]jtype.Doc, len(docs))
		for _, doc := range docs {
			docsByPath[doc.Path] = doc
		}
		for i := range page.Events {
			event := page.Events[i]
			if event.Sequence <= cursor {
				continue
			}
			doc, ok := docsByPath[event.Card.Path]
			if !ok {
				if _, markErr := p.st.MarkPluginKanbanCardUnavailable(
					ctx, spec.Automation.ID, installation.WorkspaceID, event.Card.Path, p.now(),
				); markErr != nil {
					return
				}
			} else if isMarkdown(doc.Path) {
				sequence := event.Sequence
				result, observeErr := p.st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
					AutomationID: spec.Automation.ID, ServiceID: spec.Automation.ServiceID,
					InstallationID: installation.ID, WorkspaceID: installation.WorkspaceID,
					DocumentID: doc.ID, DocumentPath: doc.Path, TriggerColumn: spec.Kanban.TriggerColumn,
					DoneColumn: spec.Kanban.DoneColumn, ObservedColumn: event.Card.Status,
					EventKey: "event:" + strconv.FormatInt(event.Sequence, 10), EventSequence: &sequence,
					ActorDisplay: event.EditedBy, ObservedAt: p.now(),
				})
				if observeErr != nil {
					return
				}
				if result.Created && result.Occurrence != nil {
					p.dispatchPluginKanbanOccurrence(ctx, api, spec, result.Occurrence, nil)
				} else {
					p.projectPluginKanbanSuppressionReceipt(ctx, api, result)
				}
			}
			advanced, advanceErr := p.st.AdvancePluginKanbanTrigger(ctx, spec.Automation.ID, cursor, event.Sequence, nil)
			if advanceErr != nil || !advanced {
				return
			}
			cursor = event.Sequence
		}
		if !page.HasMore {
			p.clearPluginKanbanPollingError(ctx, spec)
			return
		}
	}
}

func (p *Poller) clearPluginKanbanPollingError(ctx context.Context, spec *domain.PluginAutomationSpec) {
	if spec == nil {
		return
	}
	for _, prefix := range []string{
		"event_feed_unavailable:",
		"bootstrap_unavailable:",
		"card_index_unavailable:",
	} {
		if strings.HasPrefix(spec.Automation.LastError, prefix) {
			spec.Automation.LastError = ""
			_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
			return
		}
	}
}

func (p *Poller) dispatchPluginKanbanOccurrence(ctx context.Context, api DocumentAPI, spec *domain.PluginAutomationSpec, occurrence *domain.PluginKanbanOccurrence, parsedCard *jtype.Card) {
	if occurrence == nil || occurrence.RunID != "" {
		return
	}
	svc, err := p.st.GetService(ctx, spec.Automation.ServiceID)
	if err != nil {
		return
	}
	if reasonCode, reasonMessage, repairRole := p.pluginKanbanRepositoryBlocker(ctx, svc); reasonCode != "" {
		p.blockPluginKanbanOccurrence(
			ctx, api, spec, occurrence, svc, reasonCode, reasonMessage, repairRole,
		)
		return
	}
	card := parsedCard
	if card == nil {
		full, err := api.GetDocument(ctx, occurrence.WorkspaceID, occurrence.DocumentID)
		if err != nil {
			reasonCode := "card_read_unavailable"
			reasonMessage := "JType could not read the source Card. Cloud will retry this occurrence."
			repairRole := "project_owner"
			var jtypeErr *jtype.Error
			if errors.As(err, &jtypeErr) && jtypeErr.StatusCode == http.StatusNotFound {
				reasonCode = "card_unavailable"
				reasonMessage = "The source Card is no longer available in JType."
				_, _ = p.st.MarkPluginKanbanCardUnavailable(
					ctx, occurrence.AutomationID, occurrence.WorkspaceID,
					occurrence.DocumentPath, p.now(),
				)
			}
			p.blockPluginKanbanOccurrence(
				ctx, api, spec, occurrence, svc, reasonCode, reasonMessage, repairRole,
			)
			return
		}
		value := jtype.ParseCard(full.Content)
		card = &value
	}
	sel, outcome, err := p.models.SelectModel(ctx, svc.ProjectID, derefStr(svc.DefaultModelID), spec.Automation.ModelID)
	if err != nil || outcome != modelcfg.SelectOK {
		spec.Automation.LastError = "Automation model is unavailable."
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		p.blockPluginKanbanOccurrence(
			ctx, api, spec, occurrence, svc, "model_not_configured",
			"Choose an allowed model for this Service.", "project_owner",
		)
		return
	}
	if !sel.SupportsEffort(spec.Automation.ModelEffort) {
		spec.Automation.LastError = "The selected Automation model no longer supports reasoning effort."
		_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
		p.blockPluginKanbanOccurrence(
			ctx, api, spec, occurrence, svc, "model_effort_unsupported",
			"Choose a supported reasoning effort for this Automation.", "project_owner",
		)
		return
	}
	now := p.now()
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: svc.ProjectID, ServiceID: svc.ID,
		Prompt: buildPrompt(*card), Status: domain.StatusQueued, Kind: domain.RunKindAgent,
		Phase: "Queued", Origin: domain.RunOriginKanban,
		OriginAutomationID: spec.Automation.ID, OriginEventKey: occurrence.ID,
		Attempt: 1, CreatedAt: now, ModelName: sel.ModelName,
		ModelEffort: spec.Automation.ModelEffort,
	}
	if sel.ModelID != "" {
		modelID := sel.ModelID
		run.ModelID = &modelID
	}
	attached, err := p.st.CreatePluginKanbanOccurrenceRun(ctx, occurrence.ID, run)
	if err != nil || !attached {
		return
	}
	spec.Automation.LastTriggeredAt = &now
	spec.Automation.LastRunID = run.ID
	spec.Automation.LastError = ""
	_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
	occurrence.RunID = run.ID
	occurrence.State = domain.KanbanOccurrenceQueued
	occurrence.ReceiptPhase = "accepted"
	occurrence.ReceiptWrittenAt = nil
	p.projectPluginKanbanReceipt(ctx, api, occurrence, run, svc)
}

func (p *Poller) blockPluginKanbanOccurrence(
	ctx context.Context,
	api DocumentAPI,
	spec *domain.PluginAutomationSpec,
	occurrence *domain.PluginKanbanOccurrence,
	svc *domain.Service,
	reasonCode, reasonMessage, repairRole string,
) {
	spec.Automation.LastError = reasonCode + ": " + reasonMessage
	_ = p.st.UpdatePluginAutomation(ctx, &spec.Automation)
	blocked, err := p.st.SetPluginKanbanOccurrenceBlocked(
		ctx, occurrence.ID, reasonCode, reasonMessage, repairRole,
	)
	if err == nil {
		p.projectPluginKanbanReceipt(ctx, api, blocked, nil, svc)
	}
}

// pluginKanbanRepositoryBlocker checks only durable prerequisites that must be
// true before a Run is created. Temporary scheduler capacity is intentionally
// not a blocker: a valid Run may remain queued until capacity becomes free.
func (p *Poller) pluginKanbanRepositoryBlocker(ctx context.Context, svc *domain.Service) (string, string, string) {
	if svc == nil || svc.DeletingAt != nil {
		return "service_unavailable", "The target Service is being deleted.", "project_owner"
	}
	switch svc.RepoKind {
	case domain.RepoKindRaw:
		if strings.TrimSpace(svc.RawRepoURL) == "" {
			return "repository_not_configured", "Configure a repository for this Service.", "project_owner"
		}
		return "", "", ""
	case domain.RepoKindProvider:
		if !domain.ValidProvider(svc.Provider) || strings.TrimSpace(svc.RepoOwnerName) == "" {
			return "repository_not_configured", "Choose a valid provider repository for this Service.", "project_owner"
		}
	default:
		return "repository_not_configured", "Configure a repository for this Service.", "project_owner"
	}

	binding, err := p.st.GetServiceRepositoryBinding(ctx, svc.ID)
	if err != nil || binding.InstallationID == "" || strings.TrimSpace(binding.CloneURL) == "" {
		return "repository_unavailable", "Reconnect the Service repository Plugin.", "project_owner"
	}
	installation, err := p.st.GetPluginInstallation(ctx, binding.InstallationID)
	if err != nil || installation.Provider != domain.ProviderKind(svc.Provider) ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" {
		return "provider_unavailable", "Repair the repository Provider connection.", "project_owner"
	}
	if (installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
		(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
		return "provider_unavailable", "Reconnect the repository Provider credential.", "project_owner"
	}
	cfg, err := p.st.GetProviderConfig(ctx, installation.Provider)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" ||
		cfg.ConfigRevision != installation.ConfigRevision {
		return "provider_unavailable", "Ask a cluster administrator to repair the repository Provider.", "cluster_admin"
	}
	return "", "", ""
}

func (p *Poller) retryPluginKanbanOccurrences(ctx context.Context, api DocumentAPI, spec *domain.PluginAutomationSpec) {
	pending, err := p.st.ListPluginKanbanDispatchableOccurrences(ctx, spec.Automation.ID, 50)
	if err != nil {
		return
	}
	for i := range pending {
		p.dispatchPluginKanbanOccurrence(ctx, api, spec, &pending[i], nil)
	}
}

func (p *Poller) retryPluginKanbanReceipts(ctx context.Context, api DocumentAPI, spec *domain.PluginAutomationSpec) {
	pending, err := p.st.ListPluginKanbanReceiptPending(ctx, spec.Automation.ID, 50)
	if err != nil {
		return
	}
	svc, _ := p.st.GetService(ctx, spec.Automation.ServiceID)
	for i := range pending {
		occurrence := &pending[i]
		var run *domain.Run
		if occurrence.RunID != "" {
			run, _ = p.st.GetRun(ctx, occurrence.RunID)
		}
		p.projectPluginKanbanReceipt(ctx, api, occurrence, run, svc)
	}
}

func (p *Poller) projectPluginKanbanSuppressionReceipt(
	ctx context.Context,
	api DocumentAPI,
	result *store.PluginKanbanObservationResult,
) {
	if result == nil || result.Occurrence == nil ||
		(result.SuppressedReason != store.PluginKanbanAlreadyRunning &&
			result.SuppressedReason != store.PluginKanbanWritebackPending) {
		return
	}
	occurrence, err := p.st.SetPluginKanbanOccurrenceReceiptPhase(
		ctx, result.Occurrence.ID, result.SuppressedReason,
	)
	if err != nil {
		return
	}
	var run *domain.Run
	if occurrence.RunID != "" {
		run, _ = p.st.GetRun(ctx, occurrence.RunID)
	}
	p.projectPluginKanbanReceipt(ctx, api, occurrence, run, nil)
}

func (p *Poller) projectPluginKanbanReceipt(ctx context.Context, api DocumentAPI, occurrence *domain.PluginKanbanOccurrence, run *domain.Run, svc *domain.Service) {
	if occurrence == nil || occurrence.ReceiptPhase == "" || occurrence.ReceiptWrittenAt != nil {
		return
	}
	marker := jtype.KanbanReceiptMarker(occurrence.ID, occurrence.ReceiptPhase)
	dedupeMarker := marker
	if occurrence.ReceiptPhase == "blocked" && occurrence.ReasonCode != "" {
		dedupeMarker = jtype.KanbanReceiptMarker(
			occurrence.ID,
			"blocked-"+occurrence.ReasonCode+"-"+occurrence.RepairRole,
		)
	}
	comments, err := api.ListComments(ctx, occurrence.WorkspaceID, occurrence.DocumentID)
	if err != nil {
		_ = p.st.MarkPluginKanbanOccurrenceReceipt(ctx, occurrence.ID, occurrence.ReceiptPhase, nil, "JType comments could not be read.")
		return
	}
	for _, comment := range comments {
		if strings.Contains(comment.Body, dedupeMarker) {
			now := p.now()
			_ = p.st.MarkPluginKanbanOccurrenceReceipt(ctx, occurrence.ID, occurrence.ReceiptPhase, &now, "")
			return
		}
	}

	body := marker + "\n"
	if dedupeMarker != marker {
		body += dedupeMarker + "\n"
	}
	switch occurrence.ReceiptPhase {
	case "blocked":
		body += "jcode Cloud could not start this Card: " + occurrence.ReasonMessage
		if occurrence.RepairRole == "project_owner" {
			body += "\n\nNext: ask a Project owner to update the Automation or Service configuration."
		} else if occurrence.RepairRole == "cluster_admin" {
			body += "\n\nNext: ask a cluster administrator to repair the required Provider."
		}
	case "accepted":
		if run == nil || svc == nil {
			return
		}
		model := run.ModelName
		if model == "" {
			model = "the configured model"
		}
		body += "jcode Cloud accepted this Card for `" + svc.Name + "` using `" + model + "`."
		body += "\n\nRun: " + strings.TrimRight(p.consoleURL, "/") + "/runs/" + run.ID
	case store.PluginKanbanAlreadyRunning:
		body += "jcode Cloud is already working on this Card."
		if run != nil {
			body += "\n\nRun: " + strings.TrimRight(p.consoleURL, "/") + "/runs/" + run.ID
		}
	case store.PluginKanbanWritebackPending:
		body += "The Run has finished; jcode Cloud is still syncing its result to this Card."
		if run != nil {
			body += "\n\nRun: " + strings.TrimRight(p.consoleURL, "/") + "/runs/" + run.ID
		}
	default:
		return
	}
	if err := api.AddComment(ctx, occurrence.WorkspaceID, occurrence.DocumentID, body); err != nil {
		_ = p.st.MarkPluginKanbanOccurrenceReceipt(ctx, occurrence.ID, occurrence.ReceiptPhase, nil, "JType receipt comment could not be written.")
		return
	}
	now := p.now()
	_ = p.st.MarkPluginKanbanOccurrenceReceipt(ctx, occurrence.ID, occurrence.ReceiptPhase, &now, "")
}

// pollLink pulls one link's durable board-event sequence. Errors reaching jtype
// or handling an event leave the failed sequence uncommitted and retry it next
// tick. f is the tick's resolved jtype Factory (D27/D36).
func (p *Poller) pollLink(ctx context.Context, f *jtype.Factory, link *domain.KanbanLink) {
	// Resolve this link's PAT (D25/D36): the per-link encrypted token, or
	// fail-visibly skip (never a jtype call with an empty credential — the
	// cluster fallback token was removed in D36). The cursor is untouched on
	// skip so the link resumes exactly where it stopped when a token is
	// configured. Notices are throttled per link.
	token, _, err := jtype.ResolveToken(link.TokenEnc, p.decrypt)
	if err != nil {
		if _, seen := p.noted.LoadOrStore("err:"+link.ID, struct{}{}); !seen {
			p.log.Error("kanban poll: no jtype credential for link; skipping",
				"link", link.ID, "workspace", link.WorkspaceID, "err", err)
		}
		return
	}
	api := p.clientFor(f, token)

	// D30 runtime fail-visible check: a link not yet validated against a live board
	// (soft-created without a credential, or previously found invalid) gets one
	// GetBoard now that a token has resolved. Success canonicalizes board_ref to the
	// board's config id + flips board_status to "ok"; a definitive failure flips to
	// "invalid" and skips this tick's scan (an unresolved ref would match no card);
	// a transient/transport error leaves the status and retries next tick. Never a
	// silent no-op — a wrong soft-created link becomes loudly "invalid".
	if boardStatusOrOK(link.BoardStatus) != domain.KanbanBoardOK {
		if !p.revalidateBoard(ctx, api, link) {
			return
		}
	}

	// A NULL sequence marks a link created before durable board events (or a new
	// link whose board may already contain trigger cards). Run exactly one
	// compatibility level scan; only commit sequence 0 after every candidate was
	// handled. A blocked/transient card keeps NULL and retries on the next tick.
	if link.EventSequence == nil {
		if !p.bootstrapLink(ctx, api, link) {
			return
		}
		zero := int64(0)
		link.EventSequence = &zero
	}
	p.pullEvents(ctx, api, link)
}

const eventPageLimit = 100

// bootstrapLink performs the one-time compatibility level scan for a NULL event
// cursor. It preserves the product contract that a card already in the trigger
// column when a link becomes active is dispatched. Successful candidates are
// claim-deduped if a later candidate blocks and the scan has to replay.
func (p *Poller) bootstrapLink(ctx context.Context, api DocumentAPI, link *domain.KanbanLink) bool {
	docs, err := api.ListDocuments(ctx, link.WorkspaceID)
	if err != nil {
		p.log.Warn("kanban poll: bootstrap list documents", "link", link.ID, "workspace", link.WorkspaceID, "err", err)
		return false
	}
	for _, d := range docs {
		if !isMarkdown(d.Path) {
			continue
		}
		if !p.maybeDispatch(ctx, api, link, d, nil) {
			return false
		}
	}
	if err := p.st.AdvanceKanbanLinkEventSequence(ctx, link.ID, 0); err != nil {
		p.log.Error("kanban poll: commit bootstrap cursor", "link", link.ID, "err", err)
		return false
	}
	p.log.Info("kanban poll: compatibility bootstrap complete", "link", link.ID)
	return true
}

// pullEvents drains oldest-first event pages. It advances only the successfully
// handled prefix; a failure at sequence N leaves N and all later events for a
// replay. Claims make replayed successful dispatches cheap and safe.
func (p *Poller) pullEvents(ctx context.Context, api DocumentAPI, link *domain.KanbanLink) {
	cursor := int64(0)
	if link.EventSequence != nil {
		cursor = *link.EventSequence
	}
	for {
		page, err := api.PullBoardEvents(ctx, link.WorkspaceID, link.BoardRef, cursor, eventPageLimit)
		if err != nil {
			p.log.Warn("kanban poll: pull board events", "link", link.ID, "workspace", link.WorkspaceID,
				"board", link.BoardRef, "after_sequence", cursor, "err", err)
			return
		}
		if page == nil {
			p.log.Warn("kanban poll: pull board events returned no page", "link", link.ID, "after_sequence", cursor)
			return
		}
		if len(page.Events) == 0 {
			if page.HasMore {
				p.log.Warn("kanban poll: invalid empty event page with has_more", "link", link.ID, "after_sequence", cursor)
			}
			return
		}

		// The event contains a path but not a document id. Resolve all candidate
		// paths with one list call per page; pages containing only non-trigger
		// events avoid the list entirely.
		var docsByPath map[string]jtype.Doc
		if pageHasTriggerEvent(page.Events, link) {
			docs, err := api.ListDocuments(ctx, link.WorkspaceID)
			if err != nil {
				p.log.Warn("kanban poll: resolve event document paths", "link", link.ID, "workspace", link.WorkspaceID, "err", err)
				return
			}
			docsByPath = make(map[string]jtype.Doc, len(docs))
			for _, d := range docs {
				docsByPath[d.Path] = d
			}
		}

		committed := cursor
		for i := range page.Events {
			event := &page.Events[i]
			if event.Sequence <= committed {
				p.log.Warn("kanban poll: non-increasing event sequence", "link", link.ID,
					"after_sequence", committed, "event_sequence", event.Sequence)
				return
			}
			if event.Board != link.BoardRef || event.Card.Status != link.TriggerColumn {
				committed = event.Sequence
				continue
			}
			d, ok := docsByPath[event.Card.Path]
			if !ok || !isMarkdown(event.Card.Path) {
				// The card may have been deleted or renamed after this immutable event.
				// There is no document left to dispatch; a later event for its new path
				// remains independently visible, so this entry is safely consumed.
				p.log.Warn("kanban poll: event document no longer exists; skipping", "link", link.ID,
					"sequence", event.Sequence, "path", event.Card.Path)
				committed = event.Sequence
				continue
			}
			if !p.maybeDispatch(ctx, api, link, d, event) {
				p.commitEventSequence(ctx, link, cursor, committed)
				return
			}
			committed = event.Sequence
		}

		if !p.commitEventSequence(ctx, link, cursor, committed) {
			return
		}
		if committed == cursor {
			p.log.Warn("kanban poll: event page made no progress", "link", link.ID, "after_sequence", cursor)
			return
		}
		cursor = committed
		if !page.HasMore {
			return
		}
	}
}

func pageHasTriggerEvent(events []jtype.KanbanEvent, link *domain.KanbanLink) bool {
	for i := range events {
		if events[i].Board == link.BoardRef && events[i].Card.Status == link.TriggerColumn {
			return true
		}
	}
	return false
}

// commitEventSequence persists a successfully handled prefix. A store failure
// intentionally causes replay; claims prevent duplicate dispatch.
func (p *Poller) commitEventSequence(ctx context.Context, link *domain.KanbanLink, previous, sequence int64) bool {
	if sequence <= previous {
		return true
	}
	if err := p.st.AdvanceKanbanLinkEventSequence(ctx, link.ID, sequence); err != nil {
		p.log.Error("kanban poll: commit event cursor", "link", link.ID, "sequence", sequence, "err", err)
		return false
	}
	value := sequence
	link.EventSequence = &value
	return true
}

// revalidateBoard runs the D30 runtime board check for a link whose board_status
// is not "ok" (each tick until it resolves; the DB write in markBoardInvalid is
// idempotent so an already-invalid link doesn't churn the row). It returns true
// only when the board resolved and its
// columns validated — in which case link.BoardRef is canonicalized IN PLACE (to
// the board's config id) and board_status is persisted as "ok" so the same tick's
// scan matches cards by that id. A definitive failure (board gone/renamed or a
// column mismatch) persists board_status="invalid", logs once (fail-visible), and
// returns false. A transient error leaves the status untouched and returns false
// (retry next tick). Errors here never crash the tick.
func (p *Poller) revalidateBoard(ctx context.Context, api DocumentAPI, link *domain.KanbanLink) bool {
	board, err := api.GetBoard(ctx, link.WorkspaceID, link.BoardRef)
	if err != nil {
		var ambig *jtype.ErrBoardAmbiguousError
		definitive := errors.Is(err, jtype.ErrDocNotFound) || errors.As(err, &ambig)
		if !definitive {
			// A jtype 4xx that is a config/auth problem (401/403/404) is also definitive
			// (the ref won't resolve until fixed); a 5xx/transport error is transient.
			var je *jtype.Error
			if errors.As(err, &je) && je.StatusCode >= 400 && je.StatusCode < 500 {
				definitive = true
			}
		}
		if !definitive {
			p.log.Warn("kanban poll: board revalidation transient error; retry next tick",
				"link", link.ID, "workspace", link.WorkspaceID, "board", link.BoardRef, "err", err)
			return false
		}
		p.markBoardInvalid(ctx, link, err)
		return false
	}
	if !boardHasColumn(board, link.TriggerColumn) ||
		(link.DoneColumn != "" && !boardHasColumn(board, link.DoneColumn)) {
		p.markBoardInvalid(ctx, link, errColumnMismatch)
		return false
	}
	// Resolved + columns valid: canonicalize the ref to the board's config id (what
	// cards carry in frontmatter) so the scan below matches (RC2), and persist "ok".
	canonical := link.BoardRef
	if board.ID != "" {
		canonical = board.ID
	}
	if err := p.st.SetKanbanLinkBoardStatus(ctx, link.ID, domain.KanbanBoardOK, canonical, board.Title); err != nil {
		p.log.Error("kanban poll: persist board_status=ok", "link", link.ID, "err", err)
		return false // don't scan with an unpersisted canonical ref; retry next tick
	}
	link.BoardRef = canonical
	link.BoardTitle = board.Title
	link.BoardStatus = domain.KanbanBoardOK
	p.noted.Delete("board_invalid:" + link.ID) // a later re-break logs again
	p.log.Info("kanban poll: link board validated", "link", link.ID, "workspace", link.WorkspaceID, "board", canonical)
	return true
}

// markBoardInvalid persists board_status="invalid" (keeping the last-known ref)
// and logs once per link so a broken soft-created link is loud, not silent. The
// DB write is idempotent: an already-invalid link is re-checked each tick (so it
// self-heals if the board returns) but must NOT re-write the row / bump updated_at
// every tick — `link` is loaded fresh per tick, so BoardStatus reflects the
// persisted value and gates the write to the transition INTO invalid.
func (p *Poller) markBoardInvalid(ctx context.Context, link *domain.KanbanLink, cause error) {
	if boardStatusOrOK(link.BoardStatus) != domain.KanbanBoardInvalid {
		if err := p.st.SetKanbanLinkBoardStatus(ctx, link.ID, domain.KanbanBoardInvalid, "", ""); err != nil {
			p.log.Error("kanban poll: persist board_status=invalid", "link", link.ID, "err", err)
		}
		link.BoardStatus = domain.KanbanBoardInvalid
	}
	if _, seen := p.noted.LoadOrStore("board_invalid:"+link.ID, struct{}{}); !seen {
		p.log.Warn("kanban poll: link board is invalid; skipping (board gone/renamed or columns changed)",
			"link", link.ID, "workspace", link.WorkspaceID, "board", link.BoardRef, "err", cause)
	}
}

// boardStatusOrOK treats an empty board_status as "ok" (defensive: a store/row
// that predates the 0024 column). Matches api.boardStatusOrDefault.
func boardStatusOrOK(s string) string {
	if s == "" {
		return domain.KanbanBoardOK
	}
	return s
}

// boardHasColumn reports whether the board has a column with the given key.
func boardHasColumn(b *jtype.Board, key string) bool {
	if b == nil {
		return false
	}
	for _, c := range b.Columns {
		if c.Key == key {
			return true
		}
	}
	return false
}

// errColumnMismatch is the sentinel cause logged when a board resolves but its
// trigger/done columns no longer exist (a definitive "invalid").
var errColumnMismatch = errStr("trigger/done column not found on the resolved board")

type errStr string

func (e errStr) Error() string { return string(e) }

// maybeDispatch fetches one document and dispatches a run (or posts the
// not-configured notice). During bootstrap event is nil and the current
// frontmatter must match the linked board + trigger column. During sequence
// processing the immutable event snapshot is the transition authority; the
// current document is fetched for its body and may already have moved again.
// The return value says whether this input was fully handled and its sequence may
// be committed. False means retry from the same event on the next tick.
func (p *Poller) maybeDispatch(ctx context.Context, api DocumentAPI, link *domain.KanbanLink, d jtype.Doc, event *jtype.KanbanEvent) bool {
	doc, err := api.GetDocument(ctx, link.WorkspaceID, d.ID)
	if err != nil {
		p.log.Warn("kanban poll: get document", "link", link.ID, "doc", d.ID, "err", err)
		return false
	}
	card := jtype.ParseCard(doc.Content)
	if event == nil && (card.Board != link.BoardRef || card.Status != link.TriggerColumn) {
		return true // bootstrap: not a card on this board/trigger column
	}
	if event != nil {
		if event.Board != link.BoardRef || event.Card.Status != link.TriggerColumn {
			return true
		}
		if card.Title == "" {
			card.Title = event.Card.Title
		}
	}
	prompt := buildPrompt(card)
	claim, err := p.st.EnsureKanbanClaim(ctx, link.ID, d.ID, d.Path)
	if err != nil {
		p.log.Error("kanban poll: ensure claim", "link", link.ID, "doc", d.ID, "err", err)
		return false
	}
	if claim.RunID != "" {
		return true // already dispatched for this card — idempotent
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
		return false
	}
	sel, outcome, err := p.models.SelectModel(ctx, link.ProjectID, defaultModelID, "")
	if err != nil {
		p.log.Error("kanban poll: resolve model", "link", link.ID, "doc", d.ID, "err", err)
		return false // transient; retry next tick
	}
	if outcome != modelcfg.SelectOK {
		// Both "can't dispatch" states are notify-once + retried on later ticks, but
		// the card comment DIFFERS (P5): NotSelected points the service owner at the
		// default-model setting, NotConfigured points at cluster-admin authorization.
		p.notifyBlocked(ctx, api, link, d.ID, outcome)
		return false
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
		return false // run_id stays empty; retried next tick (no claim leak — no run yet)
	}
	// Commit the dispatch: stamp the claim's run_id. A racing tick that already
	// stamped a run loses (SetKanbanClaimRun is conditional), and the orphan run
	// simply completes as a normal kanban run (its own claim's writeback marker
	// is what dedups writeback, not the run) — acceptable and rare.
	if err := p.st.SetKanbanClaimRun(ctx, link.ID, d.ID, run.ID); err != nil {
		p.log.Error("kanban poll: stamp claim run", "link", link.ID, "doc", d.ID, "run", run.ID, "err", err)
		return false
	}
	p.log.Info("kanban poll: dispatched run", "link", link.ID, "doc", d.ID, "run", run.ID, "card", card.Title)
	return true
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

// _ asserts *jtype.Client satisfies DocumentAPI at compile time.
var _ DocumentAPI = (*jtype.Client)(nil)
