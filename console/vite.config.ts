/// <reference types="vitest/config" />
import { readFileSync } from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// Resolve env/root from THIS file's directory, not process.cwd(), so the server
// works no matter where it's launched from (e.g. the repo-root preview harness).
const rootDir = dirname(fileURLToPath(import.meta.url));
const packageVersion = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf8'),
) as { version: string };

// The console talks to the orchestrator over /api/v1/*. In dev we proxy that
// prefix to the orchestrator (default http://localhost:8080). In demo mode
// (VITE_DEMO=1) the app never hits the network — see src/api/mockClient.ts.
//
// Vitest config lives here too (single vite version) to avoid the vite5/vite6
// Plugin type clash that a separate vitest.config.ts would introduce.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, rootDir, '');
  const target = env.VITE_API_PROXY_TARGET || 'http://localhost:8080';
  const appVersion =
    process.env.VITE_APP_VERSION || env.VITE_APP_VERSION || `v${packageVersion.version}`;

  return {
    root: rootDir,
    plugins: [react()],
    define: {
      __JCLOUD_VERSION__: JSON.stringify(appVersion),
    },
    server: {
      port: 5173,
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
        },
        // The OAuth surface (login/callback/link/logout/providers) lives on the
        // orchestrator too. Without this the SPA fallback swallowed
        // /auth/login/gitea and the sign-in buttons dead-ended (M6 live find).
        '/auth': {
          target,
          changeOrigin: true,
        },
        // Device uplink (register/heartbeat/poll) — local jcode connectors in
        // dev talk to the same origin as the SPA, mirroring the nginx template.
        '/internal': {
          target,
          changeOrigin: true,
        },
      },
    },
    build: {
      outDir: 'dist',
      sourcemap: true,
    },
    test: {
      globals: true,
      environment: 'jsdom',
      // Include the shared package's suites: they moved to
      // packages/device-ui (M6) but still run in this harness so
      // `pnpm test` keeps covering them.
      include: [
        'src/**/*.{test,spec}.{ts,tsx}',
        'packages/device-ui/src/**/*.{test,spec}.{ts,tsx}',
        '../mobile/src/**/*.{test,spec}.{ts,tsx}',
      ],
      setupFiles: ['src/test/setup.ts'],
    },
  };
});
