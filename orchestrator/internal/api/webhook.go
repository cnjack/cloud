package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provider"
)

// maxWebhookBytes caps a webhook body (blueprint §8: read once, ≤1MiB).
const maxWebhookBytes = 1 << 20

// mentionToken is the bot handle a PR comment must start with to trigger a run.
const mentionToken = "@jcode"

// issueCommentPayload is the subset of the GitHub-style issue_comment webhook we
// consume (blueprint §8; F13). BOTH gitea and github emit this exact shape for a
// comment on a PR conversation — issue.pull_request is a JSON object when the
// issue IS a pull request, absent/null otherwise — so the two receivers share one
// struct (their transports/signatures differ, the payload does not).
type issueCommentPayload struct {
	Action  string `json:"action"`
	Comment struct {
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			ID int64 `json:"id"`
		} `json:"user"`
	} `json:"comment"`
	Issue struct {
		Number      int             `json:"number"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// isPullRequest reports whether the commented issue is a pull request (gitea and
// github both send a non-null pull_request object only for PRs).
func (p *issueCommentPayload) isPullRequest() bool {
	t := strings.TrimSpace(string(p.Issue.PullRequest))
	return t != "" && t != "null"
}

// gitlabNotePayload is the subset of GitLab's "Note Hook" (a comment) we consume
// (F13). GitLab speaks merge-request vocabulary: the comment lives under
// object_attributes and the target MR under merge_request (iid, not number).
// noteable_type distinguishes an MR comment from an Issue/Commit/Snippet note.
type gitlabNotePayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		ID int64 `json:"id"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		ID           int64  `json:"id"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		URL          string `json:"url"`
	} `json:"object_attributes"`
	MergeRequest struct {
		IID int `json:"iid"`
	} `json:"merge_request"`
}

// giteaPullRequestPayload is the provider subset used by event-driven review
// Automations. Gitea sends lifecycle changes in pull_request and new commits in
// pull_request_sync; both carry the same PR shape.
type giteaPullRequestPayload struct {
	Action  string `json:"action"`
	Number  int    `json:"number"`
	Changes struct {
		Draft *struct {
			From bool `json:"from"`
		} `json:"draft,omitempty"`
	} `json:"changes,omitempty"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type webhookReviewEvent struct {
	provider     domain.GitProvider
	repoFullName string
	event        domain.AutomationEvent
	prNumber     int
	prURL        string
	headRef      string
	headSHA      string
	baseRef      string
	draft        bool
}

// webhookMention is the provider-neutral shape a PR/MR comment collapses to after
// each provider's payload parser runs. It carries exactly what processMention
// needs, so the dispatch core (de-dup → identity gate → service RBAC → host/model
// gates → PR read → run + receipt) is IDENTICAL across gitea/github/gitlab — only
// the transport (signature scheme, event header, payload field names) and the
// reply-credential source differ (blueprint §8 platformed to three providers).
type webhookMention struct {
	provider     domain.GitProvider
	repoFullName string // "owner/name": github full_name / gitlab path_with_namespace / gitea full_name
	prNumber     int    // PR number (gitea/github) or MR iid (gitlab)
	rawCommentID string // the provider's numeric comment id, unprefixed
	commentURL   string // the triggering comment's html_url (persisted as origin_comment_url)
	commenterUID string // the commenter's provider uid → user_identities lookup
	body         string // the raw comment body → parseMentionCommand
}

// originCommentKey builds the run's origin_comment_id (de-dup key). Gitea keeps
// the BARE numeric id for backward compatibility with M7 runs already stored that
// way; github/gitlab are PREFIXED with the provider so numerically-equal comment
// ids from different hosts (e.g. github note 42 vs gitlab note 42) never de-dup
// against each other, or against a gitea id.
func originCommentKey(prov domain.GitProvider, rawID string) string {
	if prov == domain.ProviderGitea {
		return rawID
	}
	return string(prov) + ":" + rawID
}

// providerDisplayName is the human name used in visible PR/MR replies.
func providerDisplayName(prov domain.GitProvider) string {
	switch prov {
	case domain.ProviderGitHub:
		return "GitHub"
	case domain.ProviderGitLab:
		return "GitLab"
	default:
		return "Gitea"
	}
}

// commandKind classifies a parsed @jcode mention.
type commandKind int

const (
	cmdNone   commandKind = iota // not an @jcode mention (ignore)
	cmdReview                    // "@jcode review"
	cmdTask                      // "@jcode <free text>"
)

// mentionCommand is the parsed intent of a PR comment.
type mentionCommand struct {
	kind commandKind
	task string // the task text for cmdTask
}

// parseMentionCommand is a PURE parser (table-driven tested): a comment triggers
// a run iff, after leading whitespace, it starts with "@jcode" (case-insensitive)
// followed by whitespace or end-of-string. "@jcode review" (exact, after trim) is
// a review command; any other non-empty remainder is an agent task. Anything else
// (no mention, "@jcoder…", bare "@jcode") is cmdNone.
func parseMentionCommand(body string) mentionCommand {
	s := strings.TrimLeft(body, " \t\r\n")
	if len(s) < len(mentionToken) || !strings.EqualFold(s[:len(mentionToken)], mentionToken) {
		return mentionCommand{kind: cmdNone}
	}
	rest := s[len(mentionToken):]
	// The char after the mention must be whitespace/end so "@jcoder" is not a hit.
	if rest != "" {
		switch rest[0] {
		case ' ', '\t', '\r', '\n':
		default:
			return mentionCommand{kind: cmdNone}
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return mentionCommand{kind: cmdNone}
	}
	if strings.EqualFold(rest, "review") {
		return mentionCommand{kind: cmdReview}
	}
	return mentionCommand{kind: cmdTask, task: rest}
}

// validGiteaSignature verifies X-Gitea-Signature = hex(HMAC-SHA256(body, secret))
// in constant time. An empty secret or signature, or a non-hex signature, fails.
func validGiteaSignature(secret string, body []byte, sigHex string) bool {
	return validHexHMAC(secret, body, strings.TrimSpace(sigHex))
}

// validGitHubSignature verifies X-Hub-Signature-256 = "sha256=" +
// hex(HMAC-SHA256(body, secret)) in constant time (F13). An empty secret/header,
// a missing "sha256=" prefix, or a non-hex digest fails.
func validGitHubSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return validHexHMAC(secret, body, header[len(prefix):])
}

// validHexHMAC is the shared core of the gitea/github signature checks: it
// compares sigHex against hex(HMAC-SHA256(body, secret)) in constant time.
func validHexHMAC(secret string, body []byte, sigHex string) bool {
	if secret == "" || sigHex == "" {
		return false
	}
	want, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

// validGitLabToken compares the X-Gitlab-Token header to the shared secret in
// constant time (GitLab does not sign the body — it echoes the token verbatim;
// F13). An empty secret or header fails.
func validGitLabToken(secret, token string) bool {
	if secret == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1
}

// readWebhookBody reads (and 1MiB-caps) a webhook body. It writes the error
// response itself and returns ok=false on a read error / oversize body so the
// three receivers share one intake path.
func readWebhookBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read webhook body")
		return nil, false
	}
	if int64(len(body)) > maxWebhookBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "webhook body exceeds the 1MiB limit")
		return nil, false
	}
	return body, true
}

