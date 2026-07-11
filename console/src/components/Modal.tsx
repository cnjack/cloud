import { useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';
import styles from './Modal.module.css';

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  'data-testid'?: string;
}

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  'data-testid': testId,
}: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  // Keep the latest onClose without re-running the open effect: callers often
  // pass an inline handler whose identity changes every render (e.g. on each
  // keystroke), and we must not re-focus/re-bind on those re-renders.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCloseRef.current();
    };
    document.addEventListener('keydown', onKey);
    // Focus a form field first so keyboard users land in the body; fall back to
    // any focusable control. Querying plain document order would grab the
    // header's close button, which precedes the body in the DOM. Selects render
    // as Headless UI listbox trigger buttons, so match those alongside inputs.
    const panel = panelRef.current;
    const target =
      panel?.querySelector<HTMLElement>(
        'input, textarea, select, button[aria-haspopup="listbox"]',
      ) ?? panel?.querySelector<HTMLElement>('button, [tabindex]');
    target?.focus();
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div
      className={styles.overlay}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={styles.panel}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        ref={panelRef}
        data-testid={testId}
      >
        <header className={styles.header}>
          <h2 className={styles.title}>{title}</h2>
          <button
            className={styles.close}
            onClick={onClose}
            aria-label="Close dialog"
            type="button"
          >
            ✕
          </button>
        </header>
        <div className={styles.body}>{children}</div>
        {footer && <footer className={styles.footer}>{footer}</footer>}
      </div>
    </div>,
    document.body,
  );
}
