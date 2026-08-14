# 21 · GitHub-native code review

> Status: implementation contract (2026-07-28), updated 2026-08-12.
>
> Implementation reality update: the single Plugin webhook ingress serves GitHub,
> Gitea, and GitLab alike — PR lifecycle events dispatch `run_kind=review`
> Automations for all three providers (the Gitea-first limitation of docs/16 is
> superseded), the comment-command grammar runs for every provider, and the
> review source bundle fetches each provider's synthetic PR/MR head ref
> (`refs/pull/<n>/head` for GitHub/Gitea, `refs/merge-requests/<iid>/head` for
> GitLab) so fork pull requests are reviewable everywhere. Gitea/GitLab
> writeback still uses the top-level summary renderer (no batch inline review).
>
> Scope: GitHub App review entry points, review quality, repeatable mentions,
> automation setup, structured output, and GitHub-native writeback.
>
> This supersedes the GitHub limitation in
> `docs/16-repository-navigation-and-pr-automation.md`. It reuses the shared
> plugin webhook, App installation credentials, Run reconciler, and runner
> instead of creating another review service.

## 0. Outcome

A project owner connects a GitHub repository once and enables useful PR reviews
without learning webhook mechanics or writing an agent prompt. A reviewer can
also ask the installed App directly:

```text
@jcode-cloud-app review
@jcode-cloud-app review security and concurrency
@jcode-cloud-app full review
```

`@jcode review` remains a compatibility alias. The Console presents and copies
the exact comment command observed for the installed App. GitHub does **not**
put arbitrary GitHub Apps in the `@` autocomplete menu, so this is a literal
webhook command, not a linked user mention. A future true-autocomplete alias
requires a separate GitHub user or organization identity.

Every accepted manual request receives an eyes reaction, creates a
`RunKindReview`, and reviews the current head with repository context. Manual
commands retain that lightweight acknowledgement and do not create a status
comment. An accepted automatic PR event from an enabled review Automation
creates or updates one mutable status comment for the Service and pull request;
the completed result is published separately as one non-blocking native
`COMMENT` review per Run. It has high-confidence inline findings or an explicit
clean result. A new manual command can repeat the review on the same head.
Missing identity, membership, model, runner, or writeback dependencies fail
visibly.

## 1. Product principles and research

The interaction combines the strongest product choices already proven
elsewhere:

- GitHub Copilot Code Review uses native review surfaces, automatic triggers,
  and a non-blocking `COMMENT` result that leaves merge authority with people.
- Claude Code's review plugin reads repository instructions and changed code,
  independently verifies findings, and filters by confidence.
- CodeRabbit lets automatic and repeatable mention-based review coexist, and
  distinguishes normal follow-up from explicit full review.

References:

- <https://docs.github.com/en/copilot/concepts/agents/code-review>
- <https://docs.github.com/en/copilot/how-tos/use-copilot-agents/request-a-code-review/use-code-review>
- <https://docs.github.com/en/copilot/concepts/agents/about-third-party-coding-agents>
- <https://github.com/anthropics/claude-code/blob/main/plugins/code-review/README.md>
- <https://github.com/anthropics/claude-code-action/blob/main/docs/solutions.md>
- <https://docs.coderabbit.ai/configuration/auto-review>
- <https://docs.coderabbit.ai/reference/review-commands>
- <https://docs.github.com/en/rest/pulls/reviews>
- <https://docs.github.com/en/webhooks/webhook-events-and-payloads>

The resulting jcode personality is restrained and trustworthy: acknowledge
lightly, comment only when actionable, keep configuration in Cloud, and keep
the review conversation on GitHub.

## 2. Entry points and information architecture

### 2.1 One-click setup

The Automations page has two entries:

1. **Review pull requests** is primary and opens a purpose-built setup.
2. **Custom automation** preserves the existing advanced event/prompt editor.

Review setup contains only decisions that change behavior:

- repository service;
- model and effort;
- automatic events (`ready for review`, `new commits`);
- draft policy, off by default;
- optional repository-specific focus.