// handleGiteaWebhook is the public Gitea webhook endpoint (POST /webhooks/gitea).
// It is registered only when WEBHOOK_SECRET is configured (else the route 404s).
// It authenticates the delivery by HMAC signature (401 on mismatch), then treats
// every non-matching event / non-command comment as a 200 no-op. A matching
// `@jcode …` PR comment creates a run on behalf of the mapped user (blueprint §8).
func (s *Server) handleGiteaWebhook(w http.ResponseWriter, r *http.Request) {
	body, ok := readWebhookBody(w, r)
	if !ok {
		return
	}
	// HMAC gate — the ONLY hard failure. A bad/missing signature is a 401.
	if !validGiteaSignature(s.cfg.WebhookSecret, body, r.Header.Get("X-Gitea-Signature")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	// Everything past the signature is a 200. Comment commands and persisted PR
	// review Automations share the receiver but retain independent authorization
	// and idempotency contracts.
	eventHeader := r.Header.Get("X-Gitea-Event")
	if eventHeader == "pull_request" || eventHeader == "pull_request_sync" {
		event, status := parseGiteaReviewEvent(eventHeader, body)
		if event == nil {
			writeWebhookOK(w, status)
			return
		}
		writeWebhookOK(w, s.processReviewEvent(r.Context(), event))
		return
	}
	if eventHeader != "issue_comment" && eventHeader != "pull_request_comment" {
		writeWebhookOK(w, "ignored: unsupported Gitea event")
		return
	}
	m, cmd, status := parseIssueCommentMention(domain.ProviderGitea, body)
	if m == nil {
		writeWebhookOK(w, status)
		return
	}
	s.processMention(r.Context(), m, cmd)
	writeWebhookOK(w, "accepted")
}

// parseGiteaReviewEvent normalizes documented Gitea PR event headers/actions.
// An edited PR is "ready" only with an explicit draft=true previous value; a
// title/body edit must never accidentally dispatch a review.
func parseGiteaReviewEvent(header string, body []byte) (*webhookReviewEvent, string) {
	var p giteaPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "ignored: unparseable pull-request payload"
	}
	var event domain.AutomationEvent
	switch {
	case header == "pull_request_sync" && p.Action == "synchronized":
		event = domain.AutomationEventSynchronize
	case header == "pull_request" && p.Action == "opened":
		event = domain.AutomationEventOpened
	case header == "pull_request" && p.Action == "reopened":
		event = domain.AutomationEventReopened
	case header == "pull_request" && p.Action == "edited" && !p.PullRequest.Draft && p.Changes.Draft != nil && p.Changes.Draft.From:
		event = domain.AutomationEventReady
	default:
		return nil, "ignored: PR action is not an Automation event"
	}
	if p.Repository.FullName == "" || p.Number == 0 || p.PullRequest.Head.Ref == "" ||
		p.PullRequest.Head.SHA == "" || p.PullRequest.Base.Ref == "" {
		return nil, "ignored: incomplete pull-request payload"
	}
	return &webhookReviewEvent{
		provider: domain.ProviderGitea, repoFullName: p.Repository.FullName, event: event,
		prNumber: p.Number, prURL: p.PullRequest.HTMLURL, headRef: p.PullRequest.Head.Ref,
		headSHA: p.PullRequest.Head.SHA, baseRef: p.PullRequest.Base.Ref, draft: p.PullRequest.Draft,
	}, ""
}

