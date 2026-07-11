import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { Select } from './Select';
import { SelectField } from './Field';

const OPTIONS = [
  { value: '', label: 'Service default' },
  { value: 'm_gpt', label: 'GPT-4o' },
  { value: 'm_claude', label: 'Claude' },
];

describe('Select (Headless UI listbox)', () => {
  it('shows the selected option label on the trigger', () => {
    render(
      <Select value="m_gpt" onChange={() => {}} options={OPTIONS} data-testid="sel" />,
    );
    expect(screen.getByTestId('sel').textContent).toBe('GPT-4o');
    // Collapsed: no options rendered yet.
    expect(screen.queryByRole('option')).toBeNull();
  });

  it('opens on click and fires onChange with the picked value', async () => {
    const onChange = vi.fn();
    render(<Select value="" onChange={onChange} options={OPTIONS} data-testid="sel" />);

    fireEvent.click(screen.getByTestId('sel'));
    const options = await screen.findAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([
      'Service default',
      'GPT-4o',
      'Claude',
    ]);

    fireEvent.click(screen.getByRole('option', { name: 'Claude' }));
    expect(onChange).toHaveBeenCalledWith('m_claude');
    // The panel closes after picking.
    expect(screen.queryByRole('option')).toBeNull();
  });

  it('does NOT fire onChange when re-picking the already-selected option', async () => {
    const onChange = vi.fn();
    render(
      <Select value="m_gpt" onChange={onChange} options={OPTIONS} data-testid="sel" />,
    );
    fireEvent.click(screen.getByTestId('sel'));
    fireEvent.click(await screen.findByRole('option', { name: 'GPT-4o' }));
    // Native selects only fire change on an actual change; mutation-wired
    // callers (role change, default model) rely on the same contract.
    expect(onChange).not.toHaveBeenCalled();
  });

  it('exposes aria-haspopup="listbox" on the trigger (Modal autofocus relies on it)', () => {
    render(<Select value="" onChange={() => {}} options={OPTIONS} data-testid="sel" />);
    expect(screen.getByTestId('sel').getAttribute('aria-haspopup')).toBe('listbox');
  });

  it('marks the current value as selected in the open panel', async () => {
    render(<Select value="m_gpt" onChange={() => {}} options={OPTIONS} data-testid="sel" />);
    fireEvent.click(screen.getByTestId('sel'));
    const selected = await screen.findByRole('option', { name: 'GPT-4o' });
    expect(selected.getAttribute('aria-selected')).toBe('true');
  });

  it('is a real disabled button when disabled — clicking does not open it', () => {
    render(
      <Select value="" onChange={() => {}} options={OPTIONS} disabled data-testid="sel" />,
    );
    const trigger = screen.getByTestId('sel') as HTMLButtonElement;
    expect(trigger.disabled).toBe(true);
    fireEvent.click(trigger);
    expect(screen.queryByRole('option')).toBeNull();
  });

  it('falls back to the placeholder when the value matches no option', () => {
    render(
      <Select
        value="gone"
        onChange={() => {}}
        options={OPTIONS}
        placeholder="Pick a model…"
        data-testid="sel"
      />,
    );
    expect(screen.getByTestId('sel').textContent).toBe('Pick a model…');
  });
});

describe('SelectField', () => {
  it('wires the label to the trigger and renders hint / error', () => {
    const { rerender } = render(
      <SelectField
        label="Service"
        hint="Pick the repo"
        value=""
        onChange={() => {}}
        options={[{ value: '', label: 'Select service…' }]}
        data-testid="field-sel"
      />,
    );
    const trigger = screen.getByLabelText('Service');
    expect(trigger).toBe(screen.getByTestId('field-sel'));
    expect(screen.getByText('Pick the repo')).toBeTruthy();

    rerender(
      <SelectField
        label="Service"
        error="Required"
        value=""
        onChange={() => {}}
        options={[{ value: '', label: 'Select service…' }]}
        data-testid="field-sel"
      />,
    );
    expect(screen.getByText('Required')).toBeTruthy();
    expect(screen.getByTestId('field-sel').getAttribute('aria-invalid')).toBe('true');
  });
});
