import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from './App';
import { ApiProvider } from './api/ApiProvider';
import { AuthProvider } from './auth/AuthProvider';
import { ToastProvider } from './components/Toast';
import './i18n';
import 'jcode-ui/styles.css';
// M14: legacy-token bridge + jcloud→product token aliases for the jcode
// product composer / Thread (order matters: compat after styles, aliases last).
import 'jcode-ui/compat.css';
import './styles/global.css';
import '@jcloud/device-ui/src/product/productTokens.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

const rootEl = document.getElementById('root');
if (!rootEl) throw new Error('#root not found');

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* AuthProvider sits outside ApiProvider: the http client reads the
          runtime token (and session-401 hook) from the auth context. */}
      <AuthProvider>
        <ApiProvider>
          <ToastProvider>
            <BrowserRouter>
              <App />
            </BrowserRouter>
          </ToastProvider>
        </ApiProvider>
      </AuthProvider>
    </QueryClientProvider>
  </StrictMode>,
);
