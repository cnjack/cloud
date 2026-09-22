import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { AccountPreferencesBoundary } from './components/AccountPreferencesBoundary';
import { AppShell } from './components/AppShell';
import { DeviceApiProvider } from './api/DeviceApiProvider';
import { OnboardingGate } from './pages/OnboardingGate';
import { WorkHomePage } from './work-home/WorkHomePage';
import { CodeReviewsPage } from './pages/CodeReviewsPage';
import { RepositoryAutomationPage } from './pages/RepositoryAutomationPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { DeviceListPage, DeviceWorkspacePage } from './pages/DeviceWorkspacePage';
import { DeviceGuidePage } from './pages/DeviceGuidePage';
import { DeviceSessionPage } from './pages/DeviceSessionPage';
import { ClusterOverviewPage } from './pages/ClusterOverviewPage';
import { ClusterModelsPage } from './pages/ClusterModelsPage';
import { ClusterConnectionsPage } from './pages/ClusterConnectionsPage';
import { DeviceAuthorizePage } from './pages/DeviceAuthorizePage';
import { NotFoundPage } from './pages/NotFoundPage';
import { SetupPage } from './pages/SetupPage';
import { AccountSettingsPage } from './pages/AccountSettingsPage';
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
    // setup guidance and sign-in. Success enters Work Home directly.
    <OnboardingGate>
      <AccountPreferencesBoundary><AppShell>
        <DeviceApiProvider>
          <Routes>
            <Route path="/" element={<WorkHomePage />} />
            <Route path="/setup" element={<SetupPage />} />
            <Route path="/repositories" element={<WorkHomePage />} />
            <Route path="/repositories/connect" element={<Navigate to="/repositories" replace />} />
            <Route path="/connections/repositories" element={<Navigate to="/account/settings?section=connections" replace />} />
            <Route path="/repositories/:repositoryId" element={<LegacyRepositoryRedirect />} />
            <Route path="/repositories/:repositoryId/automations/new" element={<RepositoryAutomationPage mode="edit" />} />
            <Route path="/repositories/:repositoryId/automations/:automationId" element={<RepositoryAutomationPage mode="detail" />} />
            <Route path="/repositories/:repositoryId/automations/:automationId/edit" element={<RepositoryAutomationPage mode="edit" />} />
            <Route path="/code-reviews" element={<CodeReviewsPage />} />
            <Route path="/account/settings" element={<AccountSettingsPage />} />
            <Route path="/projects/*" element={<Navigate to="/" replace />} />
            <Route path="/runs/:runId" element={<RunDetailPage />} />
            <Route path="/devices" element={<DeviceListPage />} />
            <Route path="/devices/guide" element={<DeviceGuidePage />} />
            <Route path="/devices/:deviceId" element={<DeviceWorkspacePage />} />
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
      </AppShell></AccountPreferencesBoundary>
    </OnboardingGate>
  );
}

export function LegacyRepositoryRedirect() {
  const { repositoryId = '' } = useParams();
  const { search } = useLocation();
  const params = new URLSearchParams(search);
  params.delete('remote');
  params.set('repository', repositoryId);
  return <Navigate to={`/repositories?${params}`} replace />;
}
