import { afterEach, describe, expect, it, vi } from 'vitest';
import { api,setSession } from './api';
import { refreshUserSession,safeUserNext,setUserSession,userApi,userRequestHeaders,onUserSessionCleared } from './userApi';

afterEach(()=>{setUserSession(null);setSession(null);vi.unstubAllGlobals()});
describe('ordinary user API session isolation',()=>{
 it('clears private state when switching between member identities',()=>{setUserSession({user:{id:'a',username:'alice'},csrf_token:'a'});const clear=vi.fn();const unsubscribe=onUserSessionCleared(clear);setUserSession({user:{id:'b',username:'bob'},csrf_token:'b'});expect(clear).toHaveBeenCalledOnce();unsubscribe()});
 it('uses the ordinary CSRF token for tests and preserves the administrator token separately',async()=>{
  setSession({user:{id:'admin',username:'admin'},csrf_token:'admin-csrf'});
  setUserSession({user:{id:'member',username:'member'},csrf_token:'member-csrf'});
  const fetcher=vi.fn().mockImplementation(()=>Promise.resolve(new Response('{}',{status:200})));
  vi.stubGlobal('fetch',fetcher);
  await userApi('/test/models',{method:'POST',body:'{}'});
  await api('/admin/settings',{method:'PUT',body:'{}'});
  expect(fetcher.mock.calls[0][0]).toBe('/api/v1/test/models');
  expect(new Headers(fetcher.mock.calls[0][1].headers).get('X-CSRF-Token')).toBe('member-csrf');
  expect(new Headers(fetcher.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('admin-csrf');
  expect(userRequestHeaders().get('X-CSRF-Token')).toBe('member-csrf');
 });
 it('handles anonymous member checks without creating administrator sessions',async()=>{
  vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response('{"error":{"code":"unauthorized","message":"登录"}}',{status:401})));
  expect(await refreshUserSession()).toBeNull();
  expect(userRequestHeaders().get('X-CSRF-Token')).toBeNull();
 });
 it('does not restore a stale member session after logout while me is pending',async()=>{
  let finish!:(response:Response)=>void;
  vi.stubGlobal('fetch',vi.fn().mockImplementation(()=>new Promise<Response>(resolve=>{finish=resolve})));
  const pending=refreshUserSession();setUserSession(null);
  finish(new Response(JSON.stringify({user:{id:'old',username:'old'},csrf_token:'old-token'})));
  await pending;expect(userRequestHeaders().get('X-CSRF-Token')).toBeNull();
 });
 it('only permits member return destinations',()=>{
  expect(safeUserNext('/test?mode=svg#preview')).toBe('/test?mode=svg#preview');
  expect(safeUserNext('/account/connections')).toBe('/account/connections');
  expect(safeUserNext('/account/tests/'+ 'a'.repeat(32))).toBe('/account/tests/'+ 'a'.repeat(32));
  for(const value of ['//evil.test','/admin','/admin/settings','https://evil.test','/\\evil.test','/%2f%2fevil.test','/login?next=/test'])expect(safeUserNext(value)).toBe('/account');
 });
});
