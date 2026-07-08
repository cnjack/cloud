package main

// permission.go — F8a: the runner side of D22's permission-approval half
// (docs/02-decision-log.md D22; the plan/full_access session-mode split is
// F7a/session.go, already built). See main.go's package doc comment and
// driverClient.RequestPermission for the entry point.
//
// Contract this implements against (F8b builds the orchestrator side; tests
// here use an httptest mock — see permission_test.go):
//
//   - Events: agent.permission_request
//     {"request_id","tool_call_id","title","options":[{"option_id","name","kind"}]}
//     and agent.permission_resolved {"request_id","option_id","resolution":"user"|"timeout"}.
//     The REQUEST event is delivered SYNCHRONOUSLY and at-least-once
//     (EmitPermissionRequestSync, emitter.go): the first decision poll is
//     only issued AFTER the control plane has acknowledged (2xx) the event —
//     never while it is still sitting in the emitter's async batch queue.
//     The RESOLVED event rides the normal best-effort async pipeline
//     (EmitPermissionResolved).
//
//   - Decision long-poll: GET {ORCH_BASE_URL}/internal/v1/runs/{id}/permissions/{request_id}/decision
//     (Bearer RUN_TOKEN). 204 = not decided yet (poll again, same
//     retry/backoff/204-floor philosophy as next-prompt — see
//     controlPlaneClient.waitForPermissionDecision in session.go); 200
//     {"option_id":"..."} = the user's decision; 404/410 = the request
//     expired/is invalid, treated identically to a client-side timeout (the
//     agent can't tell the difference and doesn't need to).
//
//     F8b contract requirement (load-bearing for the timeout-deny semantics
//     above): an UNKNOWN request_id MUST be answered 204 (pending), NOT
//     404 — 404/410 are reserved for requests that once existed and have
//     since expired or been invalidated. acpdrive holds up its half of that
//     bargain by never polling before the request event is acknowledged
//     (the sync delivery above); a 404-for-unknown server would still race
//     any server-internal ingest asynchrony and convert a pending approval
//     into an instant deny.
//
//   - Client timeout: PERMISSION_TIMEOUT_SECONDS (default 300s, see
//     defaultPermissionTimeout in main.go). The budget covers BOTH the
//     synchronous request-event delivery and the decision poll. On timeout
//     (or 404/410, or an undeliverable request event) a reject-KIND option
//     is chosen and handed back to jcode as a NORMAL Selected outcome so
//     jcode handles the rejection itself and the run continues (this is a
//     tool call being denied, not the run failing); when NO reject-kind
//     option was offered (allow-only, or zero options) the answer is ACP's
//     Cancelled outcome instead — never an allow_* option, which would turn
//     timeout-DENY into timeout-ALLOW (fail-open).
//
//     Operational note: keep PERMISSION_TIMEOUT_SECONDS SIGNIFICANTLY
//     smaller than RUN_TIMEOUT (and the orchestrator's session idle TTL).
//     The whole turn blocks inside RequestPermission while waiting, so a
//     stalled approval with a too-large timeout burns the entire run into a
//     hard RUN_TIMEOUT failure instead of a clean per-request timeout-deny.
//     F8b enforces this relation orchestrator-side when injecting the env;
//     standalone/manual users must respect it themselves.

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// forwardPermissionRequest is the approval-mode body of RequestPermission
// (driverClient.RequestPermission in main.go branches into this once
// permissionMode == permissionModeApproval and the session is live). It:
//  1. generates a request_id and SYNCHRONOUSLY delivers
//     agent.permission_request (at-least-once; see EmitPermissionRequestSync)
//     — polling before the control plane has acknowledged the event would
//     let a 404-for-unknown answer masquerade as "expired" and instantly
//     deny a perfectly pending approval;
//  2. long-polls the orchestrator's decision endpoint, with delivery + poll
//     together bounded by c.permissionTimeout;
//  3. on a user decision, emits agent.permission_resolved{resolution:"user"}
//     and returns that option to jcode;
//  4. on timeout/expiry/undeliverable-request, emits
//     agent.permission_resolved{resolution:"timeout"} and returns a
//     deny-safe outcome (see timeoutDenyPermission) so the run continues.
func (c *driverClient) forwardPermissionRequest(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	requestID := newPermissionRequestID()
	toolCallID := string(params.ToolCall.ToolCallId)
	title := permissionTitle(params.ToolCall)
	logf("[permission] request %s tool_call=%s title=%q: forwarding for interactive approval (timeout=%s)", requestID, toolCallID, title, c.permissionTimeout)

	// pollCtx derives from ctx (the RequestPermission handler's own context,
	// which the acp-go-sdk cancels when the underlying connection/request is
	// torn down — see connection.go's per-request context) so neither the
	// delivery nor the poll can outlive the run itself, on top of our own
	// PERMISSION_TIMEOUT_SECONDS bound.
	pollCtx, cancel := context.WithTimeout(ctx, c.permissionTimeout)
	defer cancel()

	if err := c.emitter.EmitPermissionRequestSync(pollCtx, requestID, toolCallID, title, params.Options); err != nil {
		// The control plane never accepted the request event within the
		// permission budget: nobody can possibly approve a request they never
		// saw, so this is the timeout-deny path WITHOUT polling (polling for
		// an unacknowledged request_id could only ever burn the same
		// remaining budget on 204s).
		logf("[permission] request %s: could not deliver agent.permission_request (%v); timeout-deny without polling", requestID, err)
		return c.timeoutDenyPermission(requestID, params.Options), nil
	}

	optionID, decided := c.cp.waitForPermissionDecision(pollCtx, requestID)

	if decided && optionOffered(params.Options, optionID) {
		logf("[permission] request %s resolved by user: option=%s", requestID, optionID)
		c.emitter.EmitPermissionResolved(requestID, optionID, "user")
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(optionID)},
		}}, nil
	}
	if decided {
		// The decision endpoint returned an option_id that was not one of the
		// options WE offered for THIS request — never trust it blindly (it
		// could be stale/cross-request). Fall through to the same
		// timeout-deny handling as a genuine timeout.
		logf("[permission] request %s: orchestrator returned unrecognized option_id %q; treating as timeout-deny", requestID, optionID)
	} else {
		logf("[permission] request %s: no decision within %s (timeout, or the request was invalidated); defaulting to a deny-safe outcome", requestID, c.permissionTimeout)
	}
	return c.timeoutDenyPermission(requestID, params.Options), nil
}

