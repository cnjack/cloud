package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// jcode device login — an RFC 8628 device-code flow (docs/17-jcode-device-relay
// §3) that lets a local jcode CLI authenticate to jcloud and register as a
// "device". Three endpoints:
//
//	POST /auth/device/code       (unauthenticated) CLI starts a flow
//	POST /auth/device/token      (unauthenticated) CLI polls for its token
//	POST /auth/device/authorize  (session)         the user approves/denies
//
// Pending flows live in an in-memory registry (the same pattern as the
// "Connect with jtype" connectRegistry — see kanban_connect.go): a restart
// drops in-flight flows, so the CLI's next poll answers expired_token and the
// user runs `jcode login` again. Nothing about a flow is secret-bearing in the
// DB: the device token's plaintext is returned EXACTLY ONCE from
// /auth/device/token and only its SHA-256 lands in device_tokens.
//
// Redemption policy (docs/17 leaves this open): an approved flow can be
// redeemed EXACTLY ONCE — the first /auth/device/token poll after approval
// mints the device + token and consumes the flow. A second poll answers 400
// token_already_redeemed. A client that lost the response re-runs the login;
// a replayed device_code can never mint a second token for the same approval.

// deviceFlowWindow is the RFC 8628 flow lifetime (expires_in) and
// deviceFlowPollInterval the client poll cadence hint (interval).
const (
	deviceFlowWindow       = 600 * time.Second
	deviceFlowPollInterval = 5
	maxDeviceFlows         = 128 // same bound rationale as maxConnectFlows
	deviceFlowPending      = "pending"
	deviceFlowApproved     = "approved"
	deviceFlowDenied       = "denied"
	deviceFlowRedeemed     = "redeemed"
)

// deviceFlow is one in-flight device-code login. Fields above mu are set once
// at creation and never mutated (publication happens-before via reg.mu — the
// same locking invariant as connectRecord); the decision state below mu is
// guarded by mu so authorize and the token poll serialise per flow.
type deviceFlow struct {
	deviceCode string // long random secret the CLI polls with
	userCode   string // short human code the user types into the console
	clientName string // becomes devices.name at redemption
	expiresAt  time.Time

	mu     sync.Mutex
	status string // deviceFlow* — "" is never used; starts at pending
	userID string // approver, set on approve
}

// deviceFlowRegistry is the process-wide table of pending flows, indexed by
// BOTH device_code (the CLI's poll key) and user_code (the console's authorize
// key). now is injectable for tests.
type deviceFlowRegistry struct {
	mu         sync.Mutex
	byCode     map[string]*deviceFlow
	byUserCode map[string]*deviceFlow
	now        func() time.Time
}

func newDeviceFlowRegistry() *deviceFlowRegistry {
	return &deviceFlowRegistry{
		byCode:     map[string]*deviceFlow{},
		byUserCode: map[string]*deviceFlow{},
		now:        time.Now,
	}
}

// add registers a fresh flow, first sweeping time-expired flows and — if still
// at the cap — evicting the oldest, so the registry stays bounded.
func (reg *deviceFlowRegistry) add(f *deviceFlow) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.sweepLocked()
	if len(reg.byCode) >= maxDeviceFlows {
		reg.evictOldestLocked()
	}
	reg.byCode[f.deviceCode] = f
	reg.byUserCode[f.userCode] = f
}

// getByCode / getByUserCode return the flow for the given key, or nil
// (unknown / swept). Both run the lazy sweep so expired flows do not linger.
func (reg *deviceFlowRegistry) getByCode(deviceCode string) *deviceFlow {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.sweepLocked()
	return reg.byCode[deviceCode]
}

func (reg *deviceFlowRegistry) getByUserCode(userCode string) *deviceFlow {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.sweepLocked()
	return reg.byUserCode[userCode]
}

// sweepLocked drops flows past their expiry window (lazy expiry, no goroutine).
// Caller holds reg.mu; it reads f.expiresAt lock-free (immutable — see
// deviceFlow). Decided-but-unexpired flows are KEPT so their terminal answer
// (access_denied / token_already_redeemed) stays stable until the window ends.
func (reg *deviceFlowRegistry) sweepLocked() {
	now := reg.now()
	for code, f := range reg.byCode {
		if now.After(f.expiresAt) {
			delete(reg.byCode, code)
			delete(reg.byUserCode, f.userCode)
		}
	}
}

