import { afterEach, describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { api, post, refreshSession, setSession } from './api';
import { bindSessionCache } from './sessionCache';
import type { Session } from './types';
const first:Session={user:{id:'one',username:'first'},csrf_token:'csrf-first'};
const second:Session={user:{id:'two',username:'second'},csrf_token:'csrf-second'};
const json=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
afterEach(()=>{setSession(null);vi.unstubAllGlobals()});
describe('session lifecycle',()=>{
 it('does not sign an administrator out when a visitor API reports an upstream 401',async()=>{
  setSession(first);const fetcher=vi.fn().mockResolvedValueOnce(json({error:{message:'upstream key rejected'}},401)).mockResolvedValueOnce(json({}));vi.stubGlobal('fetch',fetcher);
  await expect(api('/test/models',{method:'POST',body:'{}'})).rejects.toMatchObject({status:401});
  await post('/admin/settings',{});
  expect(new Headers(fetcher.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('csrf-first');
 });
 it('does not restore an old in-flight session after logout',async()=>{
  setSession(first);let resolve!:(value:Response)=>void;const fetcher=vi.fn().mockImplementationOnce(()=>new Promise<Response>(done=>{resolve=done})).mockResolvedValueOnce(json({}));vi.stubGlobal('fetch',fetcher);
  const pending=refreshSession();setSession(null);resolve(json(first));await pending;await post('/admin/settings',{});
  expect(new Headers(fetcher.mock.calls[1][1].headers).has('X-CSRF-Token')).toBe(false);
 });
 it('ignores an older protected 401 after a different administrator signs in',async()=>{
  setSession(first);let resolve!:(value:Response)=>void;const fetcher=vi.fn().mockImplementationOnce(()=>new Promise<Response>(done=>{resolve=done})).mockResolvedValueOnce(json({}));vi.stubGlobal('fetch',fetcher);
  const pending=api('/admin/models');setSession(second);resolve(json({error:{message:'expired'}},401));await expect(pending).rejects.toMatchObject({status:401});await post('/admin/settings',{});
  expect(new Headers(fetcher.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('csrf-second');
 });
 it('removes protected cached data on logout while preserving public results',()=>{
  const client=new QueryClient();const unbind=bindSessionCache(client);client.setQueryData(['admin-models'],[{id:'private'}]);client.setQueryData(['session'],first);client.setQueryData(['monitor'],{models:[]});setSession(first);setSession(null);
  expect(client.getQueryData(['admin-models'])).toBeUndefined();expect(client.getQueryData(['session'])).toBeUndefined();expect(client.getQueryData(['monitor'])).toEqual({models:[]});unbind();client.clear();
 });
});
