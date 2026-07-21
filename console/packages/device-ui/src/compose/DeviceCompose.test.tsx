/*
 * DeviceCompose.test.tsx — the M12 shared compose panel: section rendering,
 * old-device degradation (no capabilities → whole panel hidden), client-side
 * attachment limits (non-image, ≤2 MB each, ≤5 total) and the chat.send
 * payload assembly (composeExtras).
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import type { DeviceCapabilities } from '../api/devices';
import {
  DeviceCompose,
  composeExtras,
  initialComposeValue,
  COMPOSE_MAX_ATTACHMENT_BYTES,
  type ComposeValue,
} from './DeviceCompose';

const CAPS: DeviceCapabilities = {
  projects: [
    { path: '/repo/a', name: 'alpha' },
    { path: '/repo/b', name: 'beta' },
  ],
  models: [
    { provider: 'anthropic', id: 'claude-opus-4-1', label: 'Opus 4.1' },
    { provider: 'anthropic', id: 'claude-sonnet-4-5', label: 'Sonnet 4.5' },
    { provider: 'openai', id: 'gpt-5.2', label: 'GPT-5.2' },
  ],
  efforts: ['low', 'medium', 'high'],
};

function Rig({ capabilities = CAPS }: { capabilities?: DeviceCapabilities | null }) {
  const [value, setValue] = useState<ComposeValue>(initialComposeValue());
  return (
    <>
      <DeviceCompose capabilities={capabilities} value={value} onChange={setValue} />
      <output data-testid="extras">{JSON.stringify(composeExtras(value, capabilities) ?? null)}</output>
    </>
  );
}

function pickFiles(files: File[]) {
  const input = screen.getByTestId('device-compose-files') as HTMLInputElement;
  fireEvent.change(input, { target: { files } });
}

describe('DeviceCompose rendering', () => {
  it('renders all five elements from capabilities', () => {
    render(<Rig />);
    expect(screen.getByLabelText('Project directory')).toBeTruthy();
    expect(screen.getByLabelText('Model')).toBeTruthy();
    expect(screen.getByText('Effort')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Goal' })).toBeTruthy();
    expect(screen.getByText('Attachments')).toBeTruthy();
    // Models are grouped by provider.
    expect(document.querySelectorAll('optgroup')).toHaveLength(2);
    expect(document.querySelector('optgroup[label="anthropic"]')).toBeTruthy();
  });

  it('hides the whole panel for a device without capabilities (old connector)', () => {
    const { container } = render(<Rig capabilities={null} />);
    expect(screen.queryByTestId('device-compose')).toBeNull();
    expect(container.querySelector('[data-testid="extras"]')?.textContent).toBe('null');
  });

  it('hides only the empty sections for partial capabilities', () => {
    render(<Rig capabilities={{ projects: [{ path: '/repo/a', name: 'alpha' }] }} />);
    expect(screen.getByLabelText('Project directory')).toBeTruthy();
    expect(screen.queryByLabelText('Model')).toBeNull();
    expect(screen.queryByText('Effort')).toBeNull();
    // Goal + attachments are client-side concepts — always present.
    expect(screen.getByRole('button', { name: 'Goal' })).toBeTruthy();
    expect(screen.getByText('Attachments')).toBeTruthy();
  });
});

describe('composeExtras payload assembly', () => {
  it('returns undefined for the untouched default state', () => {
    expect(composeExtras(initialComposeValue(), CAPS)).toBeUndefined();
  });

  it('assembles every selected element into the chat.send extras', () => {
    const extras = composeExtras(
      {
        projectPath: '/repo/b',
        model: { provider: 'openai', id: 'gpt-5.2' },
        effort: 'high',
        goal: '  ship M12  ',
        attachments: [{ name: 'spec.txt', mime: 'text/plain', data_b64: 'aGk=' }],
      },
      CAPS,
    );
    expect(extras).toEqual({
      project_path: '/repo/b',
      model: { provider: 'openai', id: 'gpt-5.2' },
      effort: 'high',
      goal: 'ship M12',
      attachments: [{ name: 'spec.txt', mime: 'text/plain', data_b64: 'aGk=' }],
    });
  });

  it('drops selections the device no longer advertises (stale capabilities)', () => {
    const extras = composeExtras(
      {
        projectPath: '/repo/gone',
        model: { provider: 'openai', id: 'gpt-4' },
        effort: 'ultra',
        goal: '',
        attachments: [],
      },
      CAPS,
    );
    expect(extras).toBeUndefined();
  });
});

describe('DeviceCompose interaction', () => {
  it('selecting project/model/effort and a goal flows into the payload', () => {
    render(<Rig />);
    fireEvent.change(screen.getByLabelText('Project directory'), { target: { value: '/repo/a' } });
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'anthropic::claude-sonnet-4-5' } });
    fireEvent.click(screen.getByRole('button', { name: 'high' }));
    fireEvent.click(screen.getByRole('button', { name: 'Goal' }));
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'fix the flake' } });
    const extras = JSON.parse(screen.getByTestId('extras').textContent ?? 'null');
    expect(extras).toEqual({
      project_path: '/repo/a',
      model: { provider: 'anthropic', id: 'claude-sonnet-4-5' },
      effort: 'high',
      goal: 'fix the flake',
    });
  });

  it('attaches a non-image file as base64', async () => {
    render(<Rig />);
    pickFiles([new File(['hello'], 'notes.txt', { type: 'text/plain' })]);
    await waitFor(() => {
      const extras = JSON.parse(screen.getByTestId('extras').textContent ?? 'null');
      expect(extras?.attachments).toEqual([{ name: 'notes.txt', mime: 'text/plain', data_b64: 'aGVsbG8=' }]);
    });
    expect(screen.getByText('notes.txt')).toBeTruthy();
  });

  it('blocks an image file with a visible hint', async () => {
    render(<Rig />);
    pickFiles([new File(['x'], 'pic.png', { type: 'image/png' })]);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('pic.png'));
    expect(screen.getByTestId('extras').textContent).toBe('null');
  });

  it('blocks a file over 2 MB with a visible hint', async () => {
    render(<Rig />);
    const big = new File([new Uint8Array(COMPOSE_MAX_ATTACHMENT_BYTES + 1)], 'big.bin', {
      type: 'application/octet-stream',
    });
    pickFiles([big]);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('big.bin'));
    expect(screen.getByTestId('extras').textContent).toBe('null');
  });

  it('blocks picking beyond 5 attachments', async () => {
    render(<Rig />);
    const six = Array.from({ length: 6 }, (_, i) => new File(['x'], `f${i}.txt`, { type: 'text/plain' }));
    pickFiles(six);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('5'));
    expect(screen.getByTestId('extras').textContent).toBe('null');
  });
});
