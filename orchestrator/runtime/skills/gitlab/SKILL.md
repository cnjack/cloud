---
name: gitlab
description: Use the authenticated glab CLI for GitLab repositories, merge requests, issues, pipelines, tags, and releases.
slash: gitlab
---

# GitLab

Use `glab` before writing provider-specific HTTP requests.

- Authentication is provisioned for the current run. Never print tokens or
  inspect credential files.
- Confirm the repository with `glab repo view` before a write.
- Prefer non-destructive repository operations: branches, commits, merge
  requests, issue comments, pipeline inspection, tags, and releases.
- Do not change group membership, project visibility, OAuth applications,
  protected-branch policy, or delete a project.
- Summarize every external write in the final response.
- Cloud does not write back automatically. Only perform an external action when
  it is required by the task.
