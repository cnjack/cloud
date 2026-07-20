import type { ButtonHTMLAttributes, ReactNode } from 'react';
import styles from './Button.module.css';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
type Size = 'sm' | 'md';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  children: ReactNode;
}

export function Button({
  variant = 'secondary',
  size = 'md',
  loading = false,
  disabled,
  children,
  className,
  ...rest
}: ButtonProps) {
  return (
    <button
      className={[styles.btn, styles[variant], styles[size], className]
        .filter(Boolean)
        .join(' ')}
      disabled={disabled || loading}
      data-loading={loading || undefined}
      {...rest}
    >
      {loading && <span className={styles.spinner} aria-hidden />}
      <span className={styles.label}>{children}</span>
    </button>
  );
}
