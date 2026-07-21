import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useState } from 'react';
import { HashRouter, Navigate, Route, Routes } from 'react-router-dom';
import { DeviceApiProvider } from '@jcloud/device-ui';
import { MobileAuthProvider, useMobileAuth } from './auth';
import { LoginPage } from './pages/LoginPage';
import { GuidePage } from './pages/GuidePage';
import { DevicesPage } from './pages/DevicesPage';
import { ScanPage } from './pages/ScanPage';
import { DeviceWelcomePage } from './pages/DeviceWelcomePage';
import { DeviceSessionPage } from './pages/DeviceSessionPage';

/**
 * App — provider stack + routes (docs/17 §7.2). HashRouter because the Tauri
 * webview has no server fallback for deep links. Signed-out users only ever
 * see the login page.
 */
export function App() {
  const [queryClient] = useState(() => new QueryClient());
  return (
    <QueryClientProvider client={queryClient}>
      <MobileAuthProvider>
        <AuthedTree />
      </MobileAuthProvider>
    </QueryClientProvider>
  );
}

function AuthedTree() {
  const auth = useMobileAuth();
  // The guide is static content — reachable from the login screen too, so a
  // first-time user can read it before signing in.
  const [showGuide, setShowGuide] = useState(false);
  if (!auth.signedIn) {
    if (showGuide) return <GuidePage onBack={() => setShowGuide(false)} />;
    return <LoginPage onGuide={() => setShowGuide(true)} />;
  }
  return (
    <DeviceApiProvider
      getToken={auth.token}
      options={{ baseUrl: `${auth.cloudUrl}/api/v1`, credentials: 'omit' }}
    >
      <HashRouter>
        <Routes>
          <Route path="/" element={<DevicesPage />} />
          <Route path="/guide" element={<GuidePage />} />
          <Route path="/scan" element={<ScanPage />} />
          <Route path="/devices/:deviceId" element={<DeviceWelcomePage />} />
          <Route path="/devices/:deviceId/sessions/:sessionId" element={<DeviceSessionPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </HashRouter>
    </DeviceApiProvider>
  );
}
