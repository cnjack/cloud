import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountProfile, ProjectModel, RunAttachmentIntent } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { setLocale } from '../i18n';
import { AccountRepositoryComposer } from './AccountRepositoryComposer';
vi.mock('../auth/AuthProvider', () => ({ useOptionalAuth: () => ({ me: { user: { id: 'account-1' } } }) }));
const models: ProjectModel[] = ['granted','personal'].map(id => ({id,name:id,model_name:'openai/same-upstream',capabilities:{tools:true,reasoning:true,image:false}}));
const profile: AccountProfile = {display_name:'Jack',preferences:{default_model_id:'personal',permission_mode:'plan',effort:'high',send_key:'enter',language:'en',theme:'light'}};
function show(overrides: Partial<ApiClient> = {}) {
  const startAccountTask = vi.fn(async () => ({run:{id:'new-task'},repository:{id:'repo'}}));
  const client = {getAccountProfile:async()=>profile,listAccountRepositoryBranches:async()=>[{name:'main',default:true}],startAccountTask,...overrides} as unknown as ApiClient;
  render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><ApiProvider client={client}><ToastProvider><MemoryRouter><AccountRepositoryComposer target={{provider:'github',provider_repo_id:'42',full_name:'acme/app',default_branch:'main',private:true}} models={models} modelsLoading={false} accountId="account-1" contextPicker={<span>acme/app</span>} /></MemoryRouter></ToastProvider></ApiProvider></QueryClientProvider>);
  return {startAccountTask};
}
const intent: RunAttachmentIntent = {stage:{id:'stage-1',project_id:'private',display_name:'brief.txt',content_type:'text/plain',size_bytes:5,created_at:new Date().toISOString(),expires_at:new Date(Date.now()+600000).toISOString()},upload_url:'/upload',expires_at:new Date(Date.now()+600000).toISOString()};
describe('Account task submission',()=>{
  beforeEach(async()=>{window.localStorage.clear();await setLocale('en');});
  it('submits persisted defaults using the personal catalog ID despite an identical granted upstream model',async()=>{
    const {startAccountTask}=show();
    const input=await screen.findByLabelText('Describe a task');
    await screen.findByRole('button',{name:'Plan'});
    fireEvent.change(input,{target:{value:'Plan a checkout fix'}});
    fireEvent.click(screen.getByRole('button',{name:'Start task'}));
    await waitFor(()=>expect(startAccountTask).toHaveBeenCalledWith(expect.objectContaining({model_id:'personal',permission_mode:'plan',model_effort:'high'})));
  });
  it('blocks a failed file upload, retries it, and submits the completed stage',async()=>{
    const upload=vi.fn().mockRejectedValueOnce(new Error('Storage temporarily unavailable')).mockResolvedValueOnce(intent);
    const {startAccountTask}=show({uploadAccountAttachment:upload});
    fireEvent.change(await screen.findByLabelText('Describe a task'),{target:{value:'Read brief'}});
    fireEvent.change(screen.getByLabelText('Add files'),{target:{files:[new File(['brief'],'brief.txt',{type:'text/plain'})]}});
    await screen.findByText('Storage temporarily unavailable');
    expect((screen.getByRole('button',{name:'Start task'}) as HTMLButtonElement).disabled).toBe(true);
    expect(startAccountTask).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button',{name:'Retry'}));
    await screen.findByText('Attached');
    fireEvent.click(screen.getByRole('button',{name:'Start task'}));
    await waitFor(()=>expect(startAccountTask).toHaveBeenCalledWith(expect.objectContaining({attachment_stage_ids:['stage-1']})));
    expect(upload).toHaveBeenCalledWith('github','42',expect.any(File));
  });
  it('retains a rejected prompt for retry instead of losing it when the shared input clears',async()=>{
    const start=vi.fn().mockRejectedValueOnce(new Error('Busy')).mockResolvedValueOnce({run:{id:'retry'},repository:{id:'repo'}});
    show({startAccountTask:start});
    fireEvent.change(await screen.findByLabelText('Describe a task'),{target:{value:'Keep this task text'}});
    await waitFor(()=>expect((screen.getByRole('button',{name:'Start task'}) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole('button',{name:'Start task'}));
    await screen.findByText('Keep this task text',{selector:'p'});
    fireEvent.click(screen.getByRole('button',{name:'Retry'}));
    await waitFor(()=>expect(start).toHaveBeenCalledTimes(2));
    expect(start.mock.calls[1]![0].prompt).toBe('Keep this task text');
  });
});