func automationAcceptsEvent(a *domain.Automation, event *webhookReviewEvent) bool {
	if !a.Enabled || a.TriggerType != domain.AutomationTriggerPRReview ||
		!strings.EqualFold(a.BaseBranch, event.baseRef) || (event.draft && !a.IncludeDrafts) {
		return false
	}
	for _, allowed := range a.Events {
		if allowed == event.event {
			return true
		}
	}
	return false
}

func automationEventKey(a *domain.Automation, event *webhookReviewEvent) string {
	raw := strings.Join([]string{a.ID, string(event.provider), event.repoFullName,
		strconv.Itoa(event.prNumber), event.headSHA}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// processReviewEvent matches one validated provider event against every
// Service/Automation tracking the repository. It never borrows the event actor
// as authorization: the saved Automation owner is the audit principal and the
// normal credential resolver prefers the Service's bound integration.
func (s *Server) processReviewEvent(ctx context.Context, event *webhookReviewEvent) string {
	services, err := s.st.ListServicesByRepo(ctx, event.provider, event.repoFullName)
	if err != nil {
		s.log.Error("automation webhook: list services", "repo", event.repoFullName, "err", err)
		return "accepted: state recording failed"
	}
	now := time.Now().UTC()
	matched := false
	dispatched := false
	duplicate := false
	duplicateServices := map[string]bool{}
	for i := range services {
		svc := &services[i]
		automations, err := s.st.ListAutomationsByService(ctx, svc.ID)
		if err != nil {
			_ = s.st.RecordWebhookDelivery(ctx, svc.ID, now, "error", "Could not load Automations for this delivery.")
			continue
		}
		serviceMatched := false
		for i := range automations {
			a := &automations[i]
			if !automationAcceptsEvent(a, event) {
				continue
			}
			matched = true
			serviceMatched = true
			key := automationEventKey(a, event)
			if _, err := s.st.GetRunByOriginEventKey(ctx, key); err == nil {
				duplicate = true
				duplicateServices[svc.ID] = true
				continue
			}
			sel, outcome, modelErr := s.models.SelectModel(ctx, svc.ProjectID, deref(svc.DefaultModelID), a.ModelID)
			if modelErr != nil || outcome != modelcfg.SelectOK {
				message := "Automation model is unavailable. Re-save the Automation after fixing model access."
				_ = s.st.RecordAutomationDispatch(ctx, a.ID, now, "", message)
				_ = s.st.RecordWebhookDelivery(ctx, svc.ID, now, "error", message)
				continue
			}
			run := &domain.Run{
				ID: domain.NewID(), ProjectID: svc.ProjectID, ServiceID: svc.ID,
				Prompt: a.Instructions, Status: domain.StatusQueued, Kind: domain.RunKindReview,
				Phase: "Queued", TriggeredByUserID: a.CreatedBy, Attempt: 1, CreatedAt: now,
				PRURL: event.prURL, PRNumber: event.prNumber, PRHeadBranch: event.headRef,
				PRBaseBranch: event.baseRef, Origin: domain.RunOriginAutomation,
				OriginAutomationID: a.ID, OriginEventKey: key, ModelName: sel.ModelName,
			}
			if sel.ModelID != "" {
				modelID := sel.ModelID
				run.ModelID = &modelID
			}
			if err := s.st.CreateRun(ctx, run); err != nil {
				if _, duplicateErr := s.st.GetRunByOriginEventKey(ctx, key); duplicateErr == nil {
					duplicate = true
					duplicateServices[svc.ID] = true
					continue
				}
				message := "The PR event was received, but the review Run could not be created."
				_ = s.st.RecordAutomationDispatch(ctx, a.ID, now, "", message)
				_ = s.st.RecordWebhookDelivery(ctx, svc.ID, now, "error", message)
				continue
			}
			s.emitStatus(ctx, run)
			_ = s.st.RecordAutomationDispatch(ctx, a.ID, now, run.ID, "")
			_ = s.st.RecordWebhookDelivery(ctx, svc.ID, now, "accepted", "")
			dispatched = true
			s.log.Info("automation webhook: created review run", "run", run.ID, "automation", a.ID, "repo", event.repoFullName, "pr", event.prNumber)
		}
		if !serviceMatched {
			_ = s.st.RecordWebhookDelivery(ctx, svc.ID, now, "ignored", "")
		}
	}
	if dispatched {
		return "accepted"
	}
	if duplicate {
		for serviceID := range duplicateServices {
			_ = s.st.RecordWebhookDelivery(ctx, serviceID, now, "duplicate", "")
		}
		return "accepted: duplicate"
	}
	if matched {
		return "accepted: dispatch failed"
	}
	return "ignored: no matching Automation"
}

// handleGitHubWebhook is the public GitHub webhook endpoint (POST /webhooks/github;
// F13). Same shape as the gitea receiver — the payload is byte-identical — but the
// delivery is authenticated by X-Hub-Signature-256 and the event header is
// X-GitHub-Event. Registered only when WEBHOOK_SECRET is configured.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, ok := readWebhookBody(w, r)
	if !ok {
		return
	}
	if !validGitHubSignature(s.cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "issue_comment" {
		writeWebhookOK(w, "ignored: not an issue_comment event")
		return
	}
	m, cmd, status := parseIssueCommentMention(domain.ProviderGitHub, body)
	if m == nil {
		writeWebhookOK(w, status)
		return
	}
	s.processMention(r.Context(), m, cmd)
	writeWebhookOK(w, "accepted")
}

// handleGitLabWebhook is the public GitLab webhook endpoint (POST /webhooks/gitlab;
// F13). GitLab does not sign the body — it echoes the shared secret in the
// X-Gitlab-Token header (constant-time compared). The trigger event is the "Note
// Hook" whose noteable_type is MergeRequest. Registered only when WEBHOOK_SECRET
// is configured.
func (s *Server) handleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	body, ok := readWebhookBody(w, r)
	if !ok {
		return
	}
	if !validGitLabToken(s.cfg.WebhookSecret, r.Header.Get("X-Gitlab-Token")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook token")
		return
	}
	if r.Header.Get("X-Gitlab-Event") != "Note Hook" {
		writeWebhookOK(w, "ignored: not a Note Hook event")
		return
	}
	var p gitlabNotePayload
	if err := json.Unmarshal(body, &p); err != nil {
		writeWebhookOK(w, "ignored: unparseable payload")
		return
	}
	// Only comments on a merge request trigger; an Issue/Commit/Snippet note or a
	// missing iid is a no-op.
	if !strings.EqualFold(p.ObjectAttributes.NoteableType, "MergeRequest") || p.MergeRequest.IID == 0 {
		writeWebhookOK(w, "ignored: not a merge-request comment")
		return
	}
	cmd := parseMentionCommand(p.ObjectAttributes.Note)
	if cmd.kind == cmdNone {
		writeWebhookOK(w, "ignored: no @jcode command")
		return
	}
	m := &webhookMention{
		provider:     domain.ProviderGitLab,
		repoFullName: p.Project.PathWithNamespace,
		prNumber:     p.MergeRequest.IID,
		rawCommentID: strconv.FormatInt(p.ObjectAttributes.ID, 10),
		commentURL:   p.ObjectAttributes.URL,
		commenterUID: strconv.FormatInt(p.User.ID, 10),
		body:         p.ObjectAttributes.Note,
	}
	s.processMention(r.Context(), m, cmd)
	writeWebhookOK(w, "accepted")
}

// parseIssueCommentMention decodes a GitHub-style issue_comment body (shared by
// gitea + github) into a webhookMention + parsed command. It returns m==nil with
// a human-readable status when the event is a no-op (unparseable / not a new PR
// comment / no @jcode command) so the caller can 200 with that status.
func parseIssueCommentMention(prov domain.GitProvider, body []byte) (*webhookMention, mentionCommand, string) {
	var p issueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, mentionCommand{}, "ignored: unparseable payload"
	}
	if p.Action != "created" || !p.isPullRequest() {
		return nil, mentionCommand{}, "ignored: not a new pull-request comment"
	}
	cmd := parseMentionCommand(p.Comment.Body)
	if cmd.kind == cmdNone {
		return nil, mentionCommand{}, "ignored: no @jcode command"
	}
	m := &webhookMention{
		provider:     prov,
		repoFullName: p.Repository.FullName,
		prNumber:     p.Issue.Number,
		rawCommentID: strconv.FormatInt(p.Comment.ID, 10),
		commentURL:   p.Comment.HTMLURL,
		commenterUID: strconv.FormatInt(p.Comment.User.ID, 10),
		body:         p.Comment.Body,
	}
	return m, cmd, ""
}

