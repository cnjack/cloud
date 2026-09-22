import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { AccountQueryProvider } from './api/AccountQueryProvider';
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

const rootEl = document.getElementById('root');
if (!rootEl) throw new Error('#root not found');

createRoot(rootEl).render(
  <StrictMode>

      {/* AuthProvider sits outside ApiProvider: the http client reads the
          runtime token (and session-401 hook) from the auth context. */}
      <AuthProvider>
        <AccountQueryProvider>
        <ApiProvider>
          <ToastProvider>
            <BrowserRouter>
              <App />
            </BrowserRouter>
          </ToastProvider>
        </ApiProvider>
        </AccountQueryProvider>
      </AuthProvider>

  </StrictMode>,
);
