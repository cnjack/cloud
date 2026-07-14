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
import { ArrowRight, Check, GithubLogo, GitBranch, Key, Lock } from '@phosphor-icons/react';
import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '../auth/AuthProvider';
import { Wordmark } from '../components/Wordmark';
import { LanguageToggle } from '../components/LanguageToggle';
import { ThemeToggle } from '../components/ThemeToggle';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { LoadingBlock } from '../components/States';
import styles from './OnboardingGate.module.css';

/** The deploy README distilled to the three commands that fix "unreachable". */
const SETUP_STEPS: Array<{ cmd: string; whatKey: string }> = [
  { cmd: 'cd cloud/deploy && make build', whatKey: 'onboarding.setupStep1' },
  { cmd: 'make up', whatKey: 'onboarding.setupStep2' },
  { cmd: 'make port-forward', whatKey: 'onboarding.setupStep3' },
];

function CommandRow({ cmd, what }: { cmd: string; what: string }) {
  const { t } = useTranslation();
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
        {copied ? t('common.copied') : t('common.copy')}
      </Button>
    </div>
  );
}

type GateVariant = 'probing' | 'setup' | 'signin' | 'welcome' | 'landing';

function GateAside({ variant }: { variant: GateVariant }) {
  const { t } = useTranslation();
  if (variant === 'welcome') {
    return (
      <aside className={styles.aside}>
        <span className={styles.eyebrow}>{t('onboarding.assignedRole')}</span><Key size={24} aria-hidden="true" />
        <h3>{t('onboarding.clusterAdministrator')}</h3><p>{t('onboarding.adminSeparate')}</p>
        <ul className={styles.roleList}><li><Check size={14} />{t('onboarding.roleManageGrants')}</li><li><Check size={14} />{t('onboarding.roleConfigureConnections')}</li><li><Check size={14} />{t('onboarding.roleViewCapacity')}</li></ul>
      </aside>
    );
  }
  if (variant === 'signin') {
    return <aside className={styles.aside}><span className={styles.eyebrow}>{t('onboarding.sessionBoundary')}</span><h3>{t('onboarding.whatSignInEstablishes')}</h3><p>{t('onboarding.signInEstablishesBody')}</p><div className={styles.security}><Lock size={14} /><span>{t('onboarding.providerTokensServerSide')}</span></div></aside>;
  }
  if (variant === 'setup' || variant === 'probing') {
    return <aside className={styles.aside}><span className={styles.eyebrow}>{t('onboarding.currentProbe')}</span><h3>{t('onboarding.whatConsoleKnows')}</h3><p>{t('onboarding.probeBody')}</p><dl className={styles.probeFacts}><div><dt>{t('onboarding.probeTarget')}</dt><dd>localhost:8080</dd></div><div><dt>{t('onboarding.probeResponse')}</dt><dd>{variant === 'setup' ? t('onboarding.probeUnreachable') : t('onboarding.probeWaiting')}</dd></div></dl></aside>;
  }
  return null;
}

function GateFrame({ children, variant }: { children: ReactNode; variant: GateVariant }) {
  const { t } = useTranslation();
  const copy = {
    eyebrow: t(`onboarding.${variant}Eyebrow`),
    title: t(`onboarding.${variant}Title`),
    story: t(`onboarding.${variant}Story`),
    utility: t(`onboarding.${variant}Utility`),
    status: t(`onboarding.${variant}Status`),
  };
  return (
    <div className={styles.frame} data-variant={variant}>
      <aside className={styles.rail}>
        <div className={styles.brand}><Wordmark /></div>
        <div className={styles.story}><span className={styles.eyebrow}>{copy.eyebrow}</span><h1>{copy.title}</h1><span className={styles.storyLine} /><p>{copy.story}</p></div>
        <footer className={styles.railFooter}><span>{t('onboarding.selfHosted')}</span><span>v0.1.0</span></footer>
      </aside>
      <main className={styles.surface}>
        <header className={styles.utility}><span>{copy.utility}</span><div><span className={styles.state}>{copy.status}</span><LanguageToggle /><ThemeToggle /></div></header>
        <div className={styles.stage}><div className={styles.content}>{children}<GateAside variant={variant} /></div></div>
      </main>
    </div>
  );
}

