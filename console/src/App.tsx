import { useEffect, useRef } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { OnboardingGate } from './pages/OnboardingGate';
import { ProjectsPage } from './pages/ProjectsPage';
import { NewProjectPage } from './pages/NewProjectPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { ClusterOverviewPage } from './pages/ClusterOverviewPage';
import { ClusterModelsPage } from './pages/ClusterModelsPage';
import { ClusterConnectionsPage } from './pages/ClusterConnectionsPage';
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
    const integrationConnected = readQueryParam('integration_connected');
    const integrationError = readQueryParam('integration_error');
    if (linked) {
      toast.push({ kind: 'success', message: `Linked your ${linked} account.` });
    } else if (linkError === 'taken') {
      toast.push({
        kind: 'error',
        message: 'That account is already linked to another user.',
      });
    } else if (linkError) {
      toast.push({ kind: 'error', message: 'Could not link that account.' });
    } else if (integrationConnected) {
      toast.push({ kind: 'success', message: `${integrationConnected} integration connected.` });
    } else if (integrationError) {
      const message = integrationError === 'conflict'
        ? 'An integration with that name already exists.'
        : integrationError === 'expiring_token_unsupported'
          ? 'This provider issued a short-lived token. Use the bot token option for unattended access.'
          : 'Could not authorize the git integration.';
      toast.push({ kind: 'error', message });
    }
    if (linked || linkError || integrationConnected || integrationError) {
      stripQueryParams(['linked', 'link_error', 'integration_connected', 'integration_error']);
    }
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
          <Route path="/" element={<Navigate to="/projects" replace />} />
          <Route path="/projects" element={<ProjectsPage />} />
          <Route path="/projects/new" element={<NewProjectPage />} />
          <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
          <Route path="/runs/:runId" element={<RunDetailPage />} />
          <Route path="/cluster" element={<ClusterOverviewPage />} />
          <Route path="/cluster/models" element={<ClusterModelsPage />} />
          <Route path="/cluster/connections" element={<ClusterConnectionsPage />} />
          <Route path="/system" element={<Navigate to="/cluster" replace />} />
          <Route path="*" element={<NotFoundPage />} />
        </Routes>
      </AppShell>
    </OnboardingGate>
  );
}
