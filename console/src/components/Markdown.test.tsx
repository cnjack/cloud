/*
 * Markdown.test.tsx — the jcode-ui review-output renderer:
 *   - GFM headings, bold, inline code, lists, links and fenced code blocks
 *   - XSS: dangerous elements and event handlers are removed by DOMPurify
 */
import { describe, expect, it } from 'vitest';
import { render } from '@testing-library/react';
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
      '[review](https://example.com/review)',
      '',
      '```',
      'const x = 1;',
      '```',
    ].join('\n');
    const { container } = render(<Markdown source={src} />);

    expect(container.querySelector('h2')?.textContent).toBe('Summary');
    expect(container.querySelector('strong')?.textContent).toBe('bold');
    // inline code + code block both produce <code>.
    const codes = [...container.querySelectorAll('code')].map((c) => c.textContent);
    expect(codes).toContain('inline');
    expect(codes.some((t) => t?.includes('const x = 1;'))).toBe(true);
    expect(container.querySelector('pre')).toBeTruthy();
    const items = [...container.querySelectorAll('li')].map((li) => li.textContent);
    expect(items).toEqual(['first', 'second']);
    expect(container.querySelector('a')?.getAttribute('href')).toBe('https://example.com/review');
    expect(container.querySelector('[data-jcode-ui]')).toBeTruthy();
  });

  it('sanitizes scripts and event handlers from model-provided HTML', () => {
    const { container } = render(
      <Markdown source={'<script>alert(1)</script><img src=x onerror=alert(2)><b>hi</b>'} />,
    );
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('img')?.hasAttribute('onerror')).toBe(false);
    expect(container.querySelector('b')?.textContent).toBe('hi');
  });

  it('renders an empty string without crashing', () => {
    const { container } = render(<Markdown source={''} />);
    expect(container.firstChild).toBeTruthy();
  });
});
