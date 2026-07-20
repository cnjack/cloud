import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@jcloud/device-ui';
import { DEFAULT_CLOUD_URL, useMobileAuth, validateCloudUrl } from '../auth';

/**
 * LoginPage — cloud URL (https; http only for loopback dev rigs) + a user
 * session token. Validated against GET /api/v1/me before persisting.
 * `onGuide` opens the in-app user guide (M7) without signing in.
 */
export function LoginPage({ onGuide }: { onGuide?: () => void }) {
  const { t } = useTranslation();
  const auth = useMobileAuth();
  const [cloudUrl, setCloudUrl] = useState(DEFAULT_CLOUD_URL);
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    setError(null);
    const url = validateCloudUrl(cloudUrl);
    if (!url.ok) {
      setError(t(url.reason === 'http_not_allowed' ? 'mobile.login.httpNotAllowed' : 'mobile.login.invalidUrl'));
      return;
    }
    setBusy(true);
    try {
      const result = await auth.login(url.url, token);
      if (!result.ok) {
        setError(
          result.reason === 'unauthorized'
            ? t('mobile.login.unauthorized')
            : result.reason === 'unreachable'
              ? t('mobile.login.unreachable')
              : t('mobile.login.failed', { message: result.message ?? '?' }),
        );
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="app-shell">
      <div className="content content-pad-bottom">
        <form className="login-wrap" onSubmit={submit} data-testid="login-page">
          <div className="login-brand">
            <span className="login-logo" aria-hidden>JC</span>
            <div>
              <h1 className="login-title">{t('mobile.login.title')}</h1>
              <p className="login-subtitle">{t('mobile.login.subtitle')}</p>
            </div>
          </div>

          <label className="field">
            <span className="field-label">{t('mobile.login.cloudUrl')}</span>
            <input
              className="text-input"
              type="url"
              inputMode="url"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={cloudUrl}
              onChange={(e) => setCloudUrl(e.target.value)}
              placeholder={DEFAULT_CLOUD_URL}
              data-testid="login-cloud-url"
            />
          </label>

          <label className="field">
            <span className="field-label">{t('mobile.login.token')}</span>
            <input
              className="text-input"
              type="password"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              data-testid="login-token"
            />
            <span className="field-hint">{t('mobile.login.tokenHint')}</span>
          </label>

          {error && <p className="form-error" role="alert" data-testid="login-error">{error}</p>}

          <Button type="submit" variant="primary" disabled={!token.trim()} loading={busy}>
            {busy ? t('mobile.login.signingIn') : t('mobile.login.submit')}
          </Button>

          {onGuide && (
            <button type="button" className="login-guide" onClick={onGuide} data-testid="login-guide">
              {t('device.guide.entry')}
            </button>
          )}
        </form>
      </div>
    </div>
  );
}
