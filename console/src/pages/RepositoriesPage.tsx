import { ArrowRight, ArrowUp, Gear, GitBranch, MagnifyingGlass } from '@phosphor-icons/react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { useAccountModels, useAccountRepositories, useRepositories, useStartAccountTask } from '../api/queries';
import { ApiError } from '../api/client';
import { useOptionalAuth } from '../auth/AuthProvider';
import { Button } from '../components/Button';
import { SurfaceInner } from '../components/PageLayout';
import { Select } from '../components/Select';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { serviceProviderLabel, serviceSource } from '../project-workspace/presentation';
import listStyles from './ProjectsPage.module.css';
import styles from './RepositoriesPage.module.css';

const LAST_ACCOUNT_MODEL_KEY = 'jcloud.last-model.v1:';

function storedModel(accountId: string): string {
  try {
    return window.localStorage.getItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session')) ?? '';
  } catch {
    return '';
  }
}

function rememberModel(accountId: string, modelId: string) {
  try {
    window.localStorage.setItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session'), modelId);
  } catch {
    // Storage can be unavailable in hardened browsers; the in-memory pick remains valid.
  }
}

export function RepositoriesPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const auth = useOptionalAuth();
  const catalog = useAccountRepositories();
  const models = useAccountModels();
  const repositories = useRepositories();
  const startTask = useStartAccountTask();
  const [prompt, setPrompt] = useState('');
  const [selectedRepository, setSelectedRepository] = useState('');
  const [selectedModel, setSelectedModel] = useState('');
  const [search, setSearch] = useState('');

  const targets = catalog.data?.repositories ?? [];
  const targetOptions = targets.map((target) => ({
    value: `${target.provider}:${target.provider_repo_id}`,
    label: target.full_name,
  }));
  const activeTarget = targets.find((target) =>
    `${target.provider}:${target.provider_repo_id}` === selectedRepository);
  const accountId = auth?.me?.user.id ?? '';

  useEffect(() => {
    if (activeTarget || targets.length === 0) return;
    const first = targets.find((target) => target.execution_available !== false) ?? targets[0];
    if (!first) return;
    setSelectedRepository(`${first.provider}:${first.provider_repo_id}`);
  }, [activeTarget, targets]);

  useEffect(() => {
    if (!models.data?.length) {
      setSelectedModel('');
      return;
    }
    const remembered = storedModel(accountId);
    const firstModel = models.data[0];
    if (!firstModel) return;
    setSelectedModel(models.data.some((model) => model.id === remembered) ? remembered : firstModel.id);
  }, [accountId, models.data]);

  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return repositories.data ?? [];
    return (repositories.data ?? []).filter((repository) =>
      [repository.name, repository.repo_owner_name ?? '', repository.raw_repo_url ?? '']
        .join(' ')
        .toLowerCase()
        .includes(query));
  }, [repositories.data, search]);

  const missingRepository = !catalog.isLoading && targets.length === 0;
  const missingModel = !models.isLoading && (models.data?.length ?? 0) === 0;
  const canStart = !!prompt.trim() && !!activeTarget && activeTarget.execution_available !== false &&
    !!selectedModel && !startTask.isPending;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canStart || !activeTarget) return;
    rememberModel(accountId, selectedModel);
    startTask.mutate({
      provider: activeTarget.provider,
      provider_repo_id: activeTarget.provider_repo_id,
      prompt: prompt.trim(),
      base_branch: activeTarget.default_branch,
      model_id: selectedModel,
      session: true,
    }, {
      onSuccess: ({ run }) => {
        setPrompt('');
        navigate(`/runs/${encodeURIComponent(run.id)}`);
      },
      onError: (error) => toast.push({
        kind: 'error',
        message: error instanceof ApiError ? error.message : t('repositories.startFailed'),
      }),
    });
  };

  return (
    <SurfaceInner>
      <section className={styles.hero} aria-labelledby="account-task-heading">
        <div className={styles.heroCopy}>
          <span>{t('repositories.composerEyebrow')}</span>
          <h1 id="account-task-heading">{t('repositories.composerTitle')}</h1>
          <p>{t('repositories.composerDescription')}</p>
        </div>

        <form className={styles.composer} onSubmit={submit} noValidate>
          <textarea
            aria-label={t('repositories.taskAria')}
            placeholder={t('repositories.taskPlaceholder')}
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                event.currentTarget.form?.requestSubmit();
              }
            }}
            rows={3}
          />
          <div className={styles.composerBar}>
            <div className={styles.contextControls}>
              <Select
                className={styles.contextSelect}
                aria-label={t('repositories.repositoryAria')}
                value={selectedRepository}
                onChange={setSelectedRepository}
                disabled={catalog.isLoading || targetOptions.length === 0 || startTask.isPending}
                placeholder={catalog.isLoading ? t('repositories.accountLoading') : t('repositories.chooseAccountRepository')}
                options={targetOptions}
              />
              <span className={styles.branch} title={activeTarget?.default_branch}>
                <GitBranch size={15} aria-hidden="true" />
                {activeTarget?.default_branch ?? '—'}
              </span>
              <Select
                className={styles.modelSelect}
                aria-label={t('taskComposer.modelAria')}
                value={selectedModel}
                onChange={(modelId) => {
                  setSelectedModel(modelId);
                  rememberModel(accountId, modelId);
                }}
                disabled={models.isLoading || !models.data?.length || startTask.isPending}
                placeholder={models.isLoading ? t('repositories.modelsLoading') : t('repositories.modelRequired')}
                options={(models.data ?? []).map((model) => ({ value: model.id, label: model.name }))}
              />
            </div>
            <Button
              type="submit"
              variant="primary"
              className={styles.startButton}
              disabled={!canStart}
              loading={startTask.isPending}
              aria-label={t('repositories.startTask')}
            >
              <ArrowUp size={19} weight="bold" aria-hidden="true" />
            </Button>
          </div>
        </form>

        {(missingRepository || missingModel || activeTarget?.execution_available === false || catalog.isError) && (
          <div className={styles.blocker} role="status">
            {catalog.isError
              ? t('repositories.accountLoadError')
              : missingRepository
                ? t('repositories.linkAccount')
                : missingModel
                  ? t('repositories.authorizeModel')
                  : activeTarget?.execution_error}
          </div>
        )}
        {(catalog.data?.sources ?? []).filter((source) => source.status === 'unavailable').map((source) => (
          <div className={styles.blocker} role="status" key={source.provider}>
            {source.provider}: {source.message}
          </div>
        ))}
      </section>

      <section className={styles.details} aria-labelledby="repository-list-title">
        <div className={listStyles.sectionHeading}>
          <div>
            <h2 id="repository-list-title">{t('repositories.detailsTitle')}</h2>
            <p>{t('repositories.detailsDescription')}</p>
          </div>
        </div>

        {repositories.isLoading ? (
          <div className={listStyles.state}><LoadingBlock label={t('repositories.loading')} /></div>
        ) : repositories.isError ? (
          <div className={listStyles.state}>
            <ErrorBlock error={repositories.error} onRetry={() => void repositories.refetch()} title={t('repositories.loadError')} />
          </div>
        ) : (repositories.data?.length ?? 0) === 0 ? (
          <p className={styles.noDetails}>{t('repositories.noDetails')}</p>
        ) : (
          <>
            <div className={listStyles.toolbar}>
              <label className={listStyles.search}>
                <MagnifyingGlass size={14} aria-hidden="true" />
                <span className={listStyles.srOnly}>{t('repositories.search')}</span>
                <input
                  type="search"
                  aria-label={t('repositories.search')}
                  placeholder={t('repositories.searchPlaceholder')}
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </label>
            </div>
            {visible.length === 0 ? (
              <div className={listStyles.searchEmpty} role="status"><strong>{t('repositories.noMatch')}</strong></div>
            ) : (
              <ul className={listStyles.list}>
                {visible.map((repository) => (
                  <li key={repository.id}>
                    <Link className={listStyles.row} to={`/repositories/${encodeURIComponent(repository.id)}`} data-testid="repository-row">
                      <span className={listStyles.mark}><GitBranch size={18} aria-hidden="true" /></span>
                      <span className={listStyles.rowMain}>
                        <span className={listStyles.rowTitle}>
                          <strong>{repository.repo_owner_name || repository.name}</strong>
                          <span className={listStyles.tag}>{serviceProviderLabel(repository)}</span>
                        </span>
                        <span className={listStyles.rowMeta}>
                          <span className={listStyles.mono}>{serviceSource(repository)}</span>
                          <span className={listStyles.created}>{repository.default_branch}</span>
                        </span>
                      </span>
                      <span className={listStyles.rowSide}>
                        <Gear size={15} aria-label={t('repositories.details')} />
                        <span>{t('repositories.open')}</span>
                        <ArrowRight size={16} aria-hidden="true" />
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </section>
    </SurfaceInner>
  );
}
