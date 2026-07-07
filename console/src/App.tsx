import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { OnboardingGate } from './pages/OnboardingGate';
import { ProjectsPage } from './pages/ProjectsPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { SystemPage } from './pages/SystemPage';
import { NotFoundPage } from './pages/NotFoundPage';

export function App() {
  return (
    // The gate owns everything before a verified session exists: environment
    // setup guidance, sign-in, and the post-login landing card.
    <OnboardingGate>
      <AppShell>
        <Routes>
          <Route path="/" element={<ProjectsPage />} />
          <Route path="/projects" element={<Navigate to="/" replace />} />
          <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
          <Route path="/runs/:runId" element={<RunDetailPage />} />
          {/* Cluster-admin view. The route exists for both roles; SystemPage itself
              renders a plain notice for project-admin (presentation-only gating). */}
          <Route path="/system" element={<SystemPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Routes>
      </AppShell>
    </OnboardingGate>
  );
}
