import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const rootDir = dirname(fileURLToPath(import.meta.url));

// Tauri mobile dev: the webview loads devUrl (tauri.conf.json). The app talks
// DIRECTLY to the orchestrator (absolute base URL entered on the login page),
// so there is no /api proxy here — CORS is sidestepped because Tauri webviews
// are not subject to browser CORS for fetch/EventSource.
export default defineConfig({
  root: rootDir,
  plugins: [
    react(),
    {
      // Dev-only eval bridge for the M6 verification driver (scripts/drive-ios.mjs):
      // rebroadcasts m6:eval / m6:result custom HMR events between the node-side
      // driver and the webview. Inert in production builds.
      name: 'm6-drive-bridge',
      configureServer(server) {
        server.ws.on('m6:eval', (data) => server.ws.send('m6:eval', data));
        server.ws.on('m6:result', (data) => server.ws.send('m6:result', data));
      },
    },
  ],
  // Linked workspace packages (@jcloud/device-ui) ship raw source; force its
  // transitive deps (jcode-ui → highlight.js CJS) through the prebundler so
  // dev-mode native ESM gets proper CJS interop.
  optimizeDeps: { exclude: ['@jcloud/device-ui'], include: ['jcode-ui'] },
  server: {
    port: 5174,
    strictPort: true,
    // Tauri's mobile dev runner may load the page from a device/emulator that
    // reaches this server over the LAN — bind all interfaces.
    host: true,
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
  // Tauri expects a relative base for the bundled frontend.
  base: './',
});
