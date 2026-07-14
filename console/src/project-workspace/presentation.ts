import { i18n } from '../i18n';
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
  if (service.repo_kind === 'raw') return i18n.t('workspace.provider.pathOrUrl');
  switch (service.provider?.toLowerCase()) {
    case 'gitea':
      return 'Gitea';
    case 'github':
      return 'GitHub';
    case 'gitlab':
      return 'GitLab';
    default:
      return i18n.t('workspace.provider.gitRepository');
  }
}

export function runKindLabel(run: Run): string {
  if (run.kind === 'review') return i18n.t('workspace.runKind.review');
  if (run.origin === 'automation') return i18n.t('workspace.runKind.prEvent');
  if (run.origin === 'schedule') return i18n.t('workspace.runKind.schedule');
  if (run.origin === 'kanban') return i18n.t('workspace.runKind.kanban');
  if (run.origin === 'webhook') return i18n.t('workspace.runKind.webhook');
  return run.session ? i18n.t('workspace.runKind.session') : i18n.t('workspace.runKind.task');
}
