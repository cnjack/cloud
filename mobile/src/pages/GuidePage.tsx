/*
 * GuidePage — the in-app user guide (M7). Same copy as the console's
 * /devices/guide (device.guide.*, shipped via the @jcloud/device-ui locale
 * bundles), mobile layout, and the app's own screenshots. Rendered both
 * signed-out (from the login screen, via `onBack`) and signed-in (route
 * /guide, default back link to the device list).
 */
import { ArrowLeft } from '@phosphor-icons/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';

const shot = (name: string) => `${import.meta.env.BASE_URL}guide/${name}.png`;

// [command, device.guide.commands.rows key]
const COMMANDS: [string, string][] = [
  ['jcode login', 'login'],
  ['jcode login --status', 'loginStatus'],
  ['jcode logout', 'logout'],
  ['jcode cloud status', 'cloudStatus'],
  ['jcode cloud pairings', 'pairings'],
  ['jcode cloud approve <pairing_id>', 'approve'],
  ['jcode cloud deny <pairing_id>', 'deny'],
  ['jcode cloud key show-phrase', 'showPhrase'],
  ['jcode cloud key recover', 'recover'],
  ['jcode cloud rotate-key', 'rotateKey'],
];

/** Renders `code` spans inside guide copy (backtick-delimited). */
function Rich({ text }: { text: string }) {
  const parts = text.split('`');
  return (
    <>
      {parts.map((part, i) => (i % 2 === 1 ? <code key={i} className="inline-code">{part}</code> : part))}
    </>
  );
}

function CodeBlock({ children }: { children: string }) {
  return (
    <pre className="guide-code">
      <code>{children}</code>
    </pre>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="guide-section">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

export function GuidePage({ onBack }: { onBack?: () => void }) {
  const { t } = useTranslation();
  const p = (key: string) => <Rich text={t(key)} />;

  const back = onBack ? (
    <button type="button" className="topbar-back" onClick={onBack} aria-label={t('device.guide.back')}>
      <ArrowLeft size={18} />
    </button>
  ) : (
    <Link to="/" className="topbar-back" aria-label={t('device.guide.back')}>
      <ArrowLeft size={18} />
    </Link>
  );

  return (
    <div className="app-shell">
      <header className="topbar">
        {back}
        <div className="topbar-title">{t('device.guide.title')}</div>
      </header>

      <div className="content content-pad-bottom prose" data-testid="guide-page">
        <p className="guide-subtitle">{p('device.guide.subtitle')}</p>

        <Section title={t('device.guide.what.title')}>
          <p>{p('device.guide.what.p1')}</p>
          <p>{p('device.guide.what.p2')}</p>
          <figure className="guide-figure">
            <img src={shot('devices')} alt={t('device.guide.shots.devices')} loading="lazy" />
            <figcaption>{t('device.guide.shots.devices')}</figcaption>
          </figure>
        </Section>

        <Section title={t('device.guide.quickstart.title')}>
          <p>{p('device.guide.quickstart.p1')}</p>
          <CodeBlock>{t('device.guide.quickstart.code1')}</CodeBlock>
          <p>{p('device.guide.quickstart.p2')}</p>
          <p>{p('device.guide.quickstart.p3')}</p>
          <CodeBlock>{t('device.guide.quickstart.code2')}</CodeBlock>
          <figure className="guide-figure">
            <img src={shot('login')} alt={t('device.guide.shots.login')} loading="lazy" />
            <figcaption>{t('device.guide.shots.login')}</figcaption>
          </figure>
        </Section>

        <Section title={t('device.guide.remote.title')}>
          <p>{p('device.guide.remote.p1')}</p>
          <figure className="guide-figure">
            <img src={shot('welcome')} alt={t('device.guide.shots.welcome')} loading="lazy" />
            <figcaption>{t('device.guide.shots.welcome')}</figcaption>
          </figure>
          <figure className="guide-figure">
            <img src={shot('session')} alt={t('device.guide.shots.session')} loading="lazy" />
            <figcaption>{t('device.guide.shots.session')}</figcaption>
          </figure>
          <p>{p('device.guide.remote.p2')}</p>
        </Section>

        <Section title={t('device.guide.pairing.title')}>
          <p>{p('device.guide.pairing.p1')}</p>
          <p>{p('device.guide.pairing.p2')}</p>
          <CodeBlock>{t('device.guide.pairing.code1')}</CodeBlock>
          <CodeBlock>{t('device.guide.pairing.code2')}</CodeBlock>
          <p>{p('device.guide.pairing.p3')}</p>
        </Section>

        <Section title={t('device.guide.keys.title')}>
          <p>{p('device.guide.keys.p1')}</p>
          <CodeBlock>{t('device.guide.keys.code1')}</CodeBlock>
          <p>{p('device.guide.keys.p2')}</p>
          <CodeBlock>{t('device.guide.keys.code2')}</CodeBlock>
          <p>{p('device.guide.keys.p3')}</p>
          <CodeBlock>{t('device.guide.keys.code3')}</CodeBlock>
        </Section>

        <Section title={t('device.guide.commands.title')}>
          <p>{p('device.guide.commands.p1')}</p>
          <table className="guide-table">
            <tbody>
              {COMMANDS.map(([cmd, key]) => (
                <tr key={cmd}>
                  <td>
                    <code className="inline-code">{cmd}</code>
                  </td>
                  <td>{t(`device.guide.commands.rows.${key}`)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="guide-note">{p('device.guide.commands.e2eeNote')}</p>
        </Section>
      </div>
    </div>
  );
}
