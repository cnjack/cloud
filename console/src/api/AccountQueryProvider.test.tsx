import { useQuery } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { AccountQueryProvider } from './AccountQueryProvider';
const auth=vi.hoisted(()=>({status:'ready',me:{is_service:false,user:{id:'alice'}}}));
vi.mock('../auth/AuthProvider',()=>({useOptionalAuth:()=>auth}));
function Page({load}:{load:()=>Promise<string>}) {const q=useQuery({queryKey:['private-settings'],queryFn:load});return <div>{q.data ?? 'Loading private settings'}</div>;}
describe('Account query isolation',()=>{
 it('never renders a previous account cache while the next account query is pending',async()=>{
  const {rerender}=render(<AccountQueryProvider><Page load={async()=> 'Alice private provider'} /></AccountQueryProvider>);
  await screen.findByText('Alice private provider');
  auth.me.user.id='bob';
  rerender(<AccountQueryProvider><Page load={()=>new Promise(()=>{})} /></AccountQueryProvider>);
  expect(screen.queryByText('Alice private provider')).toBeNull();
  expect(screen.getByText('Loading private settings')).toBeTruthy();
 });
});
