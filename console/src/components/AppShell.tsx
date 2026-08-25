import type { ReactNode } from 'react';
import { matchPath, useLocation } from 'react-router-dom';
import { AccountHeader } from './AccountHeader';
import styles from './AppShell.module.css';

const FULL_WORKSPACE_ROUTES = [
  '/runs/:runId',
  '/devices/:deviceId/sessions/:sessionId',
] as const;

const OWN_HEADER_ROUTES = [
  '/',
  '/repositories',
  '/account/settings',
  '/devices/guide',
  '/setup',
] as const;

function matches(pathname: string, pattern: string) {
  return !!matchPath({ path: pattern, end: true }, pathname);
}

/**
 * One account-level information architecture: full-canvas workspaces for
 * conversations and runs, purpose-built headers for primary surfaces, and the
 * shared account header for every other utility page.
 */
export function AppShell({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const fullWorkspace = FULL_WORKSPACE_ROUTES.some((route) => matches(pathname, route));
  const ownsHeader = OWN_HEADER_ROUTES.some((route) => matches(pathname, route));
  const deviceAuthorization = matches(pathname, '/device');

  if (fullWorkspace) {
    const isRun = matches(pathname, '/runs/:runId');
    const isDeviceSession = matches(pathname, '/devices/:deviceId/sessions/:sessionId');
    return (
      <div
        className={styles.workspaceShell}
        data-run-workspace={isRun || undefined}
        data-device-session={isDeviceSession || undefined}
      >
        <main className={styles.workspaceContent}>{children}</main>
      </div>
    );
  }

  // Device-code authorization is a security boundary, not a workspace page.
  if (deviceAuthorization) {
    return <div className={styles.standaloneAuth} data-device-authorization="true">{children}</div>;
  }

  if (ownsHeader) {
    const isWorkHome = matches(pathname, '/') || matches(pathname, '/repositories');
    return <div className={styles.accountSurface} data-work-home={isWorkHome || undefined}>{children}</div>;
  }

  return (
    <div className={styles.accountSurface}>
      <AccountHeader />
      <main className={styles.accountSurfaceContent}>{children}</main>
    </div>
  );
}
