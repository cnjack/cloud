import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AccountPreferencesBoundary } from './AccountPreferencesBoundary';
const state=vi.hoisted(()=>({userID:'alice',sendKey:'mod_enter',language:'en',theme:'light'}));
const apply=vi.hoisted(()=>({locale:vi.fn(),theme:vi.fn()}));
vi.mock('../api/accountProfile',()=>({useAccountProfile:()=>({data:{preferences:{send_key:state.sendKey,language:state.language,theme:state.theme}}})}));
vi.mock('../auth/AuthProvider',()=>({useOptionalAuth:()=>({me:{user:{id:state.userID}}})}));
vi.mock('../i18n',()=>({setLocale:apply.locale}));
vi.mock('../theme',()=>({setTheme:apply.theme}));
beforeEach(()=>{vi.clearAllMocks();state.userID='alice';state.sendKey='mod_enter';state.language='en';state.theme='light';});
describe('Persisted account preferences',()=>{
 it('lets the shared composer select a slash command with Enter',()=>{
  const keydown=vi.fn();
  render(<AccountPreferencesBoundary><div className="jcode-product"><div className="jcode-chat-input"><div className="jcode-chat-input__slash"><button>/help</button></div><textarea aria-label="Chat" onKeyDown={keydown}/></div></div></AccountPreferencesBoundary>);
  fireEvent.keyDown(screen.getByLabelText('Chat'),{key:'Enter'});
  expect(keydown).toHaveBeenCalledTimes(1);
 });
 it('keeps plain Enter as a newline and leaves modified Enter, IME, and unrelated fields to their owners',()=>{
  const keydown=vi.fn();
  render(<AccountPreferencesBoundary><div className="jcode-product"><textarea aria-label="Chat" onKeyDown={keydown}/></div><textarea aria-label="Notes" onKeyDown={keydown}/></AccountPreferencesBoundary>);
  const chat=screen.getByLabelText('Chat');
  expect(fireEvent.keyDown(chat,{key:'Enter'})).toBe(true);
  expect(keydown).not.toHaveBeenCalled();
  fireEvent.keyDown(chat,{key:'Enter',ctrlKey:true});
  fireEvent.keyDown(chat,{key:'Enter',isComposing:true});
  fireEvent.keyDown(screen.getByLabelText('Notes'),{key:'Enter'});
  expect(keydown).toHaveBeenCalledTimes(3);
 });
 it('applies visual defaults once for each account and preserves later local choices',()=>{
  const {rerender}=render(<AccountPreferencesBoundary><span>Content</span></AccountPreferencesBoundary>);
  expect(apply.locale).toHaveBeenCalledWith('en');expect(apply.theme).toHaveBeenCalledWith('light');
  state.theme='dark';rerender(<AccountPreferencesBoundary><span>Content</span></AccountPreferencesBoundary>);
  expect(apply.theme).toHaveBeenCalledTimes(1);
  state.userID='bob';rerender(<AccountPreferencesBoundary><span>Content</span></AccountPreferencesBoundary>);
  expect(apply.theme).toHaveBeenLastCalledWith('dark');expect(apply.theme).toHaveBeenCalledTimes(2);
 });
});
