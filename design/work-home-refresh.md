# Work Home refresh — design proposal

Status: unapproved, interactive prototype only. All task records are fixtures.

## Verification cases (before implementation)

- At 1440×900 the composer and recent tasks are visible together.
- At 390×844 there is no horizontal overflow; navigation can open and close.
- Switching repositories updates both task scope and composer context.
- Search and status filters compose; no matches differs from an empty repository.
- A blank prompt or unavailable model disables submission with an explanation.
- Submission previews the request and never claims a real task was created.
- Keyboard focus, Escape dismissal, attachment removal and task detail work.
- All repositories opens a searchable account repository picker, never the old
  Project page. Selection updates the composer and task list without losing the
  draft. Verify no-match, Escape, and mobile selection.

## Direction

Reduce the oversized promotional heading and repeated repository identity.
Use a quieter, narrower account conversation rail, one repository context strip,
a white composer, and a compact recent-task list. Keep the canonical application
and provider icons, warm neutral surfaces, and orange primary action.

Repository context applies to both composition and the task list. Conversations
remain account-scoped in the rail. Remote access and repository sections now
link to the unified `cloud-*.html` suite. Remote sessions remain distinct from
repository task records. See `cloud-suite.md` for the expanded page contract.

The navigation hierarchy is Account → Repository → Task. The rail labels are
Recent repositories and All repositories; Project is not a separate concept in
this home design. Remote devices sit outside the recent repository group.

## Implementation boundary

Current Console sources: `WorkHomePage.tsx`, `AccountRepositoryComposer`, and
`RepositoryWorkspace`. This is a layout and interaction proposal over their
existing contracts. Fixture task filters operate locally; production pagination
must not label a partial loaded set as an account-wide total. Model availability
must come from account model queries. The recovery destination remains
`/account/settings?section=models`; the prototype opens the corresponding
`cloud-settings-models.html` design without mutating account configuration.

Start task and row detail open the unified conversation page. New submissions
show the entered prompt and request context without fabricating a model reply.
No real API, task, credential change, or upload occurs. Files are held only in
page memory; request previews retain filenames. Drafts, repository context, and
composer choices use namespaced session storage. Branch and model choices are
fixtures, not live capability claims. Links target design pages, not production.

## Verification result — 2026-09-22

Passed in local headless Chrome through Playwright at 1440×900, 768×1024 and
390×844. Checked repository scope, combined status/search filtering, no-match
and empty-repository states, disabled submission, model recovery entry,
submission preview, attachment addition/removal, task preview, Escape dismissal
and mobile navigation. No page errors or failed HTTP responses. Desktop and
mobile screenshots were visually inspected. JavaScript syntax and Git whitespace
checks passed. No Console runtime source or production service was changed.

Repository terminology follow-up: verified the account repository picker on
desktop and mobile, search and no-match states, draft preservation, selection
sync with the composer/task list, and Escape dismissal. The All repositories
entry no longer links to `projects.html`.
