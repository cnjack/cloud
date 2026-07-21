/*
 * DeviceGuidePage — in-app user guide for the device relay (M7). Static,
 * i18n-driven content (device.guide.*) with screenshots from public/guide/.
 * The same copy powers the mobile app's guide page (via the @jcloud/device-ui
 * locale bundles) and `jcode cloud guide`.
 */
import { ArrowLeft } from '@phosphor-icons/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { PageHeader, SurfaceInner } from '../components/PageLayout';
import styles from './DeviceGuidePage.module.css';

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
      {parts.map((part, i) =>
        i % 2 === 1 ? <code key={i} className={styles.inlineCode}>{part}</code> : part,
      )}
    </>
  );
}

function Figure({ name, caption }: { name: string; caption: string }) {
  return (
    <figure className={styles.figure}>
      <img src={shot(name)} alt={caption} loading="lazy" />
      <figcaption>{caption}</figcaption>
    </figure>
  );
}

function CodeBlock({ children }: { children: string }) {
  return <pre className={styles.code}><code>{children}</code></pre>;
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h2>{title}</h2>
      {children}
    </section>
  );
}

export function DeviceGuidePage() {
  const { t } = useTranslation();
  const p = (key: string) => <Rich text={t(key)} />;

  return (
    <SurfaceInner>
      <div data-testid="device-guide">
        <PageHeader
          eyebrow={t('device.list.title')}
          title={t('device.guide.title')}
          description={t('device.guide.subtitle')}
          actions={
            <Link to="/devices" className={styles.back}>
              <ArrowLeft size={14} aria-hidden="true" />
              {t('device.guide.back')}
            </Link>
          }
        />

        <div className={styles.stack}>
          <Section title={t('device.guide.what.title')}>
            <p>{p('device.guide.what.p1')}</p>
            <p>{p('device.guide.what.p2')}</p>
          </Section>

          <Section title={t('device.guide.quickstart.title')}>
            <p>{p('device.guide.quickstart.p1')}</p>
            <CodeBlock>{t('device.guide.quickstart.code1')}</CodeBlock>
            <p>{p('device.guide.quickstart.p2')}</p>
            <p>{p('device.guide.quickstart.p3')}</p>
            <CodeBlock>{t('device.guide.quickstart.code2')}</CodeBlock>
          </Section>

          <Section title={t('device.guide.remote.title')}>
            <p>{p('device.guide.remote.p1')}</p>
            <div className={styles.shots}>
              <Figure name="devices" caption={t('device.guide.shots.devices')} />
              <Figure name="welcome" caption={t('device.guide.shots.welcome')} />
              <Figure name="session" caption={t('device.guide.shots.session')} />
            </div>
            <p>{p('device.guide.remote.p2')}</p>
            <div className={styles.shots}>
              <Figure name="offline" caption={t('device.guide.shots.offline')} />
            </div>
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
            <table className={styles.table}>
              <tbody>
                {COMMANDS.map(([cmd, key]) => (
                  <tr key={cmd}>
                    <td><code className={styles.inlineCode}>{cmd}</code></td>
                    <td>{t(`device.guide.commands.rows.${key}`)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className={styles.note}>{p('device.guide.commands.e2eeNote')}</p>
          </Section>
        </div>
      </div>
    </SurfaceInner>
  );
}
