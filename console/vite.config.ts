/// <reference types="vitest/config" />
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// Resolve env/root from THIS file's directory, not process.cwd(), so the server
// works no matter where it's launched from (e.g. the repo-root preview harness).
const rootDir = dirname(fileURLToPath(import.meta.url));

// The console talks to the orchestrator over /api/v1/*. In dev we proxy that
// prefix to the orchestrator (default http://localhost:8080). In demo mode
// (VITE_DEMO=1) the app never hits the network — see src/api/mockClient.ts.
//
// Vitest config lives here too (single vite version) to avoid the vite5/vite6
// Plugin type clash that a separate vitest.config.ts would introduce.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, rootDir, '');
  const target = env.VITE_API_PROXY_TARGET || 'http://localhost:8080';

  return {
    root: rootDir,
    plugins: [react()],
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
      },
    },
    build: {
      outDir: 'dist',
      sourcemap: true,
    },
    test: {
      globals: true,
      environment: 'jsdom',
      include: ['src/**/*.{test,spec}.{ts,tsx}'],
      setupFiles: ['src/test/setup.ts'],
    },
  };
});
