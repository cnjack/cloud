import type { Run, Service } from '../api/types';

export function serviceMark(service: Service): string {
  if (service.repo_kind === 'raw') return 'PATH';
  switch (service.provider?.toLowerCase()) {
    case 'gitea':
      return 'GT';
    case 'github':
      return 'GH';
    case 'gitlab':
      return 'GL';
    default:
      return 'GIT';
  }
}

export function serviceSource(service: Service): string {
  return service.repo_kind === 'provider'
    ? service.repo_owner_name || service.name
    : service.raw_repo_url || service.name;
}

export function serviceProviderLabel(service: Service): string {
  if (service.repo_kind === 'raw') return 'Path / remote URL';
  switch (service.provider?.toLowerCase()) {
    case 'gitea':
      return 'Gitea';
    case 'github':
      return 'GitHub';
    case 'gitlab':
      return 'GitLab';
    default:
      return 'Git repository';
  }
}

export function runKindLabel(run: Run): string {
  if (run.kind === 'review') return 'Review';
  if (run.origin === 'schedule') return 'Schedule';
  if (run.origin === 'kanban') return 'Kanban';
  if (run.origin === 'webhook') return 'Webhook';
  return run.session ? 'Session' : 'Task';
}
