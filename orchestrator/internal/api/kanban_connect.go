package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtypeoauth"
	"github.com/cnjack/jcloud/internal/store"
)

// "Connect with jtype" OAuth device flow (D28). The console drives an RFC 8628
// device flow to mint a kanban credential (cluster fallback token or per-link
// token) without a hand-pasted PAT. jcloud is a stateless proxy over jtype's two
// UNauthenticated OAuth endpoints (internal/jtypeoauth) plus an in-memory
// registry of pending flows — there is NO background poller and NO DB
// persistence of the device_code (a SECRET). The console (React Query) drives
// the cadence; each console poll triggers at most ONE jtype token poll, gated by
// the flow's `interval`. A restart drops in-flight flows → the next poll 404s
// connect_expired and the user reconnects (a 10-minute window).
//
// Security (D28 §5): the device_code and the minted access_token NEVER reach the
// browser and are NEVER logged; only connect_id / surface / base-URL host /
// status are logged. On success the token is sealed server-side (AES-256-GCM,
// AUTH_TOKEN_KEY) and written to the target row; the poll response carries only
// {status, token_set, token_expires_at} — the plaintext never leaves the server.

// oauthClient is the slice of *jtypeoauth.Client the connect handlers need. It is
// the seam a test injects a fake (with a poll spy) through; production wires
// jtypeoauth.NewClient (see api.New).
type oauthClient interface {
	StartDeviceAuthorization(ctx context.Context) (*jtypeoauth.DeviceAuth, error)
	PollToken(ctx context.Context, deviceCode string) (*jtypeoauth.Token, jtypeoauth.Status, error)
}

// Connect surface kinds + poll status strings (the poll status enum the console
// keys off — pending is the non-terminal state; the rest are terminal).
const (
	surfaceCluster = "cluster"
	surfaceLink    = "link"

	connectPending     = "pending"
	connectComplete    = "complete"
	connectExpired     = "expired"
	connectDenied      = "denied"
	connectUnsupported = "unsupported"
)

// maxConnectFlows caps concurrent in-flight flows so a leaked/abandoned flow
// cannot grow the registry unbounded (device_code is a secret held in memory).
// A create past the cap sweeps time-expired flows, then evicts the oldest.
const maxConnectFlows = 128

// connectSurface identifies what a flow's minted token binds to: the cluster
// fallback token (cluster_kanban_config) or a specific project's kanban link.
type connectSurface struct {
	kind      string // surfaceCluster | surfaceLink
	linkID    string // surfaceLink only
	projectID string // surfaceLink only (authz + poll scoping)
}

// connectRecord is one in-flight device flow. It is keyed by an opaque 256-bit
// connect_id; the device_code it holds is a SECRET that can mint the token and
// never leaves the process.
//
// LOCKING INVARIANT: every field ABOVE mu (identity, surface, baseURL, the
// codes, expiresAt) is set once at creation and NEVER mutated afterwards —
// readers need no lock (publication happens-before is established by reg.mu on
// add/get). The registry's sweep/evict, which run UNDER the global reg.mu,
// depend on this: they read expiresAt lock-free, so a poll holding rec.mu
// across a slow jtype network call can never stall the whole registry
// (head-of-line). Only the poll state BELOW mu is guarded by mu; advanceConnect
// holds mu across its (at most one) jtype poll so polls of the SAME flow
// serialize — polls of different flows never do. No reg.mu-held path may take
// rec.mu.
type connectRecord struct {
	connectID               string
	principal               string // principalIdentity of the starter (poll re-checks it)
	updatedBy               string // user id recorded on the cluster row (audit)
	surface                 connectSurface
	baseURL                 string // the jtype base URL the flow authenticates against
	deviceCode              string // SECRET — never serialized, never logged
	userCode                string
	verificationURI         string
	verificationURIComplete string
	expiresAt               time.Time // end of the RFC 8628 device-flow window

	mu             sync.Mutex
	interval       time.Duration
	lastPolledAt   time.Time  // interval-gate cursor
	terminal       string     // "" while pending; else a terminal status string
	tokenExpiresAt *time.Time // set on complete (now + token TTL)
}

