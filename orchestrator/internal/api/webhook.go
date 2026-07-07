package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

// giteaIssueCommentPayload is the subset of Gitea's issue_comment webhook we
// consume (blueprint §8: only the fields we need). issue.pull_request is a JSON
// object when the issue IS a pull request, absent/null otherwise.
type giteaIssueCommentPayload struct {
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

// isPullRequest reports whether the commented issue is a pull request (Gitea
// sends a non-null pull_request object only for PRs).
func (p *giteaIssueCommentPayload) isPullRequest() bool {
	t := strings.TrimSpace(string(p.Issue.PullRequest))
	return t != "" && t != "null"
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
	if secret == "" || sigHex == "" {
		return false
	}
	want, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

// handleGiteaWebhook is the public Gitea webhook endpoint (POST /webhooks/gitea).
// It is registered only when WEBHOOK_SECRET is configured (else the route 404s).
// It authenticates the delivery by HMAC signature (401 on mismatch), then treats
// every non-matching event / non-command comment as a 200 no-op. A matching
// `@jcode …` PR comment creates a run on behalf of the mapped user (blueprint §8).
func (s *Server) handleGiteaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read webhook body")
		return
	}
	if int64(len(body)) > maxWebhookBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "webhook body exceeds the 1MiB limit")
		return
	}

	// HMAC gate — the ONLY hard failure. A bad/missing signature is a 401.
	if !validGiteaSignature(s.cfg.WebhookSecret, body, r.Header.Get("X-Gitea-Signature")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}

	// Everything past the signature is a 200 (no-op unless it is a real command),
	// so a redelivery / unrelated event never errors back to Gitea.
	if r.Header.Get("X-Gitea-Event") != "issue_comment" {
		writeWebhookOK(w, "ignored: not an issue_comment event")
		return
	}
	var p giteaIssueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		writeWebhookOK(w, "ignored: unparseable payload")
		return
	}
	if p.Action != "created" || !p.isPullRequest() {
		writeWebhookOK(w, "ignored: not a new pull-request comment")
		return
	}
	cmd := parseMentionCommand(p.Comment.Body)
	if cmd.kind == cmdNone {
		writeWebhookOK(w, "ignored: no @jcode command")
		return
	}

	// A real @jcode command: map identity, resolve the service, create the run.
	// All internal failures reply on the PR (best-effort) and still 200.
	s.processMention(r.Context(), &p, cmd)
	writeWebhookOK(w, "accepted")
}