// evictOldestLocked removes the flow with the earliest expiry. Caller holds
// reg.mu; f.expiresAt is read lock-free (immutable — see sweepLocked).
func (reg *deviceFlowRegistry) evictOldestLocked() {
	var oldestCode, oldestUserCode string
	var oldest time.Time
	for code, f := range reg.byCode {
		if oldestCode == "" || f.expiresAt.Before(oldest) {
			oldestCode, oldestUserCode, oldest = code, f.userCode, f.expiresAt
		}
	}
	if oldestCode != "" {
		delete(reg.byCode, oldestCode)
		delete(reg.byUserCode, oldestUserCode)
	}
}

// userCodeAlphabet excludes look-alikes (0/O, 1/I/L) so a code read off a
// terminal and typed by hand survives font ambiguity.
const userCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newDeviceCode mints the long random poll credential (32 bytes, hex).
func newDeviceCode() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("device flow: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// newUserCode mints the human code, formatted "XXXX-XXXX".
func newUserCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("device flow: crypto/rand failed: " + err.Error())
	}
	var sb strings.Builder
	for i, c := range b {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(userCodeAlphabet[int(c)%len(userCodeAlphabet)])
	}
	return sb.String()
}

// normalizeUserCode canonicalises a user-supplied code: uppercased, dash
// restored — the console accepts "abcd-efgh", "ABCDEFGH" or "abcd efgh".
func normalizeUserCode(raw string) string {
	s := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.TrimSpace(raw)))
	if len(s) == 8 {
		return s[:4] + "-" + s[4:]
	}
	return s
}

// --- request/response views ---------------------------------------------------

type deviceCodeReq struct {
	ClientName string `json:"client_name"`
}

// deviceCodeView is the POST /auth/device/code response (docs/17 §3.1).
type deviceCodeView struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceTokenReq struct {
	DeviceCode string `json:"device_code"`
}

// deviceTokenView is the successful POST /auth/device/token response. The
// access_token plaintext appears in exactly this one response, ever.
type deviceTokenView struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	DeviceID    string `json:"device_id"`
}

type deviceAuthorizeReq struct {
	UserCode string `json:"user_code"`
	Approve  bool   `json:"approve"`
}

// --- handlers -----------------------------------------------------------------

// handleDeviceCode starts a device login (docs/17 §3.1). Unauthenticated: the
// whole point is that the CLI has no credential yet. client_name becomes the
// device's display name.
func (s *Server) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	var req deviceCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	clientName := strings.TrimSpace(req.ClientName)
	if clientName == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "client_name is required")
		return
	}

	f := &deviceFlow{
		deviceCode: newDeviceCode(),
		userCode:   newUserCode(),
		clientName: clientName,
		expiresAt:  s.deviceFlows.now().Add(deviceFlowWindow),
		status:     deviceFlowPending,
	}
	// A user_code collision with a live flow is astronomically unlikely but
	// free to avoid: redraw a few times before accepting the registry's word.
	for i := 0; i < 8 && s.deviceFlows.getByUserCode(f.userCode) != nil; i++ {
		f.userCode = newUserCode()
	}
	s.deviceFlows.add(f)
	s.log.Info("device login started", "client", f.clientName, "expires_at", f.expiresAt)

	writeJSON(w, http.StatusOK, deviceCodeView{
		DeviceCode:      f.deviceCode,
		UserCode:        f.userCode,
		VerificationURI: strings.TrimRight(s.cfg.ConsoleURL, "/") + "/device",
		ExpiresIn:       int(deviceFlowWindow / time.Second),
		Interval:        deviceFlowPollInterval,
	})
}

