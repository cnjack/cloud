import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// First import: patches fetch/EventSource over the native Tauri bridges
// before any app module evaluates.
import './tauriBridge';
import './tokens.css';
import './app.css';
// M14: jcode product composer / Thread styles + jcloud→product token aliases.
import 'jcode-ui/styles.css';
import 'jcode-ui/compat.css';
import '@jcloud/device-ui/src/product/productTokens.css';
import './i18n';
import { App } from './App';

// Dev-only remote-eval hook for the iOS verification driver (see devDrive.ts).
if (import.meta.env.DEV) {
  const { installDevDrive } = await import('./devDrive');
  installDevDrive();
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
