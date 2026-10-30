import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { RestoreProgressPage } from './RestoreProgress';
import { forgetRestore, trackRestore } from '../restoreTracking';
import { setSession } from '../api';

const operation='b'.repeat(32);
let client:QueryClient;
function show(id=operation){client=new QueryClient({defaultOptions:{queries:{retry:false}}});render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={['/restore-progress/'+id]}><Routes><Route path="/restore-progress/:id" element={<RestoreProgressPage/>}/></Routes></MemoryRouter></App></QueryClientProvider>);}
beforeEach(()=>{setSession(null);const style=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>style(element));vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}));});
afterEach(()=>{cleanup();client?.clear();forgetRestore(operation);vi.restoreAllMocks();vi.unstubAllGlobals();setSession(null);});

it('uses only the operation token and needs no administrator session',async()=>{
  trackRestore(operation,'read-only-token');const fetcher=vi.fn(async(_url:RequestInfo|URL,_init?:RequestInit)=>new Response(JSON.stringify({id:operation,state:'succeeded',message:'恢复完成'})));vi.stubGlobal('fetch',fetcher);show();
  expect(await screen.findByText('恢复已完成')).toBeInTheDocument();expect(fetcher).toHaveBeenCalledTimes(1);expect(String(fetcher.mock.calls[0][0])).toBe('/api/v1/admin/backups/operations/'+operation);expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('X-Restore-Status-Token')).toBe('read-only-token');expect(localStorage.length).toBe(0);expect(sessionStorage.length).toBe(0);
});
it('does not reuse a credential for a different operation',async()=>{
  trackRestore(operation,'scoped-token');const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);show('c'.repeat(32));expect(await screen.findByText('需要重新登录后核对结果')).toBeInTheDocument();expect(fetcher).not.toHaveBeenCalled();
});
it('drops expired in-memory credentials and asks for a new login',async()=>{
  trackRestore(operation,'expired-token',Date.now()-1);const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);show();expect(await screen.findByText('需要重新登录后核对结果')).toBeInTheDocument();expect(fetcher).not.toHaveBeenCalled();
});
it('reconnects by querying the same operation without submitting another restore',async()=>{
  trackRestore(operation,'read-only-token');const fetcher=vi.fn().mockRejectedValueOnce(new Error('server restarting')).mockResolvedValue(new Response(JSON.stringify({id:operation,state:'rolled_back',message:'已恢复操作前数据'})));vi.stubGlobal('fetch',fetcher);show();
  expect(await screen.findByText('服务暂时无法连接，恢复结果尚未确认；请勿重复提交。')).toBeInTheDocument();fireEvent.click(screen.getByRole('button',{name:'刷新状态'}));expect(await screen.findByText('目标恢复未完成，已恢复操作前数据')).toBeInTheDocument();await waitFor(()=>expect(fetcher.mock.calls.length).toBeGreaterThan(1));expect(fetcher.mock.calls.every(([url,init])=>String(url).endsWith('/operations/'+operation)&&!init?.method)).toBe(true);
});