It never asks for a webhook URL, event family, provider action, Run kind, or
prompt template. Saving creates a plugin Automation with `run_kind=review`.

Unavailable dependencies show one corrective action beside the disabled
primary action: install the App, connect a repository, configure a model, or
update App permissions. After setup, the row shows policy, repository, model,
the copyable comment command, and last Run outcome or actionable error. This page
does not become a second PR dashboard.

### 2.2 GitHub interaction

Supported grammar is deliberately small and case-insensitive:

```ebnf
command-prefix = "@" app-slug | "@jcode" ;
command = command-prefix, whitespace,
          [ "full", whitespace ], "review",
          [ whitespace, focus ] ;
```

Normal and focused review inspect the current head. `full review` explicitly
evaluates the complete base-to-head diff. Every new comment is a new request.
Delivery IDs remain idempotent while comment IDs preserve repeatability.

Selecting the App in GitHub's reviewer picker is a compatible future entry
point, but it is not claimed at launch: GitHub does not provide the same
review-request webhook semantics for every App installation. Literal comment
commands and automatic PR-event triggers are the verified contract. The setup
UI must not call the command a native mention or imply GitHub autocomplete.

## 3. Review quality contract

The runner receives the checkout, base/head revisions, focused diff, and
optional user focus. It reads repository guidance (`AGENTS.md`, `CLAUDE.md`,
and nearby rules), inspects relevant callers/tests/types/history, runs
proportionate checks, and reviews changed behavior instead of merely changed
text. Diff and repository text are untrusted data, never instructions.

A finding is publishable only when it:

- describes an observable correctness, security, reliability, data-loss, or
  maintainability defect introduced by the PR;
- is actionable and explains the failure mode;
- anchors to a changed right-side line;
- has confidence of at least 80/100;
- is not a duplicate.

Style preferences, praise, speculative concerns, and test requests without a
behavioral risk are omitted. At most eight findings are published. Severity is
`P0` critical, `P1` merge-blocking, `P2` important, or `P3` bounded.

The model emits data, not ready-to-post prose:

```json
{
  "summary": "Validation is reversed for normal transfers.",
  "findings": [{
    "path": "ledger.py",
    "line": 7,
    "severity": "P1",
    "confidence": 99,
    "title": "Rejects valid transfers and permits overdrafts",
    "body": "The comparison now rejects amounts below the balance...",
    "suggestion": "if amount > balance:"
  }],
  "checks": ["Inspected transfer callers", "Ran unit tests"]
}
```

The orchestrator validates paths, lines, enums, confidence, sizes, count, and
duplicate anchors before storing or posting it. Invalid output fails visibly
and is never posted verbatim. Provider renderers escape model-authored Markdown
and HTML, neutralize `@mentions`, and size suggestion fences so result data
cannot break the surrounding comment structure. Escaped text has a bounded
provider-comment budget: every finding remains present with a visibly bounded
title/location and intact severity/confidence, while oversized prose is visibly
truncated and remains complete in jcode Cloud.

GitHub receives one batch review:

- event is always `COMMENT`;
- the top body leads with a short GitHub alert: neutral for no findings,
  important for validated findings, and warning when inline placement fails;
- the model conclusion is a separate Summary section, and detailed verification
  stays in a collapsed Checks section rather than turning the alert into a text
  wall;
- findings become inline comments with severity, title, explanation,
  confidence, and an optional suggestion;
- zero findings produces a concise explicit no-high-confidence-findings result.

Gitea and GitLab receive the same hierarchy using portable headings,
blockquotes, details, and separators, without GitHub-only alert syntax.

If GitHub rejects an inline anchor with `422`, jcode retries once as a top-level
review containing `path:line` locations and explains that inline placement was
unavailable. Other errors use reconciliation retries. Gitea/GitLab initially
use the top-level renderer until their adapters gain batch inline reviews.

### 3.1 Review status comment

