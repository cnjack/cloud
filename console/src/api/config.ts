/*
 * config.ts — resolves runtime config from Vite env vars (all VITE_-prefixed so
 * they're safe to expose to the browser bundle).
 */
export interface RuntimeConfig {
  demo: boolean;
  consoleToken: string | undefined;
}

export function loadConfig(): RuntimeConfig {
  const env = import.meta.env;
  return {
    demo: env.VITE_DEMO === '1' || env.VITE_DEMO === 'true',
    consoleToken: env.VITE_CONSOLE_TOKEN || undefined,
  };
}