// processMention handles a validated @jcode PR/MR comment for ANY provider: it
// resolves a reply client, de-dups, maps identity (hard gate — no service-
// principal fallback), resolves the service + RBAC, applies the host/model gates,
// reads the PR detail, and creates the run + posts the receipt. Every failure
// path either replies a one-line reason on the PR/MR (fail-visible) or logs and
// ignores (when it cannot even reply); nothing here is fatal to the HTTP handler.
func (s *Server) processMention(ctx context.Context, m *webhookMention, cmd mentionCommand) {
	owner, repo, ok := provider.SplitRepo(m.repoFullName)
	if !ok {
		s.log.Warn("webhook: unparseable repository full_name", "provider", m.provider, "repo", m.repoFullName)
		return
	}

	// A client authenticated to READ the PR and POST replies. For gitea this is the
	// global PAT (repo-independent, so it can even reply "no project"); for
	// github/gitlab there is no cluster PAT, so it is the bot token of an
	// integration bound to a service on this repo. No client => we cannot even
	// reply: log + ignore (an honest degrade, never a silent fake success).
	client, ok := s.webhookReplyClient(ctx, m.provider, m.repoFullName)
	if !ok {
		s.log.Warn("webhook: no credential to reply/read; cannot process mention", "provider", m.provider, "repo", m.repoFullName)
		return
	}
	reply := func(msg string) {
		if err := client.CreateIssueComment(ctx, owner, repo, m.prNumber, msg); err != nil {
			s.log.Warn("webhook: PR reply failed", "provider", m.provider, "repo", m.repoFullName, "err", err)
		}
	}

	// De-dup: a redelivery of the same comment is a no-op.
	commentID := originCommentKey(m.provider, m.rawCommentID)
	if _, err := s.st.GetRunByOriginCommentID(ctx, commentID); err == nil {
		s.log.Info("webhook: duplicate delivery ignored", "comment", commentID)
		return
	}

	// Identity mapping is a HARD GATE (blueprint §8): the commenter's provider uid
	// must resolve to a jcloud user. No service-principal fallback on this path.
	id, err := s.st.GetIdentity(ctx, m.provider, m.commenterUID)
	if err != nil {
		name := providerDisplayName(m.provider)
		reply("jcode couldn't find a jcloud account linked to this " + name + " user. Sign in to the console with " + name + " first.")
		return
	}
	user, err := s.st.GetUser(ctx, id.UserID)
	if err != nil {
		s.log.Warn("webhook: identity has no user", "identity", id.ID, "err", err)
		return
	}

	// Resolve the service: the repo's services where the commenter may run (§8).
	svc, ok := s.resolveWebhookService(ctx, m.provider, user, m.repoFullName)
	if !ok {
		reply("jcode couldn't find a jcloud project you can run on for this repository.")
		return
	}

	// Dispatch-time integration host gate (D20 / F5 adjudication A): a tightened
	// cluster allowlist stops runs on an integration-bound service immediately.
	// Reply visibly on the PR (fail-visible) rather than dispatching a run that
	// quietly ignores policy; a load error is reported as temporary.
	if allowed, host, herr := s.integrationHostStillAllowed(ctx, svc); herr != nil {
		s.log.Error("webhook: check integration host policy", "repo", m.repoFullName, "err", herr)
		reply("jcode hit a temporary internal problem — please try again shortly.")
		return
	} else if !allowed {
		reply("jcode can't run here — this service's git integration targets '" + host +
			"', which is not in the cluster's allowed git hosts.")
		return
	}

	// Fail-visible gate (CLAUDE.md red line #1): if no LLM is configured, reply on
	// the PR explaining why — do NOT create a run that would fail headlessly. A
	// transient resolve error (DB blip, key rotation mid-flight) is a DIFFERENT
	// state: log it and report it as temporary so the user retries; never
	// misreport it as "not configured". Like every other reply on this path,
	// these replies are not de-duplicated across redeliveries (consistent with
	// the M7 reply behaviour).
	sel, outcome, rerr := s.models.SelectModel(ctx, svc.ProjectID, deref(svc.DefaultModelID), "")
	if rerr != nil {
		s.log.Error("webhook: resolve model config", "repo", m.repoFullName, "err", rerr)
		reply("jcode hit a temporary internal problem — please try again shortly.")
		return
	}
	switch outcome {
	case modelcfg.SelectOK:
		// proceed
	case modelcfg.SelectNotSelected:
		// Several models are granted but the service has no default — a headless
		// mention can't pick, so tell the project owner to set a service default.
		reply("jcode can't run yet — " + modelcfg.NotSelectedMessage())
		return
	default: // SelectNotConfigured (NotGranted can't occur — no model is requested)
		reply("jcode can't run yet — " + modelcfg.NotConfiguredMessage(s.cfg.ConsoleURL))
		return
	}

	// PR detail (head/base branch + html_url) — the payload's issue omits them.
	pr, err := client.PRByNumber(ctx, owner, repo, m.prNumber)
	if err != nil || pr == nil || pr.HeadRef == "" {
		s.log.Warn("webhook: PR detail lookup failed", "provider", m.provider, "repo", m.repoFullName, "pr", m.prNumber, "err", err)
		reply("jcode couldn't read this pull request from " + providerDisplayName(m.provider) + ".")
		return
	}

	run := newWebhookRun(svc, user.ID, cmd, pr, m, commentID)
	run.ModelName = sel.ModelName
	if sel.ModelID != "" {
		run.ModelID = &sel.ModelID
	}
	if err := s.st.CreateRun(ctx, run); err != nil {
		// A concurrent redelivery that lost the de-dup pre-check trips the unique
		// origin_comment_id index here — treat it as the no-op it is.
		s.log.Warn("webhook: create run failed (likely duplicate)", "comment", commentID, "err", err)
		return
	}
	s.emitStatus(ctx, run)
	reply(fmt.Sprintf("🚀 jcode run started — %s/runs/%s", strings.TrimRight(s.cfg.ConsoleURL, "/"), run.ID))
	s.log.Info("webhook: created run", "run", run.ID, "kind", run.Kind, "provider", m.provider, "service", svc.ID, "user", user.ID)
}

