import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { DeviceApiProvider } from './api/DeviceApiProvider';
import { OnboardingGate } from './pages/OnboardingGate';
import { ProjectsPage } from './pages/ProjectsPage';
import { NewProjectPage } from './pages/NewProjectPage';
import { ProjectDetailPage } from './pages/ProjectDetailPage';
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
    const integrationConnected = readQueryParam('integration_connected');
    const integrationError = readQueryParam('integration_error');
    if (linked) {
      toast.push({ kind: 'success', message: t('app.linked', { provider: linked }) });
    } else if (linkError === 'taken') {
      toast.push({
        kind: 'error',
        message: t('app.linkTaken'),
      });
    } else if (linkError) {
      toast.push({ kind: 'error', message: t('app.linkError') });
    } else if (integrationConnected) {
      toast.push({ kind: 'success', message: t('app.integrationConnected', { provider: integrationConnected }) });
    } else if (integrationError) {
      const message = integrationError === 'conflict'
        ? t('app.integrationConflict')
        : integrationError === 'expiring_token_unsupported'
          ? t('app.integrationExpiringToken')
          : t('app.integrationError');
      toast.push({ kind: 'error', message });
    }
    if (linked || linkError || integrationConnected || integrationError) {
      stripQueryParams(['linked', 'link_error', 'integration_connected', 'integration_error']);
    }
  }, [toast, t]);
}

export function App() {
  useLinkFlash();
  return (
    // The gate owns everything before a verified session exists: environment
    // setup guidance, sign-in, and the post-login welcome/landing cards.
    <OnboardingGate>
      <AppShell>
        <DeviceApiProvider>
          <Routes>
            <Route path="/" element={<Navigate to="/projects" replace />} />
            <Route path="/projects" element={<ProjectsPage />} />
            <Route path="/projects/new" element={<NewProjectPage />} />
            <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
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