// processMention handles a validated @jcode PR comment: de-dup, identity mapping
// (hard gate — no service-principal fallback), service resolution + RBAC, PR
// detail lookup, run creation, and the receipt reply. Every failure path replies
// a one-line reason on the PR via the gitea PAT and returns; nothing here is
// fatal to the HTTP handler.
func (s *Server) processMention(ctx context.Context, p *giteaIssueCommentPayload, cmd mentionCommand) {
	owner, repo, ok := provider.SplitRepo(p.Repository.FullName)
	if !ok {
		s.log.Warn("webhook: unparseable repository full_name", "repo", p.Repository.FullName)
		return
	}

	// A PAT-authenticated gitea client for PR detail + replies. The webhook uses
	// the PAT only for reads/receipts; the branch push (below) uses the triggering
	// user's OAuth token per §3. No PAT => we cannot even reply, so log + bail.
	patClient, ok := s.webhookGiteaClient(ctx)
	if !ok {
		s.log.Warn("webhook: no gitea PAT available; cannot process mention", "repo", p.Repository.FullName)
		return
	}
	reply := func(msg string) {
		if err := patClient.CreateIssueComment(ctx, owner, repo, p.Issue.Number, msg); err != nil {
			s.log.Warn("webhook: PR reply failed", "repo", p.Repository.FullName, "err", err)
		}
	}

	// De-dup: a redelivery of the same comment is a no-op.
	commentID := strconv.FormatInt(p.Comment.ID, 10)
	if _, err := s.st.GetRunByOriginCommentID(ctx, commentID); err == nil {
		s.log.Info("webhook: duplicate delivery ignored", "comment", commentID)
		return
	}

	// Identity mapping is a HARD GATE (blueprint §8): the commenter's gitea uid
	// must resolve to a jcloud user. No service-principal fallback on this path.
	uid := strconv.FormatInt(p.Comment.User.ID, 10)
	id, err := s.st.GetIdentity(ctx, domain.ProviderGitea, uid)
	if err != nil {
		reply("jcode couldn't find a jcloud account linked to this Gitea user. Sign in to the console with Gitea first.")
		return
	}
	user, err := s.st.GetUser(ctx, id.UserID)
	if err != nil {
		s.log.Warn("webhook: identity has no user", "identity", id.ID, "err", err)
		return
	}

	// Resolve the service: the repo's services where the commenter may run (§8).
	svc, ok := s.resolveWebhookService(ctx, user, p.Repository.FullName)
	if !ok {
		reply("jcode couldn't find a jcloud project you can run on for this repository.")
		return
	}

	// Guardrail: the project's provider_allowlist (when set) may forbid this
	// repository's provider. Reply visibly on the PR rather than starting a run
	// that policy disallows (fail-visible). A load error is reported as temporary.
	allowed, aerr := s.projectAllowsProvider(ctx, svc.ProjectID, svc.Provider)
	if aerr != nil {
		s.log.Error("webhook: load project guardrails", "repo", p.Repository.FullName, "err", aerr)
		reply("jcode hit a temporary internal problem — please try again shortly.")
		return
	}
	if !allowed {
		reply("jcode can't run here — this project's guardrails don't allow " + providerLabel(svc.Provider) + " repositories.")
		return
	}

	// Fail-visible gate (CLAUDE.md red line #1): if no LLM is configured, reply on
	// the PR explaining why — do NOT create a run that would fail headlessly. A
	// transient resolve error (DB blip, key rotation mid-flight) is a DIFFERENT
	// state: log it and report it as temporary so the user retries; never
	// misreport it as "not configured". Like every other reply on this path,
	// these replies are not de-duplicated across redeliveries (consistent with
	// the M7 reply behaviour).
	resolved, rerr := s.models.Resolve(ctx)
	if rerr != nil {
		s.log.Error("webhook: resolve model config", "repo", p.Repository.FullName, "err", rerr)
		reply("jcode hit a temporary internal problem — please try again shortly.")
		return
	}
	if !resolved.Configured() {
		reply("jcode can't run yet — " + modelcfg.NotConfiguredMessage(s.cfg.ConsoleURL))
		return
	}

	// PR detail (head/base branch + html_url) — the payload's issue omits them.
	pr, err := patClient.PRByNumber(ctx, owner, repo, p.Issue.Number)
	if err != nil || pr == nil || pr.HeadRef == "" {
		s.log.Warn("webhook: PR detail lookup failed", "repo", p.Repository.FullName, "pr", p.Issue.Number, "err", err)
		reply("jcode couldn't read this pull request from Gitea.")
		return
	}

	run := newWebhookRun(svc, user.ID, cmd, pr, p, commentID)
	if err := s.st.CreateRun(ctx, run); err != nil {
		// A concurrent redelivery that lost the de-dup pre-check trips the unique
		// origin_comment_id index here — treat it as the no-op it is.
		s.log.Warn("webhook: create run failed (likely duplicate)", "comment", commentID, "err", err)
		return
	}
	s.emitStatus(ctx, run)
	reply(fmt.Sprintf("🚀 jcode run started — %s/runs/%s", strings.TrimRight(s.cfg.ConsoleURL, "/"), run.ID))
	s.log.Info("webhook: created run", "run", run.ID, "kind", run.Kind, "service", svc.ID, "user", user.ID)
}

// webhookGiteaClient builds a gitea PR client authenticated with the fallback
// PAT (creds.Resolve with no user → the global GITEA_TOKEN). Used for the
// PR-detail read and the receipt reply only.
func (s *Server) webhookGiteaClient(ctx context.Context) (provider.Provider, bool) {
	if s.factory == nil || s.creds == nil {
		return nil, false
	}
	tok, err := s.creds.Resolve(ctx, domain.ProviderGitea, nil)
	if err != nil {
		return nil, false
	}
	prov, err := s.factory.PRClient(domain.ProviderGitea, tok.Value, tok.Scheme)
	if err != nil {
		return nil, false
	}
	return prov, true
}

// resolveWebhookService returns the first gitea service for repo full_name that
// the user may run on: a cluster-admin runs on any match; otherwise the user
// must be member+ on the service's project (viewer is not enough — running is a
// mutation). Returns ok=false when none match (blueprint §8).
func (s *Server) resolveWebhookService(ctx context.Context, user *domain.User, fullName string) (*domain.Service, bool) {
	svcs, err := s.st.ListServicesByRepo(ctx, domain.ProviderGitea, fullName)
	if err != nil {
		s.log.Warn("webhook: list services by repo", "repo", fullName, "err", err)
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
func newWebhookRun(svc *domain.Service, userID string, cmd mentionCommand, pr *provider.PR, p *giteaIssueCommentPayload, commentID string) *domain.Run {
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
		OriginCommentURL:  p.Comment.HTMLURL,
		PRURL:             pr.URL,
		PRNumber:          p.Issue.Number,
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

// writeWebhookOK returns a 200 with a short machine-readable status so Gitea's
// delivery log shows why an event was accepted / ignored.
func writeWebhookOK(w http.ResponseWriter, status string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}
