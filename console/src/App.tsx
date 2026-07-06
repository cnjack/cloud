import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { ProjectsPage } from './pages/ProjectsPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { NotFoundPage } from './pages/NotFoundPage';

export function App() {
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<ProjectsPage />} />
        <Route path="/projects" element={<Navigate to="/" replace />} />
        <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
        <Route path="/runs/:runId" element={<RunDetailPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </AppShell>
  );
}
