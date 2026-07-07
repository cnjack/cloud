/*
 * ProjectDetailPage — the composer + run list (J1-S4, J2, J3; multitenant §5).
 *
 * Dumb UX: a project with a single repository never shows the word "service" —
 * the composer just dispatches a run. Once a project has more than one repo
 * (added via the low-key "+ Add repository" affordance below the runs), the
 * composer grows a repository selector and runs are dispatched against the chosen
 * service.
 *
 * Role gating (blueprint §2, backend-enforced 403s are the source of truth; this
 * is UX): a viewer sees no composer and no Settings; only an owner (or
 * cluster-admin) can change settings / add a repository / manage members.
 */
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  useProject,
  useRuns,
  useCreateRun,
  useCreateServiceRun,
  useCreateService,
} from '../api/queries';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField, TextAreaField } from '../components/Field';
import { GitModeToggle } from '../components/GitModeToggle';
import { StatusBadge } from '../components/StatusBadge';
import { EmptyState } from '../components/EmptyState';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { GitModeBadge } from '../components/GitModeBadge';
import { ProjectSettingsModal } from './ProjectSettingsModal';
import { ApiError } from '../api/client';
import { providerForRepoUrl } from '../lib/repo';
import { shortId, summarize, timeAgo } from '../lib/format';
import type { GitMode, Service } from '../api/types';
import styles from './ProjectDetailPage.module.css';

/** A human label for a service in the repository selector. */
function serviceLabel(svc: Service): string {
  const repo =
    svc.repo_kind === 'provider' ? svc.repo_owner_name : svc.raw_repo_url;
  return svc.name === 'default' ? repo || svc.name : `${svc.name} · ${repo ?? ''}`;
}

