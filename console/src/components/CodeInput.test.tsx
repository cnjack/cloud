import { useState } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { CodeInput, sanitizeCode } from './CodeInput';

/** Controlled harness mirroring how DeviceAuthorizePage drives the input. */
function Harness({
  onComplete,
  error = false,
  initial = '',
}: {
  onComplete?: (value: string) => void;
  error?: boolean;
  initial?: string;
}) {
  const [value, setValue] = useState(initial);
  return <CodeInput value={value} onChange={setValue} onComplete={onComplete} error={error} />;
}

function cells(): HTMLInputElement[] {
  return screen.getAllByRole('textbox') as HTMLInputElement[];
}

function values(): string {
  return cells()
    .map((c) => c.value || '·')
    .join('');
}

describe('CodeInput (M17 segmented device code)', () => {
  it('renders 8 cells with the 4-4 separator', () => {
    const { container } = render(<Harness />);
    expect(cells()).toHaveLength(8);
    expect(container.querySelector('[role="group"]')?.textContent).toContain('–');
  });

  it('fills a cell per character, uppercases and auto-advances focus', () => {
    render(<Harness />);
    const [c0, c1] = cells() as [HTMLInputElement, HTMLInputElement];
    fireEvent.change(c0, { target: { value: 'a' } });
    expect(cells()[0]!.value).toBe('A');
    expect(document.activeElement).toBe(cells()[1]);
    fireEvent.change(c1, { target: { value: '2' } });
    expect(values()).toBe('A2······');
    expect(document.activeElement).toBe(cells()[2]);
  });

  it('ignores characters outside the code alphabet', () => {
    render(<Harness />);
    fireEvent.change(cells()[0]!, { target: { value: '-' } });
    expect(cells()[0]!.value).toBe('');
    expect(document.activeElement).not.toBe(cells()[1]);
  });

  it('splits a whole-code paste across the cells and completes', () => {
    const onComplete = vi.fn();
    render(<Harness onComplete={onComplete} />);
    fireEvent.paste(cells()[0]!, { clipboardData: { getData: () => 'abcd-2345' } });
    expect(values()).toBe('ABCD2345');
    expect(onComplete).toHaveBeenCalledWith('ABCD2345');
  });

  it('handles messy paste (spaces, lowercase, overflow) without completing', () => {
    const onComplete = vi.fn();
    render(<Harness onComplete={onComplete} />);
    fireEvent.paste(cells()[2]!, { clipboardData: { getData: () => ' ab cd2 3456789' } });
    expect(values()).toBe('ABCD2345');
    // 8 chars fit exactly — this one completes too.
    expect(onComplete).toHaveBeenCalledWith('ABCD2345');
  });

  it('partial paste fills the leading cells and focuses the next empty one', () => {
    const onComplete = vi.fn();
    render(<Harness onComplete={onComplete} />);
    fireEvent.paste(cells()[0]!, { clipboardData: { getData: () => 'abc' } });
    expect(values()).toBe('ABC·····');
    expect(onComplete).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(cells()[3]);
  });

  it('completes when the last cell is typed', () => {
    const onComplete = vi.fn();
    render(<Harness onComplete={onComplete} initial="ABCD234" />);
    fireEvent.change(cells()[7]!, { target: { value: '5' } });
    expect(onComplete).toHaveBeenCalledWith('ABCD2345');
  });

  it('Backspace clears the current cell, then retreats and clears the previous', () => {
    render(<Harness initial="AB" />);
    const c2 = cells()[2]!;
    c2.focus();
    // Empty current cell: retreat to the previous one and clear it.
    fireEvent.keyDown(c2, { key: 'Backspace' });
    expect(values()).toBe('A·······');
    expect(document.activeElement).toBe(cells()[1]);
    // Filled current cell: clear in place.
    fireEvent.keyDown(cells()[1]!, { key: 'Backspace' });
    expect(values()).toBe('········');
    expect(document.activeElement).toBe(cells()[0]);
  });

  it('Arrow keys move between cells', () => {
    render(<Harness initial="ABCD" />);
    const c1 = cells()[1]!;
    c1.focus();
    fireEvent.keyDown(c1, { key: 'ArrowRight' });
    expect(document.activeElement).toBe(cells()[2]);
    fireEvent.keyDown(cells()[2]!, { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(cells()[1]!);
  });

  it('applies error styling to every cell', () => {
    render(<Harness error initial="ABCD" />);
    for (const cell of cells()) {
      expect(cell.className).toContain('invalid');
      expect(cell.getAttribute('aria-invalid')).toBe('true');
    }
  });

  it('sanitizeCode strips separators and uppercases', () => {
    expect(sanitizeCode('abcd-efgh')).toBe('ABCDEFGH');
    expect(sanitizeCode('abcd efgh')).toBe('ABCDEFGH');
    expect(sanitizeCode('a-b_c*d')).toBe('ABCD');
  });
});
