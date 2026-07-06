import { Link } from 'react-router-dom';
import styles from './Wordmark.module.css';

/*
 * Wordmark — the [J]CODE CLOUD brand lockup. JetBrains Mono, orange bracketed J,
 * per jcode-design. Links home.
 */
export function Wordmark() {
  return (
    <Link to="/" className={styles.wordmark} aria-label="jcode Cloud home">
      <span className={styles.bracket}>[</span>
      <span className={styles.j}>J</span>
      <span className={styles.bracket}>]</span>
      <span className={styles.rest}>CODE</span>
      <span className={styles.cloud}>CLOUD</span>
    </Link>
  );
}
