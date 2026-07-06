/*
 * ProjectDetailPage — the project's run list plus the "New Run" composer
 * (J1-S4, J2, J3-S1..S3). Runs table shows id / prompt summary / status badge /
 * created time; badges live-update via the polling in useRuns while any run is
 * non-terminal. Clicking a run opens its detail page.
 */
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useProject, useRuns, useCreateRun } from '../api/queries';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextAreaField } from '../components/Field';
import { StatusBadge } from '../components/StatusBadge';
import { EmptyState } from '../components/EmptyState';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import { shortId, summarize, timeAgo } from '../lib/format';
import styles from './ProjectDetailPage.module.css';

export function ProjectDetailPage() {
  const { projectId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();

  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const createRun = useCreateRun(projectId);

  const [prompt, setPrompt] = useState('');
  const [promptError, setPromptError] = useState<string>();

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!prompt.trim()) {
      setPromptError('Describe the task for the agent.');
      return;
    }
    setPromptError(undefined);
    createRun.mutate(
      { prompt: prompt.trim() },
      {
        onSuccess: (run) => {
          setPrompt('');
          toast.push({ kind: 'success', message: 'Run dispatched.' });
          navigate(`/runs/${run.id}`);
        },
        onError: (err) => {
          const msg = err instanceof ApiError ? err.message : 'Failed to start run.';
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  if (project.isLoading) return <LoadingBlock label="Loading project…" />;
  if (project.isError)
    return (
      <ErrorBlock
        error={project.error}
        onRetry={() => project.refetch()}
        title="Couldn't load project"
      />
    );

  const p = project.data!;

  return (
    <div className={styles.page}>
      <nav className={styles.crumbs}>
        <Link to="/" className={styles.crumbLink}>
          Projects
        </Link>
        <span className={styles.crumbSep}>/</span>
        <span className={styles.crumbCurrent}>{p.name}</span>
      </nav>

      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>{p.name}</h1>
          <div className={styles.repoRow}>
            <code className={styles.repo}>{p.repo_url}</code>
            <span className={styles.branch}>{p.default_branch}</span>
          </div>
        </div>
      </header>

      <Card className={styles.composer}>
        <form onSubmit={submit} noValidate>
          <TextAreaField
            label="New run"
            required
            placeholder="Describe the task, e.g. Add a line 'Hello' to the end of README."
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            error={promptError}
            data-testid="run-input"
            rows={3}
          />
          <div className={styles.composerActions}>
            <span className={styles.composerHint}>
              Runs headless in your cluster; you'll get a reviewable diff.
            </span>
            <Button
              type="submit"
              variant="primary"
              loading={createRun.isPending}
              data-testid="run-submit"
            >
              Run
            </Button>
          </div>
        </form>
      </Card>

      <section className={styles.runsSection}>
        <h2 className={styles.sectionTitle}>Runs</h2>
        {runs.isLoading ? (
          <LoadingBlock label="Loading runs…" />
        ) : runs.isError ? (
          <ErrorBlock error={runs.error} onRetry={() => runs.refetch()} title="Couldn't load runs" />
        ) : runs.data && runs.data.length === 0 ? (
          <EmptyState
            data-testid="runs-empty"
            title="No runs yet"
            description="Dispatch your first run using the box above."
          />
        ) : (
          <div className={styles.tableWrap} role="table" data-testid="runs-table">
            <div className={styles.tableHead} role="row">
              <span role="columnheader">Run</span>
              <span role="columnheader">Task</span>
              <span role="columnheader">Status</span>
              <span role="columnheader">Created</span>
            </div>
            <ul className={styles.rows}>
              {runs.data!.map((run) => (
                <li key={run.id}>
                  <Link
                    to={`/runs/${run.id}`}
                    className={styles.row}
                    role="row"
                    data-testid="run-row"
                    data-run-id={run.id}
                    data-status={run.status}
                  >
                    <code className={styles.runId} role="cell">
                      {shortId(run.id)}
                    </code>
                    <span className={styles.prompt} role="cell">
                      {summarize(run.prompt)}
                      {run.retried_from && (
                        <span className={styles.retryTag} title="Retry of an earlier run">
                          retry
                        </span>
                      )}
                    </span>
                    <span role="cell">
                      <StatusBadge status={run.status} size="sm" />
                    </span>
                    <span className={styles.created} role="cell">
                      {timeAgo(run.created_at)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
    </div>
  );
}