Automatic review execution has one mutable status comment keyed by Service,
provider repository ID, and pull-request number. It is created only after an
enabled review Automation accepts a PR event and creates its Run. Manual
`@jcode ... review` commands continue to use the eyes reaction or actionable
error reply and never create or update this comment. The comment is a progress
surface, not the review result: the final native review described above remains
a separate provider object and is never replaced by an edit to the status
comment. Disabled Automations, excluded drafts, unmatched events, and failures
that prevent Run creation never post a misleading processing status; their
existing ignored/blocked execution reason remains the fail-visible record.

The status comment moves through this vocabulary:

- `queued`: the review Run exists and is waiting for a runner;
- `running`: the runner is reviewing the captured head revision;
- `publishing`: analysis succeeded and native review delivery is in progress;
- `completed`: the separate native review was delivered;
- `failed`: review execution or delivery ended without a native review;
- `canceled`: an operator canceled the Run;
- `superseded`: a newer head replaced this review attempt.

`completed`, `failed`, `canceled`, and `superseded` are terminal for one attempt.
A later accepted automatic Run for the same Service and PR reuses the same
provider comment and moves it back to `queued`, while recording the new Run and
head. Each body identifies the captured revision, links to the Cloud Run when a
safe link is available, and reports aggregate plan coverage after planning. It
never claims completion before the separate native review is delivered.

GitHub status bodies use the appropriate native alert (`NOTE`, `IMPORTANT`,
`TIP`, `WARNING`, or `CAUTION`). Gitea and GitLab use portable headings and
blockquotes without GitHub alert syntax. All Run-derived text is untrusted:
renderers escape Markdown and HTML, neutralize mentions, validate HTTP(S)
links, visibly truncate bounded text, and include the caller-supplied hidden
identity marker exactly once.

## 4. Architecture

```text
GitHub App webhook
  ├─ issue_comment(created)
  │    └─ parse installed App handle / @jcode compatibility command
  └─ pull_request(opened|ready_for_review|synchronize|reopened)
       └─ match enabled review Automations
            ↓
authorize actor + resolve Service/PR/model
            ↓
Create Run(kind=review, origin=webhook|automation)
       ├─ automatic PR event
       │    └─ upsert desired Service+PR status = queued
       │         └─ reconciler creates or updates one provider status comment
       │              └─ queued → running → publishing
       │                   → completed|failed|canceled|superseded
       └─ manual command: keep eyes reaction; do not create a status comment
       ↓
runner clones exact head and reviews base...head
       ↓
review-only submit_review MCP tool validates Plan anchors + completion receipt
and emits a tool-accepted REVIEW.json with a plan-bound receipt
            ↓
orchestrator validates and stores structured result
            ↓
reconciler posts GitHub batch COMMENT review with installation token
```

No long-lived repository token enters the runner. Existing short-lived clone
credential exchange remains the only checkout credential path.

### 4.1 Provider identity

`provider_configs.app_slug` stores public metadata observed from GitHub
`GET /app`. It is never user-entered and is cleared when App configuration
changes. Capabilities expose:

```json
{
  "mention_handle": "@jcode-cloud-app",
  "inline_pull_request_reviews": true
}
```

If discovery is unavailable, mention-trigger setup is visibly unavailable
while automatic review may remain usable.

### 4.2 Automation and normalized events

`automations_v2` adds `run_kind` (`agent|review`) and `include_drafts`.
Review mode is valid only for PR SCM events. Cron, push, issue, and comment
review Automations return `invalid_review_trigger`.

Defaults are `opened`, `ready_for_review`, `synchronize`, and `reopened`, with
drafts excluded. (`opened` covers a PR that is ready immediately;
`ready_for_review` covers a draft transition.) Automatic Runs are idempotent
per Automation + PR + head SHA and coalesce older queued heads. Manual comments
intentionally do not share that key.

Normalized events add PR-comment identity, draft state, comment ID, and actor
provider user ID. GitHub `issue_comment` is accepted only when
`issue.pull_request` exists; ordinary issue comments never create review Runs.