// timeoutDenyPermission resolves a forwarded request as resolution="timeout"
// and builds the DENY-SAFE ACP outcome: a reject-KIND option when one was
// offered, else ACP's Cancelled outcome. It must never select an allow_*
// option — option lists are not guaranteed to contain a reject choice, and
// falling back to "any option" (e.g. the last one) could silently turn
// timeout-DENY into timeout-ALLOW (fail-open) on an allow-only list.
func (c *driverClient) timeoutDenyPermission(requestID string, options []acp.PermissionOption) acp.RequestPermissionResponse {
	reject := pickRejectOption(options)
	if reject == nil {
		// No reject-kind option (allow-only, or zero options): the only safe
		// refusal is the Cancelled outcome — jcode treats it as "permission
		// not granted" and handles the refusal in its own turn logic, exactly
		// like a rejected option, without anything being allowed.
		logf("[permission] request %s: timeout-deny with no reject-kind option offered; answering Cancelled (safe refusal, never an allow)", requestID)
		c.emitter.EmitPermissionResolved(requestID, "", "timeout")
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
			Cancelled: &acp.RequestPermissionOutcomeCancelled{},
		}}
	}
	logf("[permission] request %s: timeout-deny selecting option=%s (%s)", requestID, reject.OptionId, reject.Kind)
	c.emitter.EmitPermissionResolved(requestID, string(reject.OptionId), "timeout")
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Selected: &acp.RequestPermissionOutcomeSelected{OptionId: reject.OptionId},
	}}
}

// permissionTitle picks the human-readable action description for the
// agent.permission_request event's "title" field: jcode's tool call title,
// falling back to its kind, falling back to the raw tool_call_id so the
// field is never empty.
func permissionTitle(tc acp.ToolCallUpdate) string {
	if tc.Title != nil && *tc.Title != "" {
		return *tc.Title
	}
	if tc.Kind != nil && *tc.Kind != "" {
		return string(*tc.Kind)
	}
	return string(tc.ToolCallId)
}

// optionOffered reports whether optionID is one of the options THIS request
// actually offered — a defensive check against a decision response that
// (due to a control-plane bug, or a decision for a different/stale request)
// names an option this call never presented to jcode.
func optionOffered(options []acp.PermissionOption, optionID string) bool {
	for _, o := range options {
		if string(o.OptionId) == optionID {
			return true
		}
	}
	return false
}

// pickRejectOption chooses the timeout-deny option: a "reject"/"deny"-kind
// option if one was offered (least-privilege default), else nil — in which
// case the caller (timeoutDenyPermission) answers with the Cancelled outcome
// rather than gambling on any non-reject option, which could be an allow_*
// kind (fail-open).
func pickRejectOption(options []acp.PermissionOption) *acp.PermissionOption {
	for i := range options {
		k := strings.ToLower(string(options[i].Kind))
		if strings.Contains(k, "reject") || strings.Contains(k, "deny") {
			return &options[i]
		}
	}
	return nil
}

// newPermissionRequestID generates the request_id acpdrive owns end to end
// (F8a contract: "<acpdrive 生成的 uuid>") — a random UUIDv4, stdlib-only (no
// dependency worth adding for one id per permission request).
func newPermissionRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand practically never fails; this id is only ever used as a
		// tracing/idempotency key (never a security token), so degrade to a
		// still-unique-enough fallback rather than crashing the run over it.
		fmt.Fprintf(os.Stderr, "[acpdrive] warn: crypto/rand failed generating a permission request id: %v\n", err)
		return fmt.Sprintf("permreq-fallback-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
