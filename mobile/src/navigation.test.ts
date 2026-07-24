import { afterEach, describe, expect, it, vi } from 'vitest';
import { navigateBack } from './navigation';

afterEach(() => {
  window.history.replaceState({ idx: 0 }, '');
});

describe('navigateBack', () => {
  it('pops list -> welcome -> detail instead of pushing another welcome entry', () => {
    window.history.replaceState({ idx: 2 }, '');
    const navigate = vi.fn();

    navigateBack(navigate, '/devices/d1');

    expect(navigate).toHaveBeenCalledWith(-1);
  });

  it('replaces a cold-start/deep-link detail with its semantic parent', () => {
    window.history.replaceState({ idx: 0 }, '');
    const navigate = vi.fn();

    navigateBack(navigate, '/devices/d1');

    expect(navigate).toHaveBeenCalledWith('/devices/d1', { replace: true });
  });
});
