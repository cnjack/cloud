import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { expect, it } from 'vitest';
import { LegacyRepositoryRedirect } from '../App';

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
}

it('keeps the selected section in older Repository links and clears an unrelated Remote context', async () => {
  render(<MemoryRouter initialEntries={['/repositories/repo-1?tab=board&remote=old-device']}>
    <Routes>
      <Route path="/repositories/:repositoryId" element={<LegacyRepositoryRedirect />} />
      <Route path="/repositories" element={<Location />} />
    </Routes>
  </MemoryRouter>);
  expect((await screen.findByTestId('location')).textContent).toBe('/repositories?tab=board&repository=repo-1');
});
