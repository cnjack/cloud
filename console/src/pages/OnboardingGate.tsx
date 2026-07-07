/*
 * OnboardingGate — full-screen gate in front of the console, driven by the
 * AuthProvider state machine:
 *
 *   probing         → connecting splash
 *   unreachable     → setup guide (copyable commands, auto-reprobe every 3s)
 *   unauthenticated → sign-in (console token, masked)
 *   ready + landing → landing card (cluster snapshot) right after a manual sign-in
 *   ready           → the app
 *
 * Demo mode (VITE_DEMO=1) never reaches this file's screens: AuthProvider
 * boots straight to 'ready'.
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
  expired: 'Your session token stopped working (rotated or revoked). Sign in again.',
  'signed-out': 'Signed out.',
};

function SignIn() {
  const { login, reason } = useAuth();
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const notice = REASON_COPY[reason] ?? null;

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
          Paste the console token — the <code>CONSOLE_TOKEN</code> the
          orchestrator was deployed with. Holding it makes you the cluster
          admin of this single-tenant console.
        </p>
        {notice && (
          <p className={styles.notice} role="status">
            {notice}
          </p>
        )}
        <form onSubmit={submit} className={styles.form}>
          <TextField
            label="Console token"
            type="password"
            autoComplete="off"
            autoFocus
            value={token}
            onChange={(e) => setToken(e.target.value)}
            error={error}
            placeholder="dev-console-token"
            hint="Stored locally in this browser. OIDC sign-in is on the roadmap."
          />
          <Button type="submit" variant="primary" loading={busy}>
            Sign in
          </Button>
        </form>
      </Card>
    </GateFrame>
  );
}

function Landing() {
  const { system, enterConsole } = useAuth();
  return (
    <GateFrame>
      <Card className={styles.card} data-testid="landing-card">
        <h1 className={styles.title}>You&rsquo;re in — cluster admin</h1>
        <p className={styles.lede}>Connected to this orchestrator:</p>
        <dl className={styles.facts}>
          <div className={styles.fact}>
            <dt>Version</dt>
            <dd>
              {system?.version.version ?? '—'}
              {system?.version.commit && system.version.commit !== 'none'
                ? ` (${system.version.commit.slice(0, 7)})`
                : ''}
            </dd>
          </div>
          <div className={styles.fact}>
            <dt>Namespace</dt>
            <dd>{system?.namespace ?? '—'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Launcher</dt>
            <dd>{system?.launcher ?? '—'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Gitea draft-PR</dt>
            <dd>{system?.provider.gitea_enabled ? 'enabled' : 'off'}</dd>
          </div>
        </dl>
        <div className={styles.footerRow}>
          <span className={styles.autoNote}>
            Capacity {system?.capacity.running ?? 0} running ·{' '}
            {system?.capacity.queued ?? 0} queued
          </span>
          <Button variant="primary" onClick={enterConsole} autoFocus>
            Enter console
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

export function OnboardingGate({ children }: { children: ReactNode }) {
  const { status, landing } = useAuth();

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
      return landing ? <Landing /> : <>{children}</>;
  }
}