// statusViewLocked renders the poll response from the record; caller holds mu.
func (rec *connectRecord) statusViewLocked() kanbanConnectStatusView {
	status := connectPending
	if rec.terminal != "" {
		status = rec.terminal
	}
	v := kanbanConnectStatusView{Status: status, TokenSet: rec.terminal == connectComplete}
	if rec.tokenExpiresAt != nil {
		v.TokenExpiresAt = rec.tokenExpiresAt.UTC().Format(time.RFC3339)
	}
	return v
}

// connectRegistry is the process-wide in-memory table of pending flows. mu guards
// the map structure; each record's own mu guards its poll state, so polls of
// different flows never serialize. now is injectable for tests.
type connectRegistry struct {
	mu   sync.Mutex
	byID map[string]*connectRecord
	now  func() time.Time
}

func newConnectRegistry() *connectRegistry {
	return &connectRegistry{byID: map[string]*connectRecord{}, now: time.Now}
}

// add registers a fresh flow, first sweeping time-expired flows and — if still at
// the cap — evicting the oldest, so the registry stays bounded (D28 §1.2).
func (reg *connectRegistry) add(rec *connectRecord) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.sweepLocked()
	if len(reg.byID) >= maxConnectFlows {
		reg.evictOldestLocked()
	}
	reg.byID[rec.connectID] = rec
}

// get returns the flow for connectID, or nil (unknown / swept). Also runs a lazy
// sweep so expired flows do not linger.
func (reg *connectRegistry) get(connectID string) *connectRecord {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.sweepLocked()
	return reg.byID[connectID]
}

// sweepLocked drops flows past their expiry window (lazy expiry, no goroutine).
// Terminal-but-not-yet-expired flows are KEPT so a just-completed flow stays
// readable until its window closes. Caller holds reg.mu. It reads rec.expiresAt
// LOCK-FREE (immutable after creation — see connectRecord): taking rec.mu here
// would let one flow blocked mid-poll (rec.mu held across the jtype network
// call) stall every other start/poll behind reg.mu.
func (reg *connectRegistry) sweepLocked() {
	now := reg.now()
	for id, rec := range reg.byID {
		if now.After(rec.expiresAt) {
			delete(reg.byID, id)
		}
	}
}

// evictOldestLocked removes the flow with the earliest expiry (the oldest
// window). Caller holds reg.mu; rec.expiresAt is read lock-free (immutable —
// see sweepLocked).
func (reg *connectRegistry) evictOldestLocked() {
	var oldestID string
	var oldest time.Time
	for id, rec := range reg.byID {
		if oldestID == "" || rec.expiresAt.Before(oldest) {
			oldestID, oldest = id, rec.expiresAt
		}
	}
	if oldestID != "" {
		delete(reg.byID, oldestID)
	}
}

