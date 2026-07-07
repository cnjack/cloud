/*
 * OnboardingGate — full-screen gate in front of the console, driven by the
 * AuthProvider state machine:
 *
 *   probing            → connecting splash
 *   unreachable        → setup guide (copyable commands, auto-reprobe every 3s)
 *   unauthenticated    → sign-in: OAuth provider buttons + Advanced console token
 *   ready + welcome    → OAuth welcome card (first-admin / new)
 *   ready + landing    → manual console-token landing card
 *   ready              → the app
 *
 * Demo mode (VITE_DEMO=1) never reaches this file's screens: AuthProvider boots
 * straight to 'ready' with a synthetic principal.
 */
import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useAuth } from '../auth/AuthProvider';
import { Wordmark } from '../components/Wordmark';
import { ThemeToggle } from '../components/ThemeToggle';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { LoadingBlock } from '../components/States';
import styles from './OnboardingGate.module.css';

/** The deploy README distilled to the three commands that fix "unreachable". */
const SETUP_STEPS: Array<{ cmd: string; what: string }> = [
  { cmd: 'cd cloud/deploy && make build', what: 'Build the four local images' },
  { cmd: 'make up', what: 'Deploy to the OrbStack cluster and wait for rollouts' },
  { cmd: 'make port-forward', what: 'Forward the orchestrator API to localhost:8080' },
];

function CommandRow({ cmd, what }: { cmd: string; what: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — the text is selectable anyway */
    }
  };
  return (
    <div className={styles.cmdRow}>
      <div className={styles.cmdMain}>
        <code className={styles.cmd}>{cmd}</code>
        <span className={styles.cmdWhat}>{what}</span>
      </div>
      <Button variant="ghost" size="sm" onClick={copy}>
        {copied ? 'Copied' : 'Copy'}
      </Button>
    </div>
  );
}

function GateFrame({ children }: { children: ReactNode }) {
  return (
    <div className={styles.frame}>
      <div className={styles.brand}>
        <Wordmark />
        <ThemeToggle />
      </div>
      {children}
    </div>
  );
}

