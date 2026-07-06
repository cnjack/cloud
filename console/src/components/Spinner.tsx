import styles from './Spinner.module.css';

export function Spinner({ label }: { label?: string }) {
  return (
    <span className={styles.wrap} role="status">
      <span className={styles.spinner} aria-hidden />
      {label && <span className={styles.label}>{label}</span>}
    </span>
  );
}
