import type { NavigateFunction } from 'react-router-dom';

interface RouterHistoryState {
  idx?: unknown;
}

function hasAppHistory(): boolean {
  const state = window.history.state as RouterHistoryState | null;
  return typeof state?.idx === 'number' && state.idx > 0;
}

/**
 * Return through the router stack when this screen was reached inside the app.
 * A direct deep link begins at index zero, so replacing it with the semantic
 * parent avoids navigating out of the app or to an unrelated browser page.
 */
export function navigateBack(navigate: NavigateFunction, fallback: string): void {
  if (hasAppHistory()) {
    navigate(-1);
    return;
  }
  navigate(fallback, { replace: true });
}
