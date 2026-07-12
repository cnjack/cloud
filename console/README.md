# jcode Cloud

jcode Cloud is a shared workspace for asking an AI coding agent to work on the
repositories your team already uses. Start a task in a Project, follow the
agent in a chat-like timeline, inspect its changes, and continue the session
when you need another turn.

> This guide is for people using an already deployed jcode Cloud workspace. It
> explains the product, not how to install or operate the platform.

## What you can do

- Start an interactive coding session for a repository or local-path service.
- Watch the agent's work, respond to permission requests, and send follow-up
  instructions in the same task.
- Inspect a diff, open the related pull request or merge request, and request
  an AI review when one is available.
- Schedule recurring work for a service, such as a daily check or release-note
  update.
- Open a linked Kanban board when your Project uses jtype.

## Before you start

You need:

1. The URL for your organization's jcode Cloud workspace.
2. A signed-in account with access to a Project.
3. At least one service in that Project. A service is usually a Gitea, GitHub,
   or GitLab repository; it can also be a path or remote URL.
4. A model made available to the Project by a cluster administrator.

If any of these are missing, the console says so explicitly. For example, a
disabled **Send** button with a “No model is available” message means a model
must be granted before a task can start.

## Start your first task

1. Open **Projects** and select a Project. If you are an owner and do not have
   one yet, choose **New project**.
2. Choose a service from the left rail. If the Project has no services, ask an
   owner to connect a repository or add one from the Project workspace.
3. On the **Tasks** tab, describe the outcome you want in the **New task**
   composer. Include useful context: the affected area, expected behavior, and
   any constraints.
4. Pick a model or leave **Service default** selected when your Project offers
   more than one model. Choose **Full access** or **Ask before actions** as
   appropriate for the task.
5. Select **Send**. jcode Cloud opens the task detail page and begins showing
   progress.

### Write a useful request

Good requests are specific about the result, not just the implementation.

```text
Add CSV export to the analytics dashboard. Include the currently filtered rows,
use the existing date formatting, add a test for an empty result, and open a
draft PR when the change is ready.
```

You can keep the conversation going from the task detail page. Ask for a
revision, provide extra context, or finish the session once the work is done.

## Work inside a Project

A Project is a home for related repositories and their tasks.

### Services

The service rail on the left lists the repositories and paths available in the
current Project. Selecting one changes the task composer, recent-task list,
and automation view to that service.

Use a separate service when work belongs to a different repository. This keeps
the agent's workspace, branch, schedules, and history tied to the right code
base.

### Recent tasks

The **Tasks** tab shows recent work for the selected service. Use **All**,
**Sessions**, and **Reviews** to narrow the list.

Open any task to see its live timeline. Depending on the task, you can:

- send a follow-up message;
- answer a permission request;
- cancel or retry the task;
- inspect the diff;
- open its pull request or merge request; and
- request an AI review.

The available actions depend on the task state and your Project role.

## Automate recurring work

Open **Automations** for the selected service to manage schedules. Project
owners can add a cron expression and the prompt that should run at that time.
For example, a weekday schedule can ask the agent to check a dependency report
or summarize recently merged changes.

Provider-event reviews require an administrator to configure the provider and
webhook delivery. If the console says that this status cannot be verified, do
not assume automatic reviews are active—ask your administrator to confirm the
integration.

## Use Kanban when it is linked

If a Project has a jtype board link, a **Kanban** button appears in its header.
Open it to view the board without leaving jcode Cloud. When several boards are
linked, choose the one you need from the board selector.

The console makes board-link problems visible. An unavailable board can be
retried; an invalid or unvalidated automation link is clearly marked so it is
not mistaken for a healthy card-triggered workflow.

## Roles at a glance

| Role | Typical access |
| --- | --- |
| Viewer | View Projects and task history. |
| Member | Start and follow tasks, continue sessions, and request reviews when permitted. |
| Owner | Manage Project settings, services, members, integrations, schedules, and Kanban links. |

Your organization may apply additional permissions. If a control is missing or
disabled, ask the Project owner rather than trying to work around it.

## When something needs attention

| What you see | What to do |
| --- | --- |
| **No model is available for this project** | Ask a cluster administrator to grant a model to the Project. |
| **No service connected yet** | Ask a Project owner to add or connect the repository or path. |
| A task fails | Open the task to read the failure reason, then retry after fixing the reported issue or ask the owner for help. |
| A schedule shows **did not dispatch** | Read the visible error on the schedule; it commonly identifies an unavailable model or repository/integration problem. |
| Kanban is unavailable or a link is invalid | Use **Retry** if offered, then ask a Project owner or administrator to repair the board connection or mapping. |
| Provider event status is unavailable | Ask an administrator to verify the provider webhook; the console cannot infer that it is working. |

## A few practical tips

- Use one task for one coherent outcome. Start a new task when the work moves to
  another repository or a separate goal.
- State what “done” means: tests to run, files to avoid, acceptance criteria,
  or whether a draft pull request is expected.
- Review the diff and pull request before merging. The agent can propose and
  prepare changes; your team remains responsible for approving them.
- Choose **Ask before actions** for work that needs your decision before the
  agent performs sensitive or consequential steps.

## Need help?

Start with the Project owner when you need repository access, a service,
automation changes, or Kanban setup. Contact a cluster administrator for model
availability, sign-in, or provider/webhook problems.

For platform installation, deployment, and API documentation, use the
repository's administrator and developer documentation rather than this guide.
