import { ArrowLeft, ArrowRight, Check, Copy, LockKey, TerminalWindow } from '@phosphor-icons/react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useDevices } from '@jcloud/device-ui';
import { sanitizeCode } from '../components/CodeInput';
import { AccountHeader } from '../components/AccountHeader';
import styles from './DeviceGuidePage.module.css';

const COMMAND = 'jcode login --cloud https://cloud.j-code.net';

/** Setup-only Remote onboarding. Later selection and conversations live in Work Home. */
export function DeviceGuidePage() {
  const navigate = useNavigate();
  const devices = useDevices();
  const [code, setCode] = useState('');
  const [copied, setCopied] = useState(false);
  const normalized = sanitizeCode(code).slice(0, 8);

  const authorize = () => {
    if (normalized.length === 8) navigate(`/device?user_code=${encodeURIComponent(normalized)}`);
  };

  return (
    <div className={styles.page} data-testid="device-guide">
      <AccountHeader />
      <main className={styles.main}>
        <nav className={styles.secure}><Link to="/" className={styles.back}><ArrowLeft size={15} />Back to Work Home</Link><span><LockKey size={15} />End-to-end encrypted connection</span></nav>
        <header className={styles.hero}>
          <span>Remote connection</span>
          <h1>Connect a jcode device</h1>
          <p>Complete login once. The device then appears in the upper-left context picker in Work Home—there is no separate Remote workspace or device management page.</p>
        </header>

        <ol className={styles.progress} aria-label="Remote onboarding steps">
          <li data-state="active"><span>1</span><strong>Log in to jcode</strong></li>
          <li><span>2</span><strong>Approve device</strong></li>
          <li><span>3</span><strong>Encrypted pairing</strong></li>
        </ol>

        <section className={styles.layout}>
          <aside className={styles.rail}>
            <strong>New Remote device</strong><small>About one minute</small>
            <ol>
              <li data-state="active"><span>1</span><p><strong>Run the login command</strong><small>Get a one-time device code</small></p></li>
              <li><span>2</span><p><strong>Confirm device identity</strong><small>Approve account access</small></p></li>
              <li><span>3</span><p><strong>Complete pairing</strong><small>Approve in local jcode</small></p></li>
            </ol>
          </aside>

          <section className={styles.panel}>
            <div className={styles.panelBody}>
              <span className={styles.kicker}><TerminalWindow size={16} />On the device you want to connect</span>
              <h2>Run jcode login</h2>
              <p>The command creates a one-time authorization code and normally opens this browser automatically. If it does not, enter the code below.</p>
              <label>Terminal</label>
              <div className={styles.command}><code>{COMMAND}</code><button type="button" aria-label={copied ? 'Copied' : 'Copy login command'} onClick={() => { void navigator.clipboard?.writeText(COMMAND); setCopied(true); }}>{copied ? <Check size={17} /> : <Copy size={17} />}</button></div>
              <label htmlFor="remote-device-code">Enter the code shown by the CLI</label>
              <div className={styles.codeRow}>
                <input id="remote-device-code" value={code} onChange={(event) => setCode(event.target.value)} placeholder="JCDX-4H7Q" autoComplete="one-time-code" />
                <button type="button" className={styles.primary} disabled={normalized.length !== 8} onClick={authorize}>Continue authorization<ArrowRight size={15} /></button>
              </div>
              <div className={styles.note}><LockKey size={18} /><span><strong>Cloud only routes ciphertext</strong>The device code links this login to your account. Pairing keys stay in jcode and this browser; the server never parses device command payloads.</span></div>

              {(devices.data ?? []).length > 0 && (
                <div className={styles.existing}>
                  <header><strong>Already connected</strong><small>Select one in Work Home, or continue above to add another device.</small></header>
                  {(devices.data ?? []).map((device) => <Link key={device.id} to={`/?remote=${encodeURIComponent(device.id)}`}><TerminalWindow size={17} /><span><strong>{device.name}</strong><small>{device.platform || 'jcode device'} · {device.online ? 'online' : 'offline'}</small></span><ArrowRight size={15} /></Link>)}
                </div>
              )}
            </div>
            <footer><span>The authorization code expires visibly in jcode.</span><Link to="/">Cancel</Link></footer>
          </section>
        </section>
      </main>
    </div>
  );
}
