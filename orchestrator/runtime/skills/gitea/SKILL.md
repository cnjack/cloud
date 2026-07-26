---
name: gitea
description: Use the authenticated tea CLI for Gitea repositories, pull requests, issues, actions, tags, and releases.
slash: gitea
---

# Gitea

Use the official `tea` CLI before writing provider-specific HTTP requests.

- The active login is provisioned for the current run. Never print tokens or
  inspect credential files.
- Confirm the repository and Gitea instance before a write.
- Prefer non-destructive repository operations: branches, commits, pull
  requests, issue comments, action inspection, tags, and releases.
- Do not change organizations, members, OAuth applications, repository
  visibility, or delete a repository.
- Summarize every external write in the final response.
- Cloud does not write back automatically. Only perform an external action when
  it is required by the task.
