import { fireEvent, screen } from '@testing-library/react';

/** Open a Headless UI Select by its trigger testid and click the named option. */
export async function pickOption(testId: string, name: string | RegExp) {
  fireEvent.click(screen.getByTestId(testId));
  fireEvent.click(await screen.findByRole('option', { name }));
}
