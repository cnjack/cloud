import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useRepository } from '../api/queries';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ProjectDetailPage } from './ProjectDetailPage';

export function RepositoryDetailPage() {
  const { t } = useTranslation();
  const { repositoryId = '' } = useParams();
  const repository = useRepository(repositoryId);

  if (repository.isLoading) return <LoadingBlock label={t('repositories.loadingOne')} />;
  if (repository.isError || !repository.data) {
    return (
      <ErrorBlock
        title={t('repositories.loadOneError')}
        error={repository.error}
        onRetry={() => void repository.refetch()}
      />
    );
  }

  return (
    <ProjectDetailPage
      projectIdOverride={repository.data.project_id}
      repositoryIdOverride={repository.data.id}
      repositoryMode
    />
  );
}
