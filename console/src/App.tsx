import { useEffect, useRef } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { OnboardingGate } from './pages/OnboardingGate';
import { ProjectsPage } from './pages/ProjectsPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { SystemPage } from './pages/SystemPage';
import { NotFoundPage } from './pages/NotFoundPage';
import { useToast } from './components/Toast';
import { readQueryParam, stripQueryParams } from './lib/url';

/**
 * Surfaces the identity-link result the orchestrator appends to CONSOLE_URL after
 * an /auth/link round trip (blueprint §2): ?linked=<provider> (success) or
 * ?link_error=taken (the account already belongs to someone else). Read once,
 * then stripped from the URL so a refresh doesn't replay the toast.
 */
function useLinkFlash() {
  const toast = useToast();
  const fired = useRef(false);
  useEffect(() => {
    if (fired.current) return;
    fired.current = true;
    const linked = readQueryParam('linked');
    const linkError = readQueryParam('link_error');
    if (linked) {
      toast.push({ kind: 'success', message: `Linked your ${linked} account.` });
    } else if (linkError === 'taken') {
      toast.push({
        kind: 'error',
        message: 'That account is already linked to another user.',
      });
    } else if (linkError) {
      toast.push({ kind: 'error', message: 'Could not link that account.' });
    }
    if (linked || linkError) stripQueryParams(['linked', 'link_error']);
  }, [toast]);
}

export function App() {
  useLinkFlash();
  return (
    // The gate owns everything before a verified session exists: environment
    // setup guidance, sign-in, and the post-login welcome/landing cards.
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
