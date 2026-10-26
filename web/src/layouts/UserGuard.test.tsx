import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setSession } from '../api';
import { setUserSession, userApi } from '../userApi';
import { bindSessionCache } from '../sessionCache';
import { RequireMember } from './UserGuard';

const member = { user: { id: 'member-one', username: 'viewer' }, csrf_token: 'member-csrf' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;
let unbind: () => void;

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  unbind = bindSessionCache(client);
  setSession(null);
  setUserSession(null);
});
afterEach(() => { cleanup(); unbind(); client.clear(); setSession(null); setUserSession(null); vi.unstubAllGlobals(); });

function Location() { const location = useLocation(); return <output data-testid="member-location">{location.pathname}{location.search}{location.hash}</output>; }
function renderTest(path = '/test') {
  render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Location/><Routes>
    <Route path="/test" element={<RequireMember><h1>在线测试内容</h1></RequireMember>}/>
    <Route path="/login" element={<h1>用户登录</h1>}/>
  </Routes></MemoryRouter></QueryClientProvider>);
}

describe('online test membership', () => {
  it('requires a member session even when an administrator is already signed in', async () => {
    setSession({ user: { id: 'administrator', username: 'operator' }, csrf_token: 'admin-csrf' });
    const fetcher = vi.fn().mockResolvedValue(json({ error: { message: '请登录' } }, 401));
    vi.stubGlobal('fetch', fetcher);
    renderTest('/test?model=sample#result');
    expect(await screen.findByRole('heading', { name: '用户登录' })).toBeInTheDocument();
    expect(screen.getByTestId('member-location').textContent).toBe('/login?next=%2Ftest%3Fmodel%3Dsample%23result');
    expect(screen.queryByRole('heading', { name: '在线测试内容' })).not.toBeInTheDocument();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/user/auth/me', expect.any(Object));
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('waits for the member cookie to be validated before rendering online test', async () => {
    let resolve!: (response: Response) => void;
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => new Promise<Response>(done => { resolve = done; })));
    renderTest();
    expect(screen.queryByRole('heading', { name: '在线测试内容' })).not.toBeInTheDocument();
    await act(async () => { resolve(json(member)); });
    expect(await screen.findByRole('heading', { name: '在线测试内容' })).toBeInTheDocument();
    expect(screen.getByTestId('member-location').textContent).toBe('/test');
  });

  it('allows a failed member status request to be retried', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(json({ error: { message: '服务暂时不可用' } }, 503)).mockResolvedValueOnce(json(member)));
    renderTest();
    expect(await screen.findByText('用户登录状态读取失败')).toBeInTheDocument();
    expect(screen.getByText('服务暂时不可用')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /重\s*试/ }));
    expect(await screen.findByRole('heading', { name: '在线测试内容' })).toBeInTheDocument();
  });

  it('removes online test content when a member request reports session expiry', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(json(member)).mockImplementation(() => Promise.resolve(json({ error: { message: '会话已过期' } }, 401))));
    renderTest();
    expect(await screen.findByRole('heading', { name: '在线测试内容' })).toBeInTheDocument();
    await act(async () => { await userApi('/auth/me').catch(() => {}); });
    expect(await screen.findByRole('heading', { name: '用户登录' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '在线测试内容' })).not.toBeInTheDocument();
  });
});