// webhookReplyClient builds a Provider able to READ the PR/MR and POST replies on
// repo `fullName` for provider `prov`, plus whether one is available.
//
//   - gitea: the global admin PAT (creds.Resolve with no user → GITEA_TOKEN),
//     repo-independent so it can reply even before a service is resolved (M7).
//   - github/gitlab: there is no cluster-wide PAT, so the bot credential is taken
//     from an integration bound to a service on this repo (the first service that
//     yields a resolvable credential). No service / no integration credential ⇒
//     ok=false, and the caller log+ignores (it cannot even reply). This mirrors
//     the run path (review.go / prState), which also builds GH/GL clients via the
//     Factory against the public host.
func (s *Server) webhookReplyClient(ctx context.Context, prov domain.GitProvider, fullName string) (provider.Provider, bool) {
	if s.factory == nil || s.creds == nil {
		return nil, false
	}
	if prov == domain.ProviderGitea {
		tok, err := s.creds.Resolve(ctx, domain.ProviderGitea, nil)
		if err != nil {
			return nil, false
		}
		p, err := s.factory.PRClient(domain.ProviderGitea, tok.Value, tok.Scheme)
		if err != nil {
			return nil, false
		}
		return p, true
	}
	svcs, err := s.st.ListServicesByRepo(ctx, prov, fullName)
	if err != nil {
		s.log.Warn("webhook: list services by repo (reply client)", "provider", prov, "repo", fullName, "err", err)
		return nil, false
	}
	for i := range svcs {
		tok, err := s.creds.ResolveForService(ctx, &svcs[i], nil)
		if err != nil {
			continue
		}
		p, err := s.factory.PRClient(prov, tok.Value, tok.Scheme)
		if err != nil {
			continue
		}
		return p, true
	}
	return nil, false
}