// kanbanConnectStartView is the POST .../connect response. device_code is
// WITHHELD (a secret); user_code + the verification URIs are what the browser
// needs to approve (D28 §2.1).
type kanbanConnectStartView struct {
	ConnectID               string `json:"connect_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// kanbanConnectStatusView is the GET .../connect/{id} poll response. It carries
// ONLY the status enum + token_set + (on complete) the sealed token's expiry —
// never the plaintext token (D28 §1.3).
type kanbanConnectStatusView struct {
	Status         string `json:"status"` // pending|complete|expired|denied|unsupported
	TokenSet       bool   `json:"token_set"`
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
}

// --- cluster fallback token (requireClusterAdmin) ---------------------------

// handleStartKanbanConnect starts a device flow for the CLUSTER fallback token
// (D28 §2.1). Preconditions, in order: cluster-admin; a cipher (checked AT START,
// before any jtype call — a token we cannot seal is a config error); a DB
// override row with a base_url (D27 same-source binding). ErrOAuthUnsupported ⇒
// a typed 409 so the console falls back to the paste path (never a silent mock).
func (s *Server) handleStartKanbanConnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before connecting a cluster jtype token")
		return
	}
	row, err := s.st.GetClusterKanbanConfig(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "base_url_not_configured",
			"Save the jtype base URL before connecting.")
		return
	}
	if err != nil {
		s.log.Error("kanban connect: read cluster config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read kanban config")
		return
	}
	s.startConnectFlow(w, r, connectSurface{kind: surfaceCluster}, row.BaseURL)
}

// handlePollKanbanConnect polls a cluster flow (D28 §2.1). It RE-authorizes
// (cluster-admin) and checks the record principal — a leaked connect_id is
// unusable by another subject (surfaced as connect_expired, not an existence
// leak). On complete the token is already sealed into cluster_kanban_config and
// the resolver invalidated; this only reads the outcome.
func (s *Server) handlePollKanbanConnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	rec := s.connects.get(r.PathValue("connectID"))
	if rec == nil || rec.surface.kind != surfaceCluster ||
		rec.principal != principalIdentity(principalFrom(r.Context())) {
		writeError(w, http.StatusNotFound, "connect_expired", "Connection expired — click Connect again.")
		return
	}
	writeJSON(w, http.StatusOK, s.advanceConnect(r.Context(), rec))
}

// --- per-link token (authorizeProject RoleOwner) ----------------------------

// handleStartLinkConnect starts a device flow for an EXISTING project kanban
// link (D28 §2.2, create-then-connect). Preconditions: project owner; a cipher
// (AT START); the effective cluster kanban config is enabled (else 409
// kanban_not_configured); the link exists and belongs to the path project. The
// flow targets the EFFECTIVE cluster base URL.
func (s *Server) handleStartLinkConnect(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before connecting a per-link jtype token")
		return
	}
	eff, err := s.kanban.Effective(r.Context())
	if err != nil || !eff.Enabled() {
		writeError(w, http.StatusConflict, "kanban_not_configured",
			"configure the cluster jtype base URL before connecting a per-link token")
		return
	}
	linkID := r.PathValue("linkID")
	link, err := s.st.GetKanbanLink(r.Context(), linkID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && link.ProjectID != projectID) {
		writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
		return
	}
	if err != nil {
		s.log.Error("kanban connect: load link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load kanban link")
		return
	}
	s.startConnectFlow(w, r, connectSurface{kind: surfaceLink, linkID: linkID, projectID: projectID}, eff.BaseURL)
}

// handlePollLinkConnect polls a per-link flow (D28 §2.2). Re-authorizes (project
// owner) and checks the record principal AND that the connect_id is bound to
// exactly this project's link (else connect_expired). On complete the token is
// sealed into kanban_links.token_enc (+token_expires_at); no resolver
// invalidation is needed (per-link tokens are read fresh each poller tick).
func (s *Server) handlePollLinkConnect(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	rec := s.connects.get(r.PathValue("connectID"))
	if rec == nil || rec.surface.kind != surfaceLink ||
		rec.surface.linkID != r.PathValue("linkID") || rec.surface.projectID != projectID ||
		rec.principal != principalIdentity(principalFrom(r.Context())) {
		writeError(w, http.StatusNotFound, "connect_expired", "Connection expired — click Connect again.")
		return
	}
	writeJSON(w, http.StatusOK, s.advanceConnect(r.Context(), rec))
}

// --- shared start + poll advance --------------------------------------------

// startConnectFlow calls jtype's device_authorization, registers the flow, and
// returns the start view. The device_code is stored server-side only; the
// response withholds it (D28 §1.2). All errors are typed + fail-visible.
func (s *Server) startConnectFlow(w http.ResponseWriter, r *http.Request, surface connectSurface, baseURL string) {
	da, err := s.oauthClientFor(baseURL).StartDeviceAuthorization(r.Context())
	if errors.Is(err, jtypeoauth.ErrOAuthUnsupported) {
		writeError(w, http.StatusConflict, "jtype_oauth_unsupported",
			"This jtype deployment does not support Connect — paste a token instead.")
		return
	}
	if err != nil {
		s.log.Warn("kanban connect: start device authorization",
			"surface", surface.kind, "host", hostOf(baseURL), "err", err)
		writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
			"could not reach jtype to start the connect flow")
		return
	}

	expiresIn := da.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 600
	}
	interval := da.Interval
	if interval <= 0 {
		interval = 2
	}
	p := principalFrom(r.Context())
	now := s.connects.now()
	rec := &connectRecord{
		connectID:               newConnectID(),
		principal:               principalIdentity(p),
		updatedBy:               p.userID(),
		surface:                 surface,
		baseURL:                 baseURL,
		deviceCode:              da.DeviceCode,
		userCode:                da.UserCode,
		verificationURI:         da.VerificationURI,
		verificationURIComplete: da.VerificationURIComplete,
		expiresAt:               now.Add(time.Duration(expiresIn) * time.Second),
		interval:                time.Duration(interval) * time.Second,
	}
	s.connects.add(rec)
	// NON-secret context only (never device_code/user_code/access_token, D28 §5).
	s.log.Info("kanban connect started", "connect", rec.connectID, "surface", surface.kind, "host", hostOf(baseURL))

	writeJSON(w, http.StatusOK, kanbanConnectStartView{
		ConnectID:               rec.connectID,
		UserCode:                da.UserCode,
		VerificationURI:         da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete,
		ExpiresIn:               expiresIn,
		Interval:                interval,
	})
}

// advanceConnect performs at most ONE jtype token poll and returns the current
// status. Held under the record's mutex so concurrent polls of the SAME flow
// serialize (different flows never do). Order: already-terminal → time-expiry →
// base-URL-changed-mid-flow → interval gate → poll (D28 §1.2/§1.3).
func (s *Server) advanceConnect(ctx context.Context, rec *connectRecord) kanbanConnectStatusView {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	now := s.connects.now()

	if rec.terminal != "" {
		return rec.statusViewLocked()
	}
	if now.After(rec.expiresAt) {
		rec.terminal = connectExpired
		return rec.statusViewLocked()
	}
	// A base_url changed under the flow (console edit / delete) ⇒ a token minted
	// against a now-stale instance must never be stored: expire the flow.
	if cur, ok := s.currentBaseURL(ctx, rec.surface); !ok || cur != rec.baseURL {
		rec.terminal = connectExpired
		return rec.statusViewLocked()
	}
	// Interval gate: a poll faster than `interval` returns the cached (pending)
	// status WITHOUT hitting jtype — the rate limit + slow_down backoff.
	if !rec.lastPolledAt.IsZero() && now.Sub(rec.lastPolledAt) < rec.interval {
		return rec.statusViewLocked()
	}

	tok, st, err := s.oauthClientFor(rec.baseURL).PollToken(ctx, rec.deviceCode)
	rec.lastPolledAt = now
	switch {
	case errors.Is(err, jtypeoauth.ErrOAuthUnsupported):
		rec.terminal = connectUnsupported
	case err != nil:
		// Transient (5xx / transport / unexpected): stay pending, log non-secret ctx.
		s.log.Warn("kanban connect: poll transient error", "connect", rec.connectID,
			"surface", rec.surface.kind, "host", hostOf(rec.baseURL), "err", err)
	case st == jtypeoauth.StatusComplete:
		s.completeConnectLocked(ctx, rec, tok, now)
	case st == jtypeoauth.StatusSlowDown:
		rec.interval += 5 * time.Second // back off; stay pending
	case st == jtypeoauth.StatusExpired:
		rec.terminal = connectExpired
	case st == jtypeoauth.StatusDenied:
		rec.terminal = connectDenied
	default: // StatusPending
	}
	return rec.statusViewLocked()
}

// completeConnectLocked seals the minted token and writes it to the flow's target
// row, marking the record terminal-complete. Caller holds rec.mu. A seal/store
// failure is fail-visible (mark expired + log) — NEVER a faked success.
func (s *Server) completeConnectLocked(ctx context.Context, rec *connectRecord, tok *jtypeoauth.Token, now time.Time) {
	if s.cipher == nil {
		// Pathological: cipher was verified at start and is immutable in production.
		s.log.Error("kanban connect: minted token but no cipher to seal it", "connect", rec.connectID)
		rec.terminal = connectExpired
		return
	}
	enc, err := s.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		s.log.Error("kanban connect: seal minted token", "connect", rec.connectID, "err", err)
		rec.terminal = connectExpired
		return
	}
	// token_expires_at = now + the token's TTL; unknown TTL ⇒ nil (honest, not
	// "expires now"). jtype mints a 90-day session (expires_in=7776000).
	var expPtr *time.Time
	if tok.ExpiresIn > 0 {
		exp := now.Add(time.Duration(tok.ExpiresIn) * time.Second)
		expPtr = &exp
	}

	switch rec.surface.kind {
	case surfaceCluster:
		// CONDITIONAL token write (anti-TOCTOU): the store updates token_enc /
		// token_expires_at ONLY where the row STILL carries the base URL this flow
		// authenticated against — it NEVER writes base_url, so an admin PUT that
		// re-pointed the config during the completing poll wins, and a token minted
		// against the stale instance is never stored (ErrNotFound ⇒ the flow
		// expires, matching the mid-flow-change contract).
		err := s.st.SetClusterKanbanToken(ctx, rec.baseURL, enc, expPtr, rec.updatedBy)
		if errors.Is(err, store.ErrNotFound) {
			rec.terminal = connectExpired
			return
		}
		if err != nil {
			s.log.Error("kanban connect: store cluster token", "connect", rec.connectID, "err", err)
			rec.terminal = connectExpired
			return
		}
		s.kanban.Invalidate() // activate without a restart (fail-visible, D27)
	case surfaceLink:
		if err := s.st.SetKanbanLinkToken(ctx, rec.surface.linkID, enc, expPtr); err != nil {
			s.log.Error("kanban connect: store link token", "connect", rec.connectID, "err", err)
			rec.terminal = connectExpired
			return
		}
	}
	rec.terminal = connectComplete
	rec.tokenExpiresAt = expPtr
	s.log.Info("kanban connect complete", "connect", rec.connectID, "surface", rec.surface.kind)
}

// currentBaseURL re-resolves the base URL a flow's surface targets NOW, so the
// poll can detect a base_url that changed under it. ok=false (row gone / config
// off) is treated as "changed" by the caller.
func (s *Server) currentBaseURL(ctx context.Context, surface connectSurface) (string, bool) {
	switch surface.kind {
	case surfaceCluster:
		row, err := s.st.GetClusterKanbanConfig(ctx)
		if err != nil {
			return "", false
		}
		return row.BaseURL, true
	case surfaceLink:
		eff, err := s.kanban.Effective(ctx)
		if err != nil || !eff.Enabled() {
			return "", false
		}
		return eff.BaseURL, true
	}
	return "", false
}

// principalIdentity is the opaque identity a connect flow is bound to, so a poll
// can prove the SAME subject that started it is polling (a leaked connect_id is
// unusable by anyone else). The service principal and each user get distinct keys.
func principalIdentity(p *principal) string {
	switch {
	case p == nil:
		return ""
	case p.service:
		return "service"
	case p.userID() != "":
		return "user:" + p.userID()
	case p.scopedProjectID != "":
		return "key:" + p.scopedProjectID
	default:
		return ""
	}
}

// newConnectID mints an opaque 256-bit connect id (unguessable; the only handle
// the browser holds — the device_code stays server-side). A crypto/rand failure
// is unrecoverable — a predictable connect_id would be a security hole — so it
// panics (the API's recover middleware turns it into a 500).
func newConnectID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("kanban connect: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// hostOf returns a URL's host for NON-secret logging (never the full base URL's
// path/query, though jtype base URLs carry none).
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}