// handleDeviceToken is the CLI's poll (docs/17 §3.1). It maps the flow state
// onto the RFC 8628 error codes inside the repo-standard error envelope:
// pending → 400 authorization_pending, denied → 400 access_denied, expired (or
// unknown — a swept flow is indistinguishable) → 400 expired_token, already
// redeemed → 400 token_already_redeemed. On the FIRST poll after approval it
// creates the devices + device_tokens rows and returns the token plaintext —
// the one and only time it ever appears.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req deviceTokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device_code is required")
		return
	}

	f := s.deviceFlows.getByCode(req.DeviceCode)
	if f == nil {
		writeError(w, http.StatusBadRequest, "expired_token", "the device_code is unknown or has expired")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if s.deviceFlows.now().After(f.expiresAt) {
		writeError(w, http.StatusBadRequest, "expired_token", "the device_code has expired")
		return
	}
	switch f.status {
	case deviceFlowPending:
		writeError(w, http.StatusBadRequest, "authorization_pending", "the user has not approved this login yet")
		return
	case deviceFlowDenied:
		writeError(w, http.StatusBadRequest, "access_denied", "the user denied this login")
		return
	case deviceFlowRedeemed:
		writeError(w, http.StatusBadRequest, "token_already_redeemed", "this device_code was already redeemed; run the login again")
		return
	}

	// Approved: mint the device + token, then consume the flow. If the store
	// write fails the flow stays approved so the CLI's retry can complete.
	now := time.Now().UTC()
	d := &domain.Device{
		ID:        domain.NewID(),
		UserID:    f.userID,
		Name:      f.clientName,
		KeyGen:    1,
		CreatedAt: now,
	}
	if err := s.st.CreateDevice(r.Context(), d); err != nil {
		s.log.Error("device login: create device", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the device")
		return
	}
	plaintext, err := auth.GenerateDeviceToken()
	if err != nil {
		s.log.Error("device login: generate token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not generate the device token")
		return
	}
	tok := &domain.DeviceToken{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		TokenHash: auth.HashToken(plaintext),
		CreatedAt: now,
	}
	if err := s.st.CreateDeviceToken(r.Context(), tok); err != nil {
		s.log.Error("device login: create device token", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the device token")
		return
	}
	f.status = deviceFlowRedeemed
	s.log.Info("device login redeemed", "device", d.ID, "user", d.UserID)

	writeJSON(w, http.StatusOK, deviceTokenView{
		AccessToken: plaintext,
		TokenType:   "device",
		DeviceID:    d.ID,
	})
}

// handleDeviceAuthorize approves or denies a pending flow (docs/17 §3.1),
// authenticated by the console session. Approving records the approving user;
// the CLI's next token poll then redeems the flow. A human session is
// REQUIRED: the service principal (CONSOLE_TOKEN) has no user to own the
// device, so it gets the same 400 the identity-link endpoint gives it.
func (s *Server) handleDeviceAuthorize(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.userID() == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a user session is required to authorize a device")
		return
	}
	var req deviceAuthorizeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	userCode := normalizeUserCode(req.UserCode)
	if userCode == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "user_code is required")
		return
	}

	f := s.deviceFlows.getByUserCode(userCode)
	if f == nil {
		writeError(w, http.StatusNotFound, "not_found", "no pending device login matches that code")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if s.deviceFlows.now().After(f.expiresAt) {
		writeError(w, http.StatusNotFound, "not_found", "no pending device login matches that code")
		return
	}
	if f.status != deviceFlowPending {
		writeError(w, http.StatusConflict, "already_decided", "this device login was already decided")
		return
	}
	if req.Approve {
		f.status = deviceFlowApproved
		f.userID = p.userID()
	} else {
		f.status = deviceFlowDenied
	}
	s.log.Info("device login decided", "status", f.status, "client", f.clientName, "user", p.userID())
	writeJSON(w, http.StatusOK, map[string]string{"status": f.status})
}

// requireDevice resolves the device principal for the /internal/v1/device/*
// handlers: it must have authenticated with a device token (isDevice). On
// failure it writes the 401 and returns nil; the caller must stop.
func (s *Server) requireDevice(w http.ResponseWriter, r *http.Request) *principal {
	p := principalFrom(r.Context())
	if !p.isDevice() {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a device token is required")
		return nil
	}
	return p
}

// loadDeviceForPrincipal fetches the device row the principal's token binds
// to, mapping a missing row to 404 (the device was deleted under a live
// token). On failure it writes the error and returns nil.
func (s *Server) loadDeviceForPrincipal(w http.ResponseWriter, r *http.Request, deviceID string) *domain.Device {
	d, err := s.st.GetDevice(r.Context(), deviceID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "device not found")
		return nil
	}
	if err != nil {
		s.log.Error("load device", "device", deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the device")
		return nil
	}
	return d
}
