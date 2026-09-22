import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { Run } from '../api/types';
import { RunActivityList } from './RunActivityList';

const runs = [
  { id: 'a', prompt: 'Fix login', status: 'running', created_at: '2026-09-20T00:00:00Z' },
  { id: 'b', prompt: 'Fix deploy', status: 'failed', created_at: '2026-09-21T00:00:00Z' },
  { id: 'c', prompt: 'Write docs', status: 'succeeded', created_at: '2026-09-22T00:00:00Z' },
  { id: 'd', prompt: 'Review login plan', status: 'awaiting_input', created_at: '2026-09-22T01:00:00Z' },
] as Run[];
function show(records = runs) {
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><RunActivityList runs={records} isLoading={false} error={null} onRetry={() => {}} filter="all" onFilterChange={() => {}} canRun showFilters={false} /></MemoryRouter></QueryClientProvider>);
}

describe('Task search and status filters', () => {
  it('combines search and status, treats awaiting input as attention, and clears filters', () => {
    show();
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search tasks' }), { target: { value: 'login' } });
    fireEvent.click(screen.getByRole('button', { name: /Needs attention/ }));
    expect(screen.getAllByTestId('run-row').map((row) => row.getAttribute('data-run-id'))).toEqual(['d']);
    fireEvent.click(screen.getByRole('button', { name: /Completed/ }));
    expect(screen.getByText('No matching tasks')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(screen.getAllByTestId('run-row').map((row) => row.getAttribute('data-run-id'))).toEqual(['d', 'c', 'b', 'a']);
    expect(runs.map((run) => run.id)).toEqual(['a', 'b', 'c', 'd']);
  });

  it('distinguishes a repository without tasks from an unmatched search', () => {
    show([]);
    expect(screen.getByTestId('runs-empty')).toBeTruthy();
    expect(screen.queryByText('No matching tasks')).toBeNull();
  });
});
