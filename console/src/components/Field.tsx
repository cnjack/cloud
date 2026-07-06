import type {
  InputHTMLAttributes,
  ReactNode,
  TextareaHTMLAttributes,
} from 'react';
import { useId } from 'react';
import styles from './Field.module.css';

interface BaseProps {
  label: string;
  hint?: ReactNode;
  error?: string;
  required?: boolean;
}

export function TextField({
  label,
  hint,
  error,
  required,
  className,
  ...rest
}: BaseProps & InputHTMLAttributes<HTMLInputElement>) {
  const id = useId();
  return (
    <div className={styles.field}>
      <label htmlFor={id} className={styles.label}>
        {label}
        {required && <span className={styles.req} aria-hidden> *</span>}
      </label>
      <input
        id={id}
        className={[styles.input, error && styles.invalid, className]
          .filter(Boolean)
          .join(' ')}
        aria-invalid={!!error}
        aria-required={required}
        {...rest}
      />
      {error ? (
        <span className={styles.error}>{error}</span>
      ) : (
        hint && <span className={styles.hint}>{hint}</span>
      )}
    </div>
  );
}

export function TextAreaField({
  label,
  hint,
  error,
  required,
  className,
  ...rest
}: BaseProps & TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const id = useId();
  return (
    <div className={styles.field}>
      <label htmlFor={id} className={styles.label}>
        {label}
        {required && <span className={styles.req} aria-hidden> *</span>}
      </label>
      <textarea
        id={id}
        className={[styles.input, styles.textarea, error && styles.invalid, className]
          .filter(Boolean)
          .join(' ')}
        aria-invalid={!!error}
        aria-required={required}
        {...rest}
      />
      {error ? (
        <span className={styles.error}>{error}</span>
      ) : (
        hint && <span className={styles.hint}>{hint}</span>
      )}
    </div>
  );
}
