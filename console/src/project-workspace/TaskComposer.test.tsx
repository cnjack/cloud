import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ComponentProps, FormEvent } from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { Service } from '../api/types';
import { TaskComposer } from './TaskComposer';

const service: Service = {
  id: 'svc-1',
  project_id: 'project-1',
  name: 'api',
  repo_kind: 'provider',
  provider: 'github',
  repo_owner_name: 'acme/api',
  default_branch: 'main',
  git_mode: 'readonly',
  created_at: '',
};

function composerProps(
  overrides: Partial<ComponentProps<typeof TaskComposer>> = {},
): ComponentProps<typeof TaskComposer> {
  return {
    service,
    notice: null,
    configured: true,
    prompt: '',
    onPromptChange: () => {},
    models: [],
    selectedModel: '',
    onSelectedModelChange: () => {},
    effortEnabled: false,
    modelEffort: 'auto',
    onModelEffortChange: () => {},
    goalMode: false,
    onGoalModeChange: () => {},
    attachments: [],
    onAttachmentsAdd: () => {},
    onAttachmentRemove: () => {},
    branches: [{ name: 'main', default: true }],
    branchesLoading: false,
    branchesError: false,
    selectedBranch: 'main',
    onSelectedBranchChange: () => {},
    askApproval: false,
    onAskApprovalChange: () => {},
    onSubmit: (event) => event.preventDefault(),
    busy: false,
    ...overrides,
  };
}

describe('TaskComposer attachments', () => {
  it('keeps selected files visible while announcing a typed total-size error', () => {
    const onAttachmentsAdd = vi.fn();
    render(
      <TaskComposer
        {...composerProps({
          attachments: [new File(['a'], 'existing.txt')],
          attachmentError: 'Attachments can total at most 100 MiB per Run.',
          onAttachmentsAdd,
        })}
      />,
    );

    expect(screen.getByTitle('existing.txt')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('Attachments can total at most 100 MiB per Run.');
    const input = screen.getByTestId('composer-attachment-input');
    expect((input as HTMLInputElement).disabled).toBe(false);
    expect((input as HTMLInputElement).tabIndex).toBe(-1);
    const candidate = new File(['b'], 'candidate.txt');
    fireEvent.change(input, { target: { files: [candidate] } });
    expect(onAttachmentsAdd).toHaveBeenCalledWith([candidate]);
  });

  it('puts attachment and Goal controls in one accessible add menu', () => {
    const onAttachmentsAdd = vi.fn();
    const onGoalModeChange = vi.fn();
    const view = render(
      <TaskComposer
        {...composerProps({ onAttachmentsAdd, onGoalModeChange })}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    expect(screen.getByRole('menu')).toBeTruthy();
    expect(screen.getByRole('menuitem', { name: 'Add attachments' })).toBeTruthy();
    const goalItem = screen.getByRole('menuitem', { name: 'Goal mode · Enable' });
    expect(goalItem.hasAttribute('aria-pressed')).toBe(false);
    fireEvent.click(goalItem);
    expect(onGoalModeChange).toHaveBeenCalledWith(true);

    view.rerender(
      <TaskComposer
        {...composerProps({ goalMode: true, onAttachmentsAdd, onGoalModeChange })}
      />,
    );
    expect(screen.getByTestId('composer-goal-active').textContent).toContain('Goal mode');
    fireEvent.click(screen.getByRole('button', { name: 'Turn off Goal mode' }));
    expect(onGoalModeChange).toHaveBeenLastCalledWith(false);

    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Add attachments' }));
    const candidate = new File(['b'], 'candidate.txt');
    fireEvent.change(screen.getByTestId('composer-attachment-input'), {
      target: { files: [candidate] },
    });
    expect(onAttachmentsAdd).toHaveBeenCalledWith([candidate]);
  });

  it('disables a full attachment item and skips it during keyboard navigation', async () => {
    const attachments = Array.from(
      { length: 10 },
      (_, index) => new File([String(index)], `file-${index}.txt`),
    );
    render(<TaskComposer {...composerProps({ attachments })} />);

    await act(async () => {
      fireEvent.click(screen.getByTestId('composer-add-menu'));
    });

    const menu = await screen.findByRole('menu');
    const attachmentItem = screen.getByRole('menuitem', { name: /^Add attachments/ });
    const goalItem = screen.getByRole('menuitem', { name: 'Goal mode · Enable' });
    expect(attachmentItem.getAttribute('aria-disabled')).toBe('true');
    await act(async () => {
      fireEvent.keyDown(menu, { key: 'Home' });
    });
    await waitFor(() => {
      expect(attachmentItem.hasAttribute('data-focus')).toBe(false);
      expect(goalItem.hasAttribute('data-focus')).toBe(true);
    });
  });
});

describe('TaskComposer keyboard submission', () => {
  it('submits on Enter but preserves Shift+Enter and IME composition', () => {
    const onSubmit = vi.fn((event: FormEvent<HTMLFormElement>) => event.preventDefault());
    render(<TaskComposer {...composerProps({ prompt: 'ship it', onSubmit })} />);
    const input = screen.getByTestId('run-input');

    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true });
    fireEvent.keyDown(input, { key: 'Enter', isComposing: true });
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 229 });
    expect(onSubmit).not.toHaveBeenCalled();

    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it('does not submit from Enter while busy or unconfigured', () => {
    const onSubmit = vi.fn((event: FormEvent<HTMLFormElement>) => event.preventDefault());
    const view = render(
      <TaskComposer {...composerProps({ prompt: 'ship it', busy: true, onSubmit })} />,
    );

    fireEvent.keyDown(screen.getByTestId('run-input'), { key: 'Enter' });
    expect(onSubmit).not.toHaveBeenCalled();

    view.rerender(
      <TaskComposer
        {...composerProps({ prompt: 'ship it', configured: false, onSubmit })}
      />,
    );
    fireEvent.keyDown(screen.getByTestId('run-input'), { key: 'Enter' });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('toggles Goal without submitting the form', () => {
    const onSubmit = vi.fn((event: FormEvent<HTMLFormElement>) => event.preventDefault());
    const onGoalModeChange = vi.fn();
    render(
      <TaskComposer
        {...composerProps({ prompt: 'ship it', onSubmit, onGoalModeChange })}
      />,
    );

    fireEvent.click(screen.getByTestId('composer-add-menu'));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Goal mode · Enable' }));
    expect(onGoalModeChange).toHaveBeenCalledWith(true);
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
