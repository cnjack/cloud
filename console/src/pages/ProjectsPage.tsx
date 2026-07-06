/*
 * ProjectsPage — J1-S1/S3. Project list with first-run empty state and the
 * create-project modal. Empty state carries [data-testid=new-project-btn].
 */
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useProjects } from '../api/queries';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { CreateProjectModal } from './CreateProjectModal';
import { timeAgo } from '../lib/format';
import styles from './ProjectsPage.module.css';

export function ProjectsPage() {
  const { data: projects, isLoading, isError, error, refetch } = useProjects();
  const [modalOpen, setModalOpen] = useState(false);
  const navigate = useNavigate();

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Projects</h1>
          <p className={styles.subtitle}>
            Point a project at a git repo, then dispatch runs to your cluster.
          </p>
        </div>
        {projects && projects.length > 0 && (
          <Button
            variant="primary"
            onClick={() => setModalOpen(true)}
            data-testid="new-project-btn"
          >
            New project
          </Button>
        )}
      </header>

      {isLoading ? (
        <LoadingBlock label="Loading projects…" />
      ) : isError ? (
        <ErrorBlock error={error} onRetry={() => refetch()} title="Couldn't load projects" />
      ) : projects && projects.length === 0 ? (
        <EmptyState
          data-testid="projects-empty"
          title="No projects yet"
          description="Create your first project to point jcode Cloud at a git repository and dispatch a run."
          action={
            <Button
              variant="primary"
              onClick={() => setModalOpen(true)}
              data-testid="new-project-btn"
            >
              New project
            </Button>
          }
        />
      ) : (
        <ul className={styles.grid}>
          {projects!.map((p) => (
            <li key={p.id}>
              <Card
                interactive
                onClick={() => navigate(`/projects/${p.id}`)}
                data-testid="project-card"
                data-project-name={p.name}
              >
                <div className={styles.cardTop}>
                  <span className={styles.cardName}>{p.name}</span>
                  <span className={styles.branch}>{p.default_branch}</span>
                </div>
                <div className={styles.repo}>{p.repo_url}</div>
                <div className={styles.meta}>Created {timeAgo(p.created_at)}</div>
              </Card>
            </li>
          ))}
        </ul>
      )}

      <CreateProjectModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        onCreated={(project) => {
          setModalOpen(false);
          navigate(`/projects/${project.id}`);
        }}
      />
    </div>
  );
}
