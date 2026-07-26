import { ShieldCheck } from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import type { ProviderKind } from '../api/types';
import { useApi } from '../api/ApiProvider';
import { Button } from '../components/Button';
import { SelectField, TextField } from '../components/Field';
import { LanguageToggle } from '../components/LanguageToggle';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ThemeToggle } from '../components/ThemeToggle';
import { Wordmark } from '../components/Wordmark';
import styles from './SetupPage.module.css';

const PROVIDER_OPTIONS = [
  { value: 'github', label: 'GitHub' },
  { value: 'gitlab', label: 'GitLab' },
  { value: 'gitea', label: 'Gitea' },
];

/**
 * The only unauthenticated configuration screen.  It completes setup in one
 * request so an empty cluster can never be left with a public URL but no way
 * for a person to authenticate.
 */
export function SetupPage() {
  const api = useApi();
  const [status, setStatus] = useState<Awaited<ReturnType<typeof api.getSetupStatus>> | null>(null);
  const [url, setUrl] = useState('');
  const [provider, setProvider] = useState<Exclude<ProviderKind, 'jtype'>>('github');
  const [baseUrl, setBaseUrl] = useState('https://github.com');
  const [clientId, setClientId] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = () => void api.getSetupStatus().then((next) => {
    setStatus(next);
    // An empty string means setup has no stored value yet. In that case the
    // browser origin is the most accurate public entry, including scheme and
    // any non-default port used by a local deployment.
    setUrl(next.setup_required ? window.location.origin : (next.public_url?.trim() || window.location.origin));
    setError('');
  }).catch((reason) => setError(reason instanceof Error ? reason.message : 'Could not load setup status.'));
  useEffect(load, []);
  const changeProvider = (next: Exclude<ProviderKind, 'jtype'>) => {
    setProvider(next); setBaseUrl(next === 'github' ? 'https://github.com' : '');
  };

  if (error && !status) return <div className={styles.page}><main className={styles.statePanel}><ErrorBlock error={new Error(error)} onRetry={load} /></main></div>;
  if (!status) return <div className={styles.page}><main className={styles.statePanel}><LoadingBlock label="Loading setup…" /></main></div>;
  if (!status.setup_required) { window.location.replace('/'); return null; }

  const submit = (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError('');
    void api.updateSetup({
      public_url: url.trim(),
      provider: { provider, base_url: baseUrl.trim(), login_enabled: true, plugin_enabled: true, client_id: clientId.trim(), client_secret: clientSecret },
    }).then(() => window.location.assign('/')).catch((reason) => {
      setBusy(false); setError(reason instanceof Error ? reason.message : 'Setup could not be saved.');
    });
  };

  const publicOrigin = (url.trim() || window.location.origin).replace(/\/+$/, '');
  const callbackUrl = `${publicOrigin}/auth/callback/${provider}`;

  return <div className={styles.page} data-testid="first-visitor-setup">
    <header className={styles.topbar}>
      <Wordmark />
      <div className={styles.utilities}><LanguageToggle /><ThemeToggle /></div>
    </header>
    <main className={styles.main}>
      <section className={styles.panel}>
        <div className={styles.intro}>
          <span className={styles.eyebrow}>Cluster setup</span>
          <h1 className={styles.title}>Set up jcode Cloud</h1>
          <p className={styles.lede}>Connect one login provider to initialize this cluster.</p>
          <div className={styles.securityNote}>
            <ShieldCheck size={20} aria-hidden="true" />
            <div><strong>First sign-in owns setup</strong><span>The first successful sign-in becomes Cluster Admin.</span></div>
          </div>
        </div>
        <form className={styles.form} onSubmit={submit}>
          <TextField label="Cloud public URL" type="url" autoComplete="url" value={url} onChange={(event) => setUrl(event.target.value)} required hint="Detected from this browser. Used for OAuth callbacks and webhook URLs." />
          <SelectField label="Login provider" value={provider} options={PROVIDER_OPTIONS} onChange={(next) => changeProvider(next as Exclude<ProviderKind, 'jtype'>)} required />
          <TextField label="Provider URL" type="url" autoComplete="url" value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} disabled={provider === 'github'} required hint={provider === 'github' ? 'GitHub is fixed to github.com.' : 'The browser-facing provider origin.'} />
          <TextField label="OAuth client ID" autoComplete="off" value={clientId} onChange={(event) => setClientId(event.target.value)} required />
          <TextField label="OAuth client secret" type="password" autoComplete="new-password" value={clientSecret} onChange={(event) => setClientSecret(event.target.value)} required hint="Stored encrypted and never shown again." />
          <div className={styles.callback}>
            <span>OAuth callback URL</span>
            <code>{callbackUrl}</code>
          </div>
          {error && <p className={styles.error} role="alert">{error}</p>}
          <Button className={styles.submit} type="submit" loading={busy} disabled={!url.trim() || !baseUrl.trim() || !clientId.trim() || !clientSecret}>Save and continue</Button>
        </form>
      </section>
    </main>
  </div>;
}
