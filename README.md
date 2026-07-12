<div align="center">

<h1 align="center">
  <span style="font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Monaco, monospace; font-size: 32px; font-weight: 700;">
    [<span style="color: #FF8400;">J</span>CLOUD]
  </span>
</h1>

### **Give your codebase a job.**

**The shared cloud workspace for jcode coding agents.**

Describe the outcome in plain language. jcode Cloud works in the services your
team connects, shows its progress as a conversation, and gives you a diff or
pull request to review when it is ready.

[Get started](#get-started) · [How it works](#how-it-works) · [Automations](#automations-and-kanban) · [Need help](#need-help)

</div>

---

## Why jcode Cloud?

| | |
| --- | --- |
| **Projects, not loose prompts** | Group related repositories, paths, people, models, and task history in one workspace. |
| **See the work happen** | Follow the agent in a chat-like task timeline, including messages, tool activity, approvals, and results. |
| **Keep people in control** | Choose full access or request approval before actions; inspect the diff and pull request before you merge. |
| **Work where your team works** | Connect provider repositories—Gitea first, plus GitHub and GitLab—or use a path or remote URL. |
| **Automate repeatable work** | Run scheduled tasks for a service and use linked Kanban boards when your Project is configured for them. |
| **Honest status** | Missing models, unavailable boards, failed schedules, and unverified provider events are shown clearly instead of being silently simulated. |

> jcode Cloud is for people using an already deployed workspace. Your
> organization controls the available repositories, models, and integrations.

## Get started

1. **Sign in** to the jcode Cloud URL provided by your organization.
2. Open **Projects**, then choose an existing Project or create one if you are
   a Project owner.
3. Select a service from the left rail. A service is usually a repository, but
   it can also be a path or remote URL.
4. On the **Tasks** tab, describe the result you want in the **New task**
   composer.
5. Select a model when your Project offers a choice, choose the appropriate
   permission mode, and select **Send**.
6. Follow the task until it is ready to review, then inspect the diff or open
   the related pull request or merge request.

If a Project has no service or no available model, the console tells you what
is missing and who can resolve it.

### A good first request

Be clear about the outcome, constraints, and definition of done.

```text
Add CSV export to the analytics dashboard. Export the currently filtered rows,
keep the existing date format, add a test for an empty result, and open a draft
pull request when the change is ready.
```

You can continue the same task afterward: ask for a revision, provide missing
context, answer an approval request, or finish the session when the work is
complete.

## How it works

```text
Project
  └─ Service (repository, path, or remote URL)
       ├─ Task session → timeline → diff / pull request / AI review
       ├─ Scheduled task
       └─ Linked Kanban workflow
```

### Projects and services

A Project is the shared home for a stream of work. It can contain several
services, so each repository keeps the right task history, default model,
automations, and source context.

Switch services from the left rail. The selected service controls the task
composer, **Recent tasks**, and **Automations** view.

### Tasks and reviews

Open a task to see the live conversation and its current status. Depending on
the task and your role, you can:

- send a follow-up message or continue a finished session;
- answer a permission request;
- cancel or retry a task;
- inspect its diff;
- open the related pull request or merge request; and
- request an AI review.

Use **All**, **Sessions**, and **Reviews** in the Project workspace to find
recent work for the selected service.

### Models and permissions

When several models are available, choose one for the task or leave **Service
default** selected. If no model is available, starting a task is disabled and
the console explains how to get help.

Choose **Ask before actions** when you want to review consequential steps as
they arise. Choose **Full access** only when the task can proceed without those
interruptions.

## Automations and Kanban

### Schedules

Open **Automations** for the selected service to see its recurring tasks.
Project owners can add a cron schedule and the prompt it should run. Schedules
surface their last dispatch error, so a skipped run is never mistaken for a
successful one.

### Provider events

Provider-event reviews depend on an administrator-configured provider and
webhook. If the console says that delivery cannot be verified, do not assume an
automatic review is active—ask an administrator to confirm the integration.

### Kanban

When a jtype board is linked to a Project, use **Kanban** in the Project header
to open it without leaving jcode Cloud. If several boards are linked, choose
the one you need from the board selector.

An unavailable board can be retried. Invalid, unvalidated, or disabled board
automation links are called out explicitly so they are not confused with a
working card-triggered workflow.

## Roles

| Role | Typical access |
| --- | --- |
| **Viewer** | View Projects and task history. |
| **Member** | Start and follow tasks, continue sessions, and request reviews when permitted. |
| **Owner** | Manage Project settings, services, members, integrations, schedules, and Kanban links. |

Your organization may add further restrictions. If an action is unavailable,
ask the Project owner instead of trying to work around it.

## When something needs attention

| What you see | What to do |
| --- | --- |
| **No model is available for this project** | Ask a cluster administrator to grant a model to the Project. |
| **No service connected yet** | Ask a Project owner to add or connect the repository or path. |
| A task fails | Open the task, read the failure reason, then retry after addressing it or ask the owner for help. |
| A schedule shows **did not dispatch** | Read the visible error; it commonly identifies a model or repository/integration problem. |
| Kanban is unavailable or a link is invalid | Use **Retry** if offered, then ask a Project owner or administrator to repair the connection or mapping. |
| Provider event status is unavailable | Ask an administrator to verify the provider webhook. |

## Need help?

Start with the Project owner for repository access, services, schedules, and
Kanban setup. Contact a cluster administrator for models, sign-in, or provider
and webhook issues.

This README intentionally focuses on the everyday product experience. Your
organization maintains operational and contributor documentation separately.
