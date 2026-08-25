import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { DeviceApiProvider } from './api/DeviceApiProvider';
import { OnboardingGate } from './pages/OnboardingGate';
import { ProjectsPage } from './pages/ProjectsPage';
import { RepositoriesPage } from './pages/RepositoriesPage';
import { RepositoryDetailPage } from './pages/RepositoryDetailPage';
import { ConnectRepositoryPage } from './pages/ConnectRepositoryPage';
import { CodeReviewsPage } from './pages/CodeReviewsPage';
import { RepositoryAutomationPage } from './pages/RepositoryAutomationPage';
import { NewProjectPage } from './pages/NewProjectPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
import { ProjectPluginDetailPage } from './pages/ProjectPluginDetailPage';
import { AutomationEditorPage } from './pages/AutomationEditorPage';
import { AutomationDetailPage } from './pages/AutomationDetailPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { DevicesPage } from './pages/DevicesPage';
import { DeviceWelcomePage } from './pages/DeviceWelcomePage';
import { DeviceGuidePage } from './pages/DeviceGuidePage';
import { DeviceSessionPage } from './pages/DeviceSessionPage';
import { ClusterOverviewPage } from './pages/ClusterOverviewPage';
import { ClusterModelsPage } from './pages/ClusterModelsPage';
import { ClusterConnectionsPage } from './pages/ClusterConnectionsPage';
import { DeviceAuthorizePage } from './pages/DeviceAuthorizePage';
import { NotFoundPage } from './pages/NotFoundPage';
import { SetupPage } from './pages/SetupPage';
import { SharedArtifactPage } from './pages/SharedArtifactPage';
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
  const { t } = useTranslation();
  const fired = useRef(false);
  useEffect(() => {
    if (fired.current) return;
    fired.current = true;
    const linked = readQueryParam('linked');
    const linkError = readQueryParam('link_error');
    if (linked) {
      toast.push({ kind: 'success', message: t('app.linked', { provider: linked }) });
    } else if (linkError === 'taken') {
      toast.push({
        kind: 'error',
        message: t('app.linkTaken'),
      });
    } else if (linkError) {
      toast.push({ kind: 'error', message: t('app.linkError') });
    }
    if (linked || linkError) {
      stripQueryParams(['linked', 'link_error']);
    }
  }, [toast, t]);
}

export function App() {
  const location = useLocation();
  if (location.pathname.startsWith('/s/')) {
    return <Routes><Route path="/s/:shareID" element={<SharedArtifactPage />} /><Route path="*" element={<NotFoundPage />} /></Routes>;
  }
  return <AuthenticatedApp />;
}

function AuthenticatedApp() {
  useLinkFlash();
  return (
    // The gate owns everything before a verified session exists: environment
    // setup guidance, sign-in, and the post-login welcome/landing cards.
    <OnboardingGate>
      <AppShell>
        <DeviceApiProvider>
          <Routes>
            <Route path="/" element={<Navigate to="/repositories" replace />} />
            <Route path="/setup" element={<SetupPage />} />
            <Route path="/repositories" element={<RepositoriesPage />} />
            <Route path="/repositories/connect" element={<Navigate to="/repositories" replace />} />
            <Route path="/connections/repositories" element={<ConnectRepositoryPage />} />
            <Route path="/repositories/:repositoryId" element={<RepositoryDetailPage />} />
            <Route path="/repositories/:repositoryId/automations/new" element={<RepositoryAutomationPage mode="edit" />} />
            <Route path="/repositories/:repositoryId/automations/:automationId" element={<RepositoryAutomationPage mode="detail" />} />
            <Route path="/repositories/:repositoryId/automations/:automationId/edit" element={<RepositoryAutomationPage mode="edit" />} />
            <Route path="/code-reviews" element={<CodeReviewsPage />} />
            {/* Legacy Project routes remain only for deep links during the UI migration. */}
            <Route path="/projects" element={<ProjectsPage />} />
            <Route path="/projects/new" element={<NewProjectPage />} />
            <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
            <Route path="/projects/:projectId/plugins/:provider" element={<ProjectPluginDetailPage />} />
            <Route path="/projects/:projectId/automations/new" element={<AutomationEditorPage />} />
            <Route path="/projects/:projectId/automations/:automationId" element={<AutomationDetailPage />} />
            <Route path="/projects/:projectId/automations/:automationId/edit" element={<AutomationEditorPage />} />
            <Route path="/runs/:runId" element={<RunDetailPage />} />
            <Route path="/devices" element={<DevicesPage />} />
            <Route path="/devices/guide" element={<DeviceGuidePage />} />
            <Route path="/devices/:deviceId" element={<DeviceWelcomePage />} />
            <Route path="/devices/:deviceId/sessions/:sessionId" element={<DeviceSessionPage />} />
            <Route path="/cluster" element={<ClusterOverviewPage />} />
            <Route path="/cluster/models" element={<ClusterModelsPage />} />
            <Route path="/cluster/connections" element={<ClusterConnectionsPage />} />
            {/* jcode device login (docs/17 §3): the CLI's verification_uri target. */}
            <Route path="/device" element={<DeviceAuthorizePage />} />
            <Route path="/system" element={<Navigate to="/cluster" replace />} />
            <Route path="*" element={<NotFoundPage />} />
          </Routes>
        </DeviceApiProvider>
      </AppShell>
    </OnboardingGate>
  );
}
