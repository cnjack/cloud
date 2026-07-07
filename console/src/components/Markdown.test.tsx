/*
 * Markdown.test.tsx — the tiny review-output renderer:
 *   - headings, bold, inline code, unordered lists and fenced code blocks
 *   - XSS: raw HTML in the source is rendered as literal text, never markup
 */
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Markdown } from './Markdown';

describe('Markdown', () => {
  it('renders headings, bold, inline code, lists and code blocks', () => {
    const src = [
      '## Summary',
      '',
      'This is **bold** and `inline` code.',
      '',
      '- first',
      '- second',
      '',
      '```',
      'const x = 1;',
      '```',
    ].join('\n');
    const { container } = render(<Markdown source={src} />);

    expect(container.querySelector('h4')?.textContent).toBe('Summary');
    expect(container.querySelector('strong')?.textContent).toBe('bold');
    // inline code + code block both produce <code>.
    const codes = [...container.querySelectorAll('code')].map((c) => c.textContent);
    expect(codes).toContain('inline');
    expect(codes.some((t) => t?.includes('const x = 1;'))).toBe(true);
    expect(container.querySelector('pre')).toBeTruthy();
    const items = [...container.querySelectorAll('li')].map((li) => li.textContent);
    expect(items).toEqual(['first', 'second']);
  });

  it('does not interpret raw HTML — it renders as literal text (XSS-safe)', () => {
    const { container } = render(
      <Markdown source={'<img src=x onerror=alert(1)> <b>hi</b>'} />,
    );
    // No injected <img> or <b> element — the markup is literal text.
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('b')).toBeNull();
    expect(screen.getByText(/<img src=x onerror=alert\(1\)>/)).toBeTruthy();
  });

  it('renders an empty string without crashing', () => {
    const { container } = render(<Markdown source={''} />);
    expect(container.firstChild).toBeTruthy();
  });
});