// resolveWebhookService returns the first service (for provider prov) tracking
// repo full_name that the user may run on: a cluster-admin runs on any match;
// otherwise the user must be member+ on the service's project (viewer is not
// enough — running is a mutation). Returns ok=false when none match (blueprint §8).
func (s *Server) resolveWebhookService(ctx context.Context, prov domain.GitProvider, user *domain.User, fullName string) (*domain.Service, bool) {
	svcs, err := s.st.ListServicesByRepo(ctx, prov, fullName)
	if err != nil {
		s.log.Warn("webhook: list services by repo", "provider", prov, "repo", fullName, "err", err)
		return nil, false
	}
	for i := range svcs {
		svc := svcs[i]
		if user.IsClusterAdmin {
			return &svc, true
		}
		m, err := s.st.GetMember(ctx, svc.ProjectID, user.ID)
		if err == nil && m.Role.AtLeast(domain.RoleMember) {
			return &svc, true
		}
	}
	return nil, false
}

// newWebhookRun builds the queued run a mention triggers. A review command makes
// a kind=review run (posts a review comment); a task command makes a kind=agent
// run whose baseline IS the PR head branch and which pushes back to it (§8). Both
// pre-fill the existing PR (pr_url/pr_number) and the head/base refs, and carry
// the webhook origin + triggering comment.
func newWebhookRun(svc *domain.Service, userID string, cmd mentionCommand, pr *provider.PR, m *webhookMention, commentID string) *domain.Run {
	uid := userID
	run := &domain.Run{
		ID:                domain.NewID(),
		ProjectID:         svc.ProjectID,
		ServiceID:         svc.ID,
		Status:            domain.StatusQueued,
		Phase:             "Queued",
		TriggeredByUserID: &uid,
		Attempt:           1,
		CreatedAt:         time.Now().UTC(),
		Origin:            domain.RunOriginWebhook,
		OriginCommentID:   commentID,
		OriginCommentURL:  m.commentURL,
		PRURL:             pr.URL,
		PRNumber:          m.prNumber,
		PRHeadBranch:      pr.HeadRef,
		PRBaseBranch:      pr.BaseRef,
	}
	if cmd.kind == cmdReview {
		run.Kind = domain.RunKindReview
		run.Prompt = "AI review of PR " + pr.URL
	} else {
		run.Kind = domain.RunKindAgent
		run.Prompt = cmd.task
	}
	return run
}

// writeWebhookOK returns a 200 with a short machine-readable status so the
// provider's delivery log shows why an event was accepted / ignored.
func writeWebhookOK(w http.ResponseWriter, status string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}