### 4.3 Persistence

Runs add nullable `review_result JSONB`. `review_output` remains for backward
compatibility. New runners upload `application/vnd.jcode.review+json`; the
domain exposes typed `ReviewResult`, and provider rendering happens only after
validation.

The review-status record is independent from the final Run delivery record. Its
unique identity is `(service_id, provider, provider_repo_id, pr_number)`, and it
stores the current Run/head plus desired and applied state/body hashes. A short
claim leases provider work across reconcilers. Provider comment ID and URL are
saved after creation so normal updates address the same comment; a stable hidden
marker allows recovery when provider creation succeeded but persistence of that
ID did not. Webhook delivery replay, concurrent reconcilers, and multiple
automatic actions for the same head therefore do not create additional status
comments. Manual repeat commands remain outside this lifecycle. A new automatic
head updates the existing comment and makes its Run the current owner. Late
updates from a displaced Run are fenced by `current_run_id` and cannot overwrite
the newer `queued`/`running` state. A genuinely current displaced attempt may
render `superseded`; neither path rewrites or removes already-published native
reviews.

For GitHub review Runs, source-bundle creation fetches the authenticated
synthetic `refs/pull/<number>/head` into the captured head branch before
bundling. Same-repository and fork PRs therefore receive the exact reviewed
commit without exposing an installation token to the runner. Writeback uses the
captured PR number instead of rediscovering a PR from a possibly fork-owned
branch.

As of 2026-08-03 the same applies to the other providers: Gitea exposes the
identical `refs/pull/<number>/head`, and GitLab review Runs fetch
`refs/merge-requests/<iid>/head`. A plain all-refs bundle never contains these
refs, so fork PR/MR review depends on this explicit fetch on every provider.

## 5. Security and abuse boundaries

- Verify HMAC and installation before parsing commands.
- Manual review requires a linked GitHub actor who belongs to the project.
- Automatic review is authorized by the owner who enabled the Automation.
- Match repositories by provider repository ID plus installation, never
  comment-supplied owner/name.
- Treat source, comments, and diffs as prompt-injection-capable untrusted data.
- Treat PR titles, Run IDs/errors, revision labels, and status links as
  untrusted renderer inputs; only the validated hidden marker is emitted raw.
- Give the runner no App key/token, model secret, or webhook secret.
- Coalesce synchronize bursts per repository and PR. When manual capacity is
  exhausted, reply with a visible busy state.

## 6. Tests designed before implementation

### Normalization and parsing

- discovered handle and `@jcode` work case-insensitively;
- normal, focused, and full review parse; incidental mentions do not;
- issue comments outside PRs are ignored;
- draft, comment ID, and actor identity normalize;
- duplicate delivery is ignored, but a new comment on the same head runs.

### Dispatch and authorization

- manual command creates one `RunKindReview` with current PR refs;
- unlinked/non-member actor and missing model reply actionably with no Run;
- automatic review creates `review`, never `agent`;
- drafts follow `include_drafts`;
- automatic same-head delivery is idempotent and newer heads coalesce;
- manual same-head comments remain repeatable.

### Validation and writeback

- valid clean result and up to eight unique findings pass;
- bad severity, confidence below 80, unsafe path/line, oversize fields, and
  duplicate anchors fail;
- one GitHub batch `COMMENT` review is posted;
- suggestions render safely;
- `422` falls back visibly to top-level locations;
- transient errors retry and unvalidated text is never posted.

### Status comment lifecycle

- the first accepted automatic PR-event Run creates one `queued` comment for
  its Service and PR;
- disabled review Automations, excluded drafts, unmatched events, and
  pre-Run failures create no status comment;
- manual review commands keep the eyes reaction/error reply and never create or
  mutate the status comment;
- `queued`, `running`, `publishing`, and each terminal state render for GitHub;
- Gitea/GitLab render the same facts without GitHub-only alert syntax;
- same-delivery replay, same-head automatic PR actions, and concurrent
  reconciliation reuse the same status comment;
