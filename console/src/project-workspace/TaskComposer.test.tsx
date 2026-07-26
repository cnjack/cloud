import { fireEvent, render, screen } from '@testing-library/react';
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

describe('TaskComposer attachments', () => {
  it('keeps selected files visible while announcing a typed total-size error', () => {
    const onAttachmentsAdd = vi.fn();
    render(
      <TaskComposer
        service={service}
        notice={null}
        configured
        prompt=""
        onPromptChange={() => {}}
        models={[]}
        selectedModel=""
        onSelectedModelChange={() => {}}
        effortEnabled={false}
        modelEffort="auto"
        onModelEffortChange={() => {}}
        goalMode={false}
        onGoalModeChange={() => {}}
        attachments={[new File(['a'], 'existing.txt')]}
        attachmentError="Attachments can total at most 100 MiB per Run."
        onAttachmentsAdd={onAttachmentsAdd}
        onAttachmentRemove={() => {}}
        branches={[{ name: 'main', default: true }]}
        branchesLoading={false}
        branchesError={false}
        selectedBranch="main"
        onSelectedBranchChange={() => {}}
        askApproval={false}
        onAskApprovalChange={() => {}}
        onSubmit={(event) => event.preventDefault()}
        busy={false}
      />,
    );

    expect(screen.getByTitle('existing.txt')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('Attachments can total at most 100 MiB per Run.');
    const input = screen.getByTestId('composer-attachment-input');
    expect((input as HTMLInputElement).disabled).toBe(false);
    const candidate = new File(['b'], 'candidate.txt');
    fireEvent.change(input, { target: { files: [candidate] } });
    expect(onAttachmentsAdd).toHaveBeenCalledWith([candidate]);
  });
});
