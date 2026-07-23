/// <reference types="vite/client" />

declare const __JCLOUD_VERSION__: string;

interface ImportMetaEnv {
  readonly VITE_DEMO?: string;
  readonly VITE_DEMO_SEED?: string;
  readonly VITE_DEMO_SPEED?: string;
  readonly VITE_CONSOLE_TOKEN?: string;
  readonly VITE_API_PROXY_TARGET?: string;
  readonly VITE_APP_VERSION?: string;
  /** Console role signal: 'cluster-admin' (default) | 'project-admin'. Not authz. */
  readonly VITE_ROLE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
