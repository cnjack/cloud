import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import type { ProviderKind } from '../api/types';
import { useApi } from '../api/ApiProvider';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { ErrorBlock, LoadingBlock } from '../components/States';
import styles from './OnboardingGate.module.css';

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
    setStatus(next); setUrl(next.public_url ?? window.location.origin);
  }).catch((reason) => setError(reason instanceof Error ? reason.message : 'Could not load setup status.'));
  useEffect(load, []);
  const changeProvider = (next: Exclude<ProviderKind, 'jtype'>) => {
    setProvider(next); setBaseUrl(next === 'github' ? 'https://github.com' : '');
  };

  if (error) return <div className={styles.frame}><main className={styles.surface}><ErrorBlock error={new Error(error)} onRetry={load} /></main></div>;
  if (!status) return <div className={styles.frame}><main className={styles.surface}><LoadingBlock label="Loading setup…" /></main></div>;
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

  return <div className={styles.frame} data-testid="first-visitor-setup"><main className={styles.surface}><div className={styles.stage}><section className={styles.card}>
    <h1 className={styles.title}>Set up jcode Cloud</h1>
    <p className={styles.lede}>You are configuring this new cluster. The first successful sign-in becomes Cluster Admin.</p>
    <form className={styles.form} onSubmit={submit}>
      <TextField label="Cloud public URL" value={url} onChange={(event) => setUrl(event.target.value)} required hint="Used for OAuth callbacks and webhook URLs." />
      <label className={styles.field}><span>Login provider</span><select value={provider} onChange={(event) => changeProvider(event.target.value as Exclude<ProviderKind, 'jtype'>)}><option value="github">GitHub</option><option value="gitlab">GitLab</option><option value="gitea">Gitea</option></select></label>
      <TextField label="Provider URL" value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} disabled={provider === 'github'} required hint={provider === 'github' ? 'GitHub is fixed to github.com.' : 'The browser-facing provider origin.'} />
      <TextField label="OAuth client ID" value={clientId} onChange={(event) => setClientId(event.target.value)} required />
      <TextField label="OAuth client secret" type="password" value={clientSecret} onChange={(event) => setClientSecret(event.target.value)} required hint="Stored encrypted and never shown again." />
      <Button type="submit" loading={busy} disabled={!url.trim() || !baseUrl.trim() || !clientId.trim() || !clientSecret}>Save and continue</Button>
    </form>
  </section></div></main></div>;
}
