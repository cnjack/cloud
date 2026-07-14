import { Link } from 'react-router-dom';
import appIcon from '../../../design/assets/app-icon.svg';
import styles from './Wordmark.module.css';

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
