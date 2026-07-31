import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { UsageSummary as UsageSummaryValue } from '../api/types';
import { UsageSummary } from './UsageSummary';

const available: UsageSummaryValue = {
  availability: 'available',
  requests: 2,
  capture: { reported: 1, partial: 1, unavailable: 0, parse_error: 0 },
  tokens: { input: 1200, output: 80, cache_read: null, cache_write: 40 },
  costs: {
    reported: [{ currency: 'USD', micros: 3210 }],
    estimated: [{ currency: 'USD', micros: 1720, pricing_revision_ids: ['price-v3'] }],
    uncosted: [{ category: 'cache_write', tokens: 40 }],
  },
};

describe('UsageSummary', () => {
  it('keeps Reported, Estimated, and Uncosted distinct and discloses capture provenance', () => {
    render(<UsageSummary value={available} />);
    expect(screen.getByText('USD 0.003210')).toBeTruthy();
    expect(screen.getByText('USD 0.001720')).toBeTruthy();
    expect(screen.getByText('40 cache write')).toBeTruthy();
    expect(screen.getByText('2 / 2 captured')).toBeTruthy();
    expect(screen.getByText('price-v3')).toBeTruthy();
    expect(screen.getByText('Usage is operational telemetry, not a provider invoice.')).toBeTruthy();
  });

  it('shows unavailable as an explicit state without fabricating zero tokens', () => {
    render(<UsageSummary value={{
      availability: 'unavailable',
      reason: 'usage_not_reported',
      requests: 1,
      capture: { reported: 0, partial: 0, unavailable: 1, parse_error: 0 },
      tokens: { input: null, output: null, cache_read: null, cache_write: null },
      costs: { reported: [], estimated: [], uncosted: [] },
    }} />);
    expect(screen.getByText('Unavailable')).toBeTruthy();
    expect(screen.getByText('The provider did not report usage for this request.')).toBeTruthy();
    expect(screen.getByText('Usage is operational telemetry, not a provider invoice.')).toBeTruthy();
    expect(screen.queryByText(/0 tokens/i)).toBeNull();
  });

  it('does not disguise parser failures as provider omission', () => {
    render(<UsageSummary value={{
      availability: 'unavailable',
      reason: 'usage_not_reported',
      requests: 1,
      capture: { reported: 0, partial: 0, unavailable: 0, parse_error: 1 },
      tokens: { input: null, output: null, cache_read: null, cache_write: null },
      costs: { reported: [], estimated: [], uncosted: [] },
    }} />);
    expect(screen.getByText('Cloud couldn’t parse the provider usage payload.')).toBeTruthy();
    expect(screen.getByText('Parse errors').parentElement?.textContent).toBe('Parse errors1');
    expect(screen.queryByText('The provider did not report usage for this request.')).toBeNull();
  });
});
