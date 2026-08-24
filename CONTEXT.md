# jcode Cloud domain context

This glossary records the product language shared by the control plane, Runner,
Console, and design documents. It is intentionally small: implementation detail
belongs next to the implementation and accepted decisions belong in
`docs/02-decision-log.md`.

## Core terms

- **Work Item** — durable team work. A jtype Card remains the truth for Kanban
  work; a provider Pull Request remains the truth for review work. Cloud does not
  create a second issue system.
- **Repository** — the only user-visible source and execution target in the
  personal product. Project may remain a hidden singleton policy boundary and a
  legacy Service row may back the first implementation, but neither leaks into
  new UI or Repository execution contracts. Temporary hidden-container
  administration endpoints are migration plumbing, not product concepts.
- **Agent Board** — the one existing JType Board optionally connected to a
  Repository. A Board can belong to only one Repository. JType remains the Work
  Item truth; Cloud stores only policy, occurrence, Run, and receipt state.
- **Repository Agent Workflow** — the durable Repository policy that binds an
  Agent Board, columns, fixed Automation model/effort, execution account, Agent
  Profile, execution target, and Delivery behavior. Card transitions and the
  Card's Run-with-agent action are adapters into the same occurrence interface.
- **Trigger** — the fact that requests execution: Manual, JType transition, SCM
  event/comment, Cron occurrence, or scoped API call. Trigger implementations
  differ, but all must produce the same bounded trigger facts before dispatch.
- **Workflow Definition** — an editable, versioned authoring artifact that binds
  a trigger shape, Agent Profile, requirements, timeout, typed outputs, and prompt.
  Ship-R1 has code-owned built-in definitions; repository/UI authoring arrives in
  Ship-R2. A definition is compiled into a Run-specific Workflow Contract.
- **Agent Profile** — a versioned role contract describing how an agent should
  behave and which capabilities it requires. Built-in roles are Developer,
  Reviewer, Product Manager, and Architect; a Custom profile may refine the
  instructions and requirements. A profile is not a human identity and does not
  grant authorization.
- **LLM Selection** — the resolved provider/model/effort projection frozen for a
  Run. Ship-R1 does not introduce a separate LLM Profile resource; that remains
  distinct from Agent Profile and Runner Profile even when selected from current
  Project/model settings.
- **Workflow Contract** — the immutable, schema-versioned execution contract
  compiled for one Run from a Workflow Definition revision, bounded trigger
  facts, an Agent Profile revision, Repository policy, LLM Selection, typed delivery
  outputs, and verification rules. Editing a profile or Automation never mutates
  an existing Run's contract.
- **Run Readiness** — a server-side evaluation of a proposed Workflow Contract
  against model, repository, Plugin, Runner, persistence, and delivery
  capabilities. Ship-R1 keeps existing dispatch prerequisites and records the
  resolved contract; Ship-R2 exposes the shared preview/create evaluator. A
  failed check never silently selects another profile or output.
- **Run** — one execution truth in Cloud: lifecycle, transcript, artifacts,
  result, delivery, usage, and frozen Workflow Contract.
- **Run SCM Grant** — the immutable repository and provider authorization facts
  for a Run: installation/grant revision, provider configuration revision,
  repository identity, clone route, default branch, and acting provider identity.
  Clone, push, Pull Request, and review writeback consume the same grant.
- **Review Revision Pair** — the provider-verified base/head commit SHAs frozen on
  a Review Run. The Review Plan may additionally record the computed merge-base;
  revision facts are not authorization and therefore are not part of SCM Grant.
- **Runner Profile** — an operator-owned manifest for a Runner image digest and
  its executable/tool/network/persistence capabilities. It is infrastructure,
  not an Agent persona.
- **Review Plan** — a deterministic plan pinned to base/head commits, with a
  changed-hunk index, bounded review units, rules revision, and input coverage
  ledger. Ship-R1 coverage proves what entered the reviewer, not what the model
  semantically considered.
- **Typed Output** — an allowlisted intent produced by a Run and externalized by
  a control-plane adapter, such as `provider_review` or `create_pull_request`.
  Agent tool permission never grants an output absent from the contract.
- **Delivery** — the control-plane-owned publication of a Run result. Pull
  Request delivery is lifecycle-aware by default: a one-shot success is Ready;
  a long session may use Draft only as an intermediate state. Cloud never
  approves or merges automatically.
- **Workspace Checkpoint** — an append-only reference to a stable Run workspace
  state. A PVC is a cache and resume substrate, not the checkpoint truth.
- **Conversation** — the ordered Run/session timeline represented by
  `jcode-ui-core` runtime types and rendered by `jcode-ui`. Desktop and Cloud are
  transport adapters around this shared UI, not independent renderers.

## Invariants

1. Trigger identity, accountable identity, Agent Profile, Runner Profile, and
   provider Bot identity are distinct concepts.
2. Display-only provenance never participates in authorization.
3. A Run executes one frozen Workflow Contract; retries explicitly choose
   whether to reuse it or resolve a new revision.
4. Provider credentials never enter the Runner. A Run only receives scoped
   source and credential adapters from the control plane.
5. Different trigger implementations may collect different facts, but they may
   not bypass platform dispatch prerequisites or invent private dispatch paths.
6. Multi-agent execution is opt-in orchestration over isolated review/work
   units. A role profile does not imply multi-agent execution.
7. An Agent Board occurrence is the single dispatch seam. A Card transition and
   Run-with-agent may not create separate Run paths.
8. Account last-used model state belongs to interactive composition; unattended
   Repository Agent Workflows use their persisted model and execution account.