function SetupGuide() {
  const { retryProbe } = useAuth();
  return (
    <GateFrame>
      <Card className={styles.card} data-testid="setup-guide">
        <h1 className={styles.title}>Can&rsquo;t reach the orchestrator</h1>
        <p className={styles.lede}>
          The console proxies <code>/api</code> to{' '}
          <code>localhost:8080</code>, but nothing answered. Bring the stack up,
          then this page moves on by itself.
        </p>
        <div className={styles.cmdList}>
          {SETUP_STEPS.map((s) => (
            <CommandRow key={s.cmd} cmd={s.cmd} what={s.what} />
          ))}
        </div>
        <div className={styles.footerRow}>
          <span className={styles.autoNote} role="status">
            <span className={styles.pulse} aria-hidden />
            Re-checking every 3s — no refresh needed
          </span>
          <Button variant="secondary" size="sm" onClick={retryProbe}>
            Check now
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

const REASON_COPY: Record<string, string | null> = {
  none: null,
  rejected: 'The saved token was rejected — it may have been rotated. Enter the current one.',
  expired: 'Your session ended (expired or revoked). Sign in again.',
  'signed-out': 'Signed out.',
};

function ProviderButtons() {
  const { providers } = useAuth();
  if (providers.length === 0) return null;
  return (
    <div className={styles.providers} data-testid="provider-buttons">
      {providers.map((p) => (
        // A full navigation to the server route (NOT client routing) so the OAuth
        // round trip + Set-Cookie happen on the orchestrator.
        <a key={p.id} href={p.login_url} className={styles.provider} data-provider={p.id}>
          <span className={styles.providerIcon} aria-hidden>
            {p.name.charAt(0)}
          </span>
          <span>Continue with {p.name}</span>
        </a>
      ))}
    </div>
  );
}

function SignIn() {
  const { login, reason, providers, loginError } = useAuth();
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  // Advanced (console token) is collapsed when OAuth providers exist, and
  // auto-expanded when there are none. A manual toggle overrides the default.
  const [advManual, setAdvManual] = useState<boolean | null>(null);
  const noProviders = providers.length === 0;
  const advOpen = advManual ?? noProviders;
  const notice = loginError ?? REASON_COPY[reason] ?? null;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    const res = await login(token);
    setBusy(false);
    if (!res.ok) setError(res.error);
  };

  return (
    <GateFrame>
      <Card className={styles.card} data-testid="sign-in">
        <h1 className={styles.title}>Sign in</h1>
        <p className={styles.lede}>
          {noProviders
            ? 'No OAuth provider is configured. Use the console token the orchestrator was deployed with.'
            : 'Continue with your git provider to get a personal, per-user session.'}
        </p>
        {notice && (
          <p
            className={loginError ? styles.errorNotice : styles.notice}
            role="status"
            data-testid="signin-notice"
          >
            {notice}
          </p>
        )}

        <ProviderButtons />

        <div className={styles.advanced} data-testid="advanced">
          {!noProviders && (
            <button
              type="button"
              className={styles.advToggle}
              aria-expanded={advOpen}
              onClick={() => setAdvManual(!advOpen)}
              data-testid="advanced-toggle"
            >
              <span className={styles.advCaret} data-open={advOpen || undefined} aria-hidden>
                ▸
              </span>
              Advanced: console token
            </button>
          )}
          {advOpen && (
            <form onSubmit={submit} className={styles.form} data-testid="console-token-form">
              <TextField
                label="Console token"
                type="password"
                autoComplete="off"
                autoFocus={noProviders}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                error={error}
                placeholder="dev-console-token"
                hint="The CONSOLE_TOKEN the orchestrator was deployed with — cluster-admin. Stored locally in this browser."
              />
              <Button type="submit" variant={noProviders ? 'primary' : 'secondary'} loading={busy}>
                Sign in with token
              </Button>
            </form>
          )}
        </div>
      </Card>
    </GateFrame>
  );
}

function WelcomeCard() {
  const { welcome, dismissWelcome, me } = useAuth();
  const firstAdmin = welcome === 'first-admin';
  return (
    <GateFrame>
      <Card className={styles.card} data-testid="welcome-card" data-welcome={welcome ?? undefined}>
        <h1 className={styles.title}>
          {firstAdmin ? 'You’re the first user — cluster admin' : `Welcome, ${me?.user.display_name ?? 'friend'}`}
        </h1>
        <p className={styles.lede}>
          {firstAdmin
            ? 'You signed in first, so you’re now the cluster administrator: you can see every project and manage capacity. Everyone who joins after you starts as a regular user.'
            : 'You’re signed in. Create a project to point jcode Cloud at a repository, or open one you’ve been added to.'}
        </p>
        <div className={styles.footerRow}>
          <span className={styles.autoNote}>Signed in as {me?.user.display_name}</span>
          <Button variant="primary" onClick={dismissWelcome} autoFocus data-testid="welcome-enter">
            Get started
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

function Landing() {
  const { me, enterConsole } = useAuth();
  const identity = me?.identities?.[0];
  return (
    <GateFrame>
      <Card className={styles.card} data-testid="landing-card">
        <h1 className={styles.title}>You&rsquo;re in — cluster admin</h1>
        <p className={styles.lede}>Signed in to this orchestrator:</p>
        <dl className={styles.facts}>
          <div className={styles.fact}>
            <dt>Principal</dt>
            <dd>{me?.user.display_name ?? '—'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Access</dt>
            <dd>{me?.user.is_cluster_admin ? 'cluster admin' : 'member'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Session</dt>
            <dd>{me?.is_service ? 'console token' : 'user session'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Identity</dt>
            <dd>{identity ? `${identity.provider}/${identity.username}` : '—'}</dd>
          </div>
        </dl>
        <div className={styles.footerRow}>
          <span className={styles.autoNote}>Everything runs headless in your cluster.</span>
          <Button variant="primary" onClick={enterConsole} autoFocus>
            Enter console
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

export function OnboardingGate({ children }: { children: ReactNode }) {
  const { status, landing, welcome } = useAuth();

  switch (status) {
    case 'probing':
      return (
        <GateFrame>
          <LoadingBlock label="Connecting to the orchestrator…" />
        </GateFrame>
      );
    case 'unreachable':
      return <SetupGuide />;
    case 'unauthenticated':
      return <SignIn />;
    case 'ready':
      if (welcome) return <WelcomeCard />;
      return landing ? <Landing /> : <>{children}</>;
  }
}
