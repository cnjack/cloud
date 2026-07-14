import { Link } from 'react-router-dom';
import styles from './Wordmark.module.css';

const appIcon = `${import.meta.env.BASE_URL}app-icon.svg`;

/*
 * Wordmark — the canonical jcode application icon and Cloud product lockup.
 */
export function Wordmark() {
  return (
    <Link to="/" className={styles.wordmark} aria-label="jcode Cloud home">
      <img className={styles.icon} src={appIcon} alt="" />
      <span className={styles.name}>JCODE</span>
      <span className={styles.cloud}>Cloud</span>
    </Link>
  );
}