export function ProjectDetailPage() {
  const { projectId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();

  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const createRun = useCreateRun(projectId);
  const createServiceRun = useCreateServiceRun(projectId);
  const createService = useCreateService(projectId);

  const [prompt, setPrompt] = useState('');
  const [promptError, setPromptError] = useState<string>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [selectedService, setSelectedService] = useState<string>('');

  // Add-repository inline form.
  const [addOpen, setAddOpen] = useState(false);
  const [repoName, setRepoName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [repoMode, setRepoMode] = useState<GitMode>('readonly');
  const [repoErr, setRepoErr] = useState<{ name?: string; url?: string }>({});

  const p = project.data;
  const services = useMemo(() => p?.services ?? [], [p]);
  const multiService = services.length > 1;
  const role = p?.role ?? 'owner';
  const canRun = role !== 'viewer';
  const canManage = role === 'owner';

  // Default the composer's service selection to the 'default' (or first) service.
  const activeServiceId =
    selectedService ||
    services.find((s) => s.name === 'default')?.id ||
    services[0]?.id ||
    '';

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!prompt.trim()) {
      setPromptError('Describe the task for the agent.');
      return;
    }
    setPromptError(undefined);
    const onDone = {
      onSuccess: (run: { id: string }) => {
        setPrompt('');
        toast.push({ kind: 'success', message: 'Run dispatched.' });
        navigate(`/runs/${run.id}`);
      },
      onError: (err: unknown) => {
        const msg = err instanceof ApiError ? err.message : 'Failed to start run.';
        toast.push({ kind: 'error', message: msg });
      },
    };
    // Multi-service projects dispatch against the selected service; a single-repo
    // project uses the project shim (no service concept surfaced).
    if (multiService && activeServiceId) {
      createServiceRun.mutate({ serviceId: activeServiceId, input: { prompt: prompt.trim() } }, onDone);
    } else {
      createRun.mutate({ prompt: prompt.trim() }, onDone);
    }
  };

  const submitRepo = (e: React.FormEvent) => {
    e.preventDefault();
    const errs: typeof repoErr = {};
    if (!repoName.trim()) errs.name = 'Name is required.';
    if (!repoUrl.trim()) errs.url = 'Repository URL is required.';
    else if (repoMode === 'draft_pr' && providerForRepoUrl(repoUrl) === null)
      errs.url = 'Draft PR needs a provider repository URL.';
    setRepoErr(errs);
    if (Object.keys(errs).length) return;
    createService.mutate(
      { name: repoName.trim(), repo_url: repoUrl.trim(), git_mode: repoMode },
      {
        onSuccess: () => {
          toast.push({ kind: 'success', message: `Repository “${repoName.trim()}” added.` });
          setAddOpen(false);
          setRepoName('');
          setRepoUrl('');
          setRepoMode('readonly');
          setRepoErr({});
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Failed to add repository.',
          }),
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

  const runBusy = createRun.isPending || createServiceRun.isPending;

  return (
    <div className={styles.page}>
      <nav className={styles.crumbs}>
        <Link to="/" className={styles.crumbLink}>
          Projects
        </Link>
        <span className={styles.crumbSep}>/</span>
        <span className={styles.crumbCurrent}>{p!.name}</span>
      </nav>

      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>{p!.name}</h1>
          <div className={styles.repoRow}>
            <code className={styles.repo}>{p!.repo_url}</code>
            <span className={styles.branch}>{p!.default_branch}</span>
            <GitModeBadge gitMode={p!.git_mode} providerRepo={p!.provider_repo} />
            {multiService && (
              <span className={styles.repoCount} data-testid="repo-count">
                {services.length} repositories
              </span>
            )}
          </div>
        </div>
        {canManage && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setSettingsOpen(true)}
            data-testid="project-settings-btn"
          >
            Settings
          </Button>
        )}
      </header>

      {canRun && (
        <Card className={styles.composer}>
          <form onSubmit={submit} noValidate>
            {multiService && (
              <div className={styles.serviceRow}>
                <label className={styles.serviceLabel} htmlFor="composer-service">
                  Repository
                </label>
                <select
                  id="composer-service"
                  className={styles.serviceSelect}
                  value={activeServiceId}
                  onChange={(e) => setSelectedService(e.target.value)}
                  data-testid="composer-service-select"
                >
                  {services.map((s) => (
                    <option key={s.id} value={s.id}>
                      {serviceLabel(s)}
                    </option>
                  ))}
                </select>
              </div>
            )}
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
                loading={runBusy}
                data-testid="run-submit"
              >
                Run
              </Button>
            </div>
          </form>
        </Card>
      )}

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
            description={
              canRun
                ? 'Dispatch your first run using the box above.'
                : 'No runs have been dispatched in this project yet.'
            }
          />
        ) : (
          <div className={styles.tableWrap} role="table" data-testid="runs-table">
            <div className={styles.tableHead} role="rowgroup">
              <div className={styles.tableHeadRow} role="row">
                <span role="columnheader">Run</span>
                <span role="columnheader">Task</span>
                <span role="columnheader">Status</span>
                <span role="columnheader">Created</span>
              </div>
            </div>
            <ul className={styles.rows} role="rowgroup">
              {runs.data!.map((run) => (
                <li key={run.id} role="presentation">
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

        {/* Low-key "add another repository" affordance — turns the project into a
            multi-repo project and reveals the composer's repository selector. */}
        {canManage && (
          <div className={styles.addRepo}>
            {addOpen ? (
              <Card className={styles.addRepoCard}>
                <form onSubmit={submitRepo} noValidate className={styles.addRepoForm}>
                  <TextField
                    label="Repository name"
                    required
                    placeholder="frontend"
                    value={repoName}
                    onChange={(e) => setRepoName(e.target.value)}
                    error={repoErr.name}
                    data-testid="add-repo-name"
                    autoComplete="off"
                  />
                  <TextField
                    label="Repository URL"
                    required
                    placeholder="https://github.com/acme/frontend"
                    value={repoUrl}
                    onChange={(e) => setRepoUrl(e.target.value)}
                    error={repoErr.url}
                    data-testid="add-repo-url"
                    autoComplete="off"
                  />
                  <GitModeToggle value={repoMode} onChange={setRepoMode} />
                  <div className={styles.addRepoActions}>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setAddOpen(false)}
                      disabled={createService.isPending}
                    >
                      Cancel
                    </Button>
                    <Button
                      type="submit"
                      variant="primary"
                      size="sm"
                      loading={createService.isPending}
                      data-testid="add-repo-submit"
                    >
                      Add repository
                    </Button>
                  </div>
                </form>
              </Card>
            ) : (
              <button
                type="button"
                className={styles.addRepoTrigger}
                onClick={() => setAddOpen(true)}
                data-testid="add-repo-trigger"
              >
                + Add repository
              </button>
            )}
          </div>
        )}
      </section>

      <ProjectSettingsModal
        open={settingsOpen}
        project={p!}
        onClose={() => setSettingsOpen(false)}
        onDeleted={() => {
          setSettingsOpen(false);
          navigate('/');
        }}
      />
    </div>
  );
}
