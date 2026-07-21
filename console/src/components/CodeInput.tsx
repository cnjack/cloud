/*
 * CodeInput — segmented device-code input (M17, modelled on jtype's
 * OTPInput): one character per cell with auto-advance, Backspace retreat,
 * ←/→ navigation and whole-code paste split across the cells. `onComplete`
 * fires when every cell is filled. The orchestrator mints user codes from a
 * look-alike-free alphabet (orchestrator device_auth.go userCodeAlphabet), so
 * input is uppercased and stripped to A-Z0-9.
 */
import { useEffect, useRef } from 'react';
import styles from './CodeInput.module.css';

export interface CodeInputProps {
  /** Number of cells. Default 8 (the XXXX-XXXX user code). */
  length?: number;
  /** Controlled value (A-Z0-9 string, at most `length` chars). */
  value: string;
  onChange: (value: string) => void;
  /** Fired once per completion when every cell is filled. */
  onComplete?: (value: string) => void;
  /** Show error styling on all cells. */
  error?: boolean;
  /** Focus the first cell on mount. */
  autoFocus?: boolean;
  /** Render a separator after this many cells. Default 4 (4-4 split). */
  groupSize?: number;
  /** Accessible label for the cell group. */
  ariaLabel?: string;
  /** Per-cell accessible label; defaults to "Character N of M". */
  cellAriaLabel?: (index: number, total: number) => string;
}

/** Canonical character set: uppercase A-Z0-9, everything else dropped. */
export const sanitizeCode = (raw: string) => raw.replace(/[^a-zA-Z0-9]/g, '').toUpperCase();

export function CodeInput({
  length = 8,
  value,
  onChange,
  onComplete,
  error = false,
  autoFocus = false,
  groupSize = 4,
  ariaLabel,
  cellAriaLabel,
}: CodeInputProps) {
  const cellRefs = useRef<Array<HTMLInputElement | null>>([]);

  const chars = Array.from({ length }, (_, i) => value[i] ?? '');

  const completeRef = useRef(onComplete);
  completeRef.current = onComplete;
  useEffect(() => {
    if (chars.every((c) => c !== '')) {
      completeRef.current?.(chars.join(''));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, length]);

  useEffect(() => {
    if (autoFocus) cellRefs.current[0]?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const focusCell = (idx: number) => {
    const clamped = Math.max(0, Math.min(idx, length - 1));
    cellRefs.current[clamped]?.focus();
    cellRefs.current[clamped]?.select();
  };

  const setValueAt = (idx: number, char: string) => {
    const next = chars.slice();
    next[idx] = char;
    onChange(next.join(''));
  };

  const handleInput = (idx: number, raw: string) => {
    // Take only the last valid character; ignore anything else.
    const char = sanitizeCode(raw).slice(-1);
    setValueAt(idx, char);
    if (char && idx < length - 1) focusCell(idx + 1);
  };

  const handleKeyDown = (idx: number, e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Backspace') {
      e.preventDefault();
      if (chars[idx]) {
        setValueAt(idx, '');
      } else if (idx > 0) {
        focusCell(idx - 1);
        setValueAt(idx - 1, '');
      }
      return;
    }
    if (e.key === 'ArrowLeft' && idx > 0) {
      e.preventDefault();
      focusCell(idx - 1);
      return;
    }
    if (e.key === 'ArrowRight' && idx < length - 1) {
      e.preventDefault();
      focusCell(idx + 1);
    }
  };

  const handlePaste = (e: React.ClipboardEvent<HTMLInputElement>) => {
    e.preventDefault();
    const text = e.clipboardData?.getData('text') ?? '';
    // Whole-code paste: "abcd-efgh", "abcd efgh" and "ABCDEFGH" all split
    // across the cells.
    const pasted = sanitizeCode(text).slice(0, length);
    if (!pasted) return;
    onChange(pasted);
    focusCell(Math.min(pasted.length, length - 1));
  };

  const allFilled = chars.every((c) => c !== '');

  return (
    <div className={styles.group} role="group" aria-label={ariaLabel}>
      {chars.map((char, idx) => (
        <span key={idx} className={styles.slot}>
          <input
            ref={(el) => {
              cellRefs.current[idx] = el;
            }}
            className={[
              styles.cell,
              char ? styles.filled : '',
              allFilled ? styles.complete : '',
              error ? styles.invalid : '',
            ]
              .filter(Boolean)
              .join(' ')}
            type="text"
            inputMode="text"
            autoComplete="one-time-code"
            maxLength={1}
            value={char}
            aria-label={cellAriaLabel ? cellAriaLabel(idx, length) : `Character ${idx + 1} of ${length}`}
            aria-invalid={error || undefined}
            onChange={(e) => handleInput(idx, e.target.value)}
            onKeyDown={(e) => handleKeyDown(idx, e)}
            onPaste={handlePaste}
            onFocus={(e) => e.target.select()}
          />
          {idx === groupSize - 1 && idx < length - 1 && (
            <span className={styles.sep} aria-hidden="true">
              –
            </span>
          )}
        </span>
      ))}
    </div>
  );
}