- a new head reuses the comment for its new `queued` Run, and late terminal
  updates from the displaced Run cannot overwrite the new owner;
- provider-create success followed by persistence failure recovers by hidden
  marker instead of creating a duplicate;
- final review delivery failure never renders `completed`, and status-comment
  delivery failure never blocks review execution;
- PR title, Run/error text, links, and entity-encoded mentions cannot inject
  Markdown, HTML, alerts, links, or notifications;
- rendered bodies remain below the provider budget and disclose truncation.

### Console and end-to-end

- review setup is the primary entry, uses correct defaults, and saves
  `run_kind=review`;
- each missing dependency has one corrective action;
- exact App comment command is copyable, with the autocomplete limitation visible;
- all five supported locales and keyboard/narrow layouts work;
- the POC's reversed balance check produces one inline finding;
- after the fix, a repeated mention produces an explicit clean review;
- delivery, comment, Run, result, and GitHub review IDs remain traceable.

## 7. Acceptance criteria

Completion requires a real installed-App repository to prove:

1. setup needs no webhook URL, provider event name, Run kind, or prompt;
2. configured PR events automatically create review Runs;
3. `@jcode-cloud-app review` works repeatedly;
4. the POC defect lands on the correct changed line;
5. the corrected commit receives a clean review;
6. unauthorized mention and missing model fail visibly;
7. one status comment per Service and PR shows the latest attempt without
   replacing or duplicating its separate native reviews;
8. orchestrator, Console, migration, and public-path checks pass after deploy.

## 8. Design and architecture review

The first concept exposed automatic/manual/incremental/full modes, thresholds,
event matrices, prompt text, and commands together. Review found:

1. it taught infrastructure, so the primary action is now intent-based;
2. it presented `@jcode` like a native reference; the UI now calls the observed
   slug a copyable comment command and explains that custom Apps are not in
   GitHub's `@` autocomplete;
3. a reaction alone did not expose automatic review progress or failures, so
   automatic PR-event Runs now share one in-place status comment per Service
   and PR; manual commands keep the reaction, and every Run's
   confidence-filtered native review remains a separate result;
4. it blurred future work, so native reviewer selection and true
   previous-review incremental diffing stay unclaimed until verified.

Architecture review rejected a separate review webhook because it duplicated
signature verification, receipts, credentials, model resolution, and
reconciliation. The shared plugin webhook remains the single ingress;
explicit `run_kind=review` fixes the existing semantic gap.

## 9. Implementation review

The completed diff was reviewed independently against the acceptance and
failure-state contracts. Five defects were found and resolved before delivery:

1. manual dispatch initially overwrote the triggering comment URL with the PR
   URL; both are now retained in their distinct Run fields;
2. review writeback initially rediscovered a PR by branch, which is ambiguous
   for fork-owned heads; it now uses the authenticated webhook's PR number;
3. a normal bare clone does not contain GitHub's synthetic fork PR ref; source
   bundle creation now fetches `refs/pull/<number>/head` explicitly;
4. missing model/effort and queue failures were visible only in Cloud;
   manual mention requests now also receive an actionable GitHub reply;
5. the provider writeback functions had direct unit coverage but were omitted
   from the production reconciler `Tick`; `Tick` now drives draft-PR creation,
   existing-PR update pushes, and native review publication, with an
   entrypoint-level regression test.

The review also confirmed that duplicate deliveries remain idempotent, new
comment IDs remain repeatable, App installation credentials are minted outside
the runner, invalid structured output cannot reach provider rendering, and an
inline-anchor `422` remains visible through the top-level fallback.

Verification covers the complete orchestrator suite, Console tests/typecheck
and production build, all runner Go modules, a container-level source/review
journey, and the PostgreSQL-gated migration/store suite on an ephemeral
database. The installed-App POC repository is the final deployment gate. That
gate also verifies the runner and plugin-runtime image pins, rather than
assuming an orchestrator/Console rollout upgraded the task execution plane.
