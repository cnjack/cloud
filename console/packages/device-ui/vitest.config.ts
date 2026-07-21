/// <reference types="vitest/config" />
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const rootDir = dirname(fileURLToPath(import.meta.url));

// Mirrors the console's vitest setup (same vite major, jsdom, globals) so the
// moved suites behave identically whether run here or from the console root.
export default defineConfig({
  root: rootDir,
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    setupFiles: ['src/test/setup.ts'],
  },
});
