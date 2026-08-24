import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useCreateProject, useProjects } from '../api/queries';
import { ActionLink, PageHeader, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ProjectPluginsPanel } from './ProjectPluginsPanel';
import { ProjectDetailPage } from './ProjectDetailPage';

/**
 * Repository onboarding keeps the singleton Project as an implementation
 * boundary. The user never chooses, names, or navigates that container.
 */
export function ConnectRepositoryPage({ connectionsOnly = false }: { connectionsOnly?: boolean }) {
  const { t } = useTranslation();
  const projects = useProjects();
  const createProject = useCreateProject();
  const createStarted = useRef(false);
  const personalProject = createProject.data
    ?? (projects.data ?? []).find((project) => (project.role ?? 'owner') === 'owner');

  useEffect(() => {
    if (!projects.isSuccess || personalProject || createStarted.current) return;
    createStarted.current = true;
    createProject.mutate({ name: 'Personal workspace' });
  }, [createProject, personalProject, projects.isSuccess]);

  if (projects.isLoading || createProject.isPending) {
    return <LoadingBlock label={t('repositories.preparing')} />;
  }
  if (projects.isError || createProject.isError) {
    return (
      <ErrorBlock
        title={t('repositories.prepareError')}
        error={projects.error ?? createProject.error}
        onRetry={() => {
          createStarted.current = false;
          void projects.refetch();
        }}
      />
    );
  }
  if (!personalProject) return <LoadingBlock label={t('repositories.preparing')} />;

  if (connectionsOnly) {
    return (
      <SurfaceInner>
        <PageHeader
          eyebrow={t('repositories.eyebrow')}
          title={t('repositories.connectionsTitle')}
          description={t('repositories.connectionsDescription')}
          actions={<ActionLink to="/repositories/connect" variant="primary">{t('repositories.chooseRepository')}</ActionLink>}
        />
        <ProjectPluginsPanel project={personalProject} repositoryMode />
      </SurfaceInner>
    );
  }

  return (
    <ProjectDetailPage
      projectIdOverride={personalProject.id}
      repositoryMode
      connectMode
    />
  );
}
