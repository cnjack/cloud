import { ArrowLeft, ArrowRight, Check, Copy, LockKey, TerminalWindow } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { ErrorBlock } from '../components/States';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useDevices } from '@jcloud/device-ui';
import { sanitizeCode } from '../components/CodeInput';
import { AccountHeader } from '../components/AccountHeader';
import styles from './DeviceGuidePage.module.css';

const COMMAND = 'jcode login --cloud https://cloud.j-code.net';

/** Setup-only Remote onboarding. Later selection and conversations live in the device workspace. */
export function DeviceGuidePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const devices = useDevices();
  const [code, setCode] = useState('');
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const normalized = sanitizeCode(code).slice(0, 8);

  const authorize = () => {
    if (normalized.length === 8) navigate(`/device?user_code=${encodeURIComponent(normalized)}`);
  };

  return (
    <div className={styles.page} data-testid="device-guide">
      <AccountHeader sectionTitle={t('cloudRefresh.remoteDevices')} />
      <main className={styles.main}>
        <nav className={styles.secure}><Link to="/devices" className={styles.back}><ArrowLeft size={15} />{t('cloudRefresh.backDevices')}</Link><span><LockKey size={15} />{t('cloudDeviceGuide.secure')}</span></nav>
        <header className={styles.hero}>
          <span>{t('cloudDeviceGuide.section')}</span>
          <h1>{t('cloudDeviceGuide.title')}</h1>
          <p>{t('cloudDeviceGuide.description')}</p>
        </header>

        <ol className={styles.progress} aria-label={t('cloudDeviceGuide.steps')}>
          <li data-state="active"><span>1</span><strong>{t('cloudDeviceGuide.login')}</strong></li>
          <li><span>2</span><strong>{t('cloudDeviceGuide.approve')}</strong></li>
          <li><span>3</span><strong>{t('cloudDeviceGuide.pair')}</strong></li>
        </ol>

        <section className={styles.layout}>
          <aside className={styles.rail}>
            <strong>{t('cloudDeviceGuide.newDevice')}</strong><small>{t('cloudDeviceGuide.minute')}</small>
            <ol>
              <li data-state="active"><span>1</span><p><strong>{t('cloudDeviceGuide.runCommand')}</strong><small>{t('cloudDeviceGuide.getCode')}</small></p></li>
              <li><span>2</span><p><strong>{t('cloudDeviceGuide.confirmIdentity')}</strong><small>{t('cloudDeviceGuide.approveAccount')}</small></p></li>
              <li><span>3</span><p><strong>{t('cloudDeviceGuide.completePair')}</strong><small>{t('cloudDeviceGuide.approveLocal')}</small></p></li>
            </ol>
          </aside>

          <section className={styles.panel}>
            <div className={styles.panelBody}>
              <span className={styles.kicker}><TerminalWindow size={16} />{t('cloudDeviceGuide.onDevice')}</span>
              <h2>{t('cloudDeviceGuide.runLogin')}</h2>
              <p>{t('cloudDeviceGuide.commandDescription')}</p>
              <label>{t('cloudDeviceGuide.terminal')}</label>
              <div className={styles.command}><code>{COMMAND}</code><button type="button" aria-label={t(copied ? 'common.copied' : 'cloudDeviceGuide.copyCommand')} onClick={async () => { try { await navigator.clipboard.writeText(COMMAND); setCopied(true); setCopyError(false); } catch { setCopyError(true); } }}>{copied ? <Check size={17} /> : <Copy size={17} />}</button></div>
              {copyError && <p role="alert">{t('cloudDeviceGuide.copyError')}</p>}
              <label htmlFor="remote-device-code">{t('cloudDeviceGuide.enterCode')}</label>
              <div className={styles.codeRow}>
                <input id="remote-device-code" value={code} onChange={(event) => setCode(event.target.value)} placeholder="JCDX-4H7Q" autoComplete="one-time-code" />
                <button type="button" className={styles.primary} disabled={normalized.length !== 8} onClick={authorize}>{t('cloudDeviceGuide.continue')}<ArrowRight size={15} /></button>
              </div>
              <div className={styles.note}><LockKey size={18} /><span><strong>{t('cloudDeviceGuide.ciphertext')}</strong>{t('cloudDeviceGuide.ciphertextDescription')}</span></div>

              {devices.isError && <ErrorBlock error={devices.error} onRetry={() => void devices.refetch()} />}
              {(devices.data ?? []).length > 0 && (
                <div className={styles.existing}>
                  <header><strong>{t('cloudDeviceGuide.connected')}</strong><small>{t('cloudDeviceGuide.connectedDescription')}</small></header>
                  {(devices.data ?? []).map((device) => <Link key={device.id} to={`/devices/${encodeURIComponent(device.id)}`}><TerminalWindow size={17} /><span><strong>{device.name}</strong><small>{device.platform || t('repositories.jcodeDevice')} · {t(device.online ? 'repositories.online' : 'device.list.offline')}</small></span><ArrowRight size={15} /></Link>)}
                </div>
              )}
            </div>
            <footer><span>{t('cloudDeviceGuide.expires')}</span><Link to="/devices">{t('common.cancel')}</Link></footer>
          </section>
        </section>
      </main>
    </div>
  );
}