function SetupGuide() {
  const { t } = useTranslation();
  const { retryProbe } = useAuth();
  return (
    <GateFrame variant="setup">
      <Card className={styles.card} data-testid="setup-guide">
        <h1 className={styles.title}>{t('onboarding.cantReachTitle')}</h1>
        <p className={styles.lede}>
          {t('onboarding.setupLede1')} <code>/api</code> {t('onboarding.setupLede2')}{' '}
          <code>localhost:8080</code>{t('onboarding.setupLede3')}
        </p>
        <div className={styles.cmdList}>
          {SETUP_STEPS.map((s) => (
            <CommandRow key={s.cmd} cmd={s.cmd} what={t(s.whatKey)} />
          ))}
        </div>
        <div className={styles.footerRow}>
          <span className={styles.autoNote} role="status">
            <span className={styles.pulse} aria-hidden />
            {t('onboarding.recheckingNote')}
          </span>
          <Button variant="secondary" size="sm" onClick={retryProbe}>
            {t('onboarding.checkNow')}
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

const REASON_KEY: Record<string, string | null> = {
  none: null,
  rejected: 'onboarding.reasonRejected',
  expired: 'onboarding.reasonExpired',
  'signed-out': 'onboarding.reasonSignedOut',
};

function ProviderButtons() {
  const { t } = useTranslation();
  const { providers } = useAuth();
  if (providers.length === 0) return null;
  return (
    <div className={styles.providers} data-testid="provider-buttons">
      {providers.map((p) => (
        // A full navigation to the server route (NOT client routing) so the OAuth
        // round trip + Set-Cookie happen on the orchestrator.
        <a key={p.id} href={p.login_url} className={styles.provider} data-provider={p.id}>
          <span className={styles.providerIcon} aria-hidden>{p.id.toLowerCase().includes('github') ? <GithubLogo size={18} weight="fill" /> : <GitBranch size={18} />}</span>
          <span>{t('onboarding.continueWith', { name: p.name })}</span>
          <ArrowRight size={16} aria-hidden="true" />
        </a>
      ))}
    </div>
  );
}

function SignIn() {
  const { t } = useTranslation();
  const { login, reason, providers, loginError } = useAuth();
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  // Advanced (console token) is collapsed when OAuth providers exist, and
  // auto-expanded when there are none. A manual toggle overrides the default.
  const [advManual, setAdvManual] = useState<boolean | null>(null);
  const noProviders = providers.length === 0;
  const advOpen = advManual ?? noProviders;
  const reasonKey = REASON_KEY[reason];
  const notice = loginError ?? (reasonKey ? t(reasonKey) : null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    const res = await login(token);
    setBusy(false);
    if (!res.ok) setError(res.error);
  };

  return (
    <GateFrame variant="signin">
      <Card className={styles.card} data-testid="sign-in">
        <h1 className={styles.title}>{t('onboarding.signInHeading')}</h1>
        <p className={styles.lede}>
          {noProviders
            ? t('onboarding.signInLedeNoProviders')
            : t('onboarding.signInLedeProviders')}
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
              {t('onboarding.advancedConsoleToken')}
            </button>
          )}
          {advOpen && (
            <form onSubmit={submit} className={styles.form} data-testid="console-token-form">
              <TextField
                label={t('onboarding.consoleTokenLabel')}
                type="password"
                autoComplete="off"
                autoFocus={noProviders}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                error={error}
                placeholder="dev-console-token"
                hint={t('onboarding.consoleTokenHint')}
              />
              <Button type="submit" variant={noProviders ? 'primary' : 'secondary'} loading={busy}>
                {t('onboarding.signInWithToken')}
              </Button>
            </form>
          )}
        </div>
      </Card>
    </GateFrame>
  );
}

function WelcomeCard() {
  const { t } = useTranslation();
  const { welcome, dismissWelcome, me } = useAuth();
  const firstAdmin = welcome === 'first-admin';
  return (
    <GateFrame variant="welcome">
      <Card className={styles.card} data-testid="welcome-card" data-welcome={welcome ?? undefined}>
        <h1 className={styles.title}>
          {firstAdmin ? t('onboarding.welcomeFirstAdminTitle') : t('onboarding.welcomeGreeting', { name: me?.user.display_name ?? t('onboarding.friend') })}
        </h1>
        <p className={styles.lede}>
          {firstAdmin
            ? t('onboarding.welcomeFirstAdminLede')
            : t('onboarding.welcomeLede')}
        </p>
        <div className={styles.footerRow}>
          <span className={styles.autoNote}>{t('onboarding.signedInAs', { name: me?.user.display_name })}</span>
          <Button variant="primary" onClick={dismissWelcome} autoFocus data-testid="welcome-enter">
            {t('onboarding.getStarted')}
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

function Landing() {
  const { t } = useTranslation();
  const { me, enterConsole } = useAuth();
  const identity = me?.identities?.[0];
  return (
    <GateFrame variant="landing">
      <Card className={styles.card} data-testid="landing-card">
        <h1 className={styles.title}>{t('onboarding.landingCardTitle')}</h1>
        <p className={styles.lede}>{t('onboarding.landingLede')}</p>
        <dl className={styles.facts}>
          <div className={styles.fact}>
            <dt>{t('onboarding.principal')}</dt>
            <dd>{me?.user.display_name ?? '—'}</dd>
          </div>
          <div className={styles.fact}>
            <dt>{t('onboarding.access')}</dt>
            <dd>{me?.user.is_cluster_admin ? t('onboarding.accessClusterAdmin') : t('onboarding.accessMember')}</dd>
          </div>
          <div className={styles.fact}>
            <dt>{t('onboarding.session')}</dt>
            <dd>{me?.is_service ? t('onboarding.sessionConsoleToken') : t('onboarding.sessionUser')}</dd>
          </div>
          <div className={styles.fact}>
            <dt>{t('onboarding.identity')}</dt>
            <dd>{identity ? `${identity.provider}/${identity.username}` : '—'}</dd>
          </div>
        </dl>
        <div className={styles.footerRow}>
          <span className={styles.autoNote}>{t('onboarding.everythingHeadless')}</span>
          <Button variant="primary" onClick={enterConsole} autoFocus>
            {t('onboarding.enterConsole')}
          </Button>
        </div>
      </Card>
    </GateFrame>
  );
}

export function OnboardingGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const { status, landing, welcome } = useAuth();

  switch (status) {
    case 'probing':
      return (
        <GateFrame variant="probing">
          <LoadingBlock label={t('onboarding.connecting')} />
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
