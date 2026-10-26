import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setSession, useSession } from '../api';
import { bindSessionCache } from '../sessionCache';
import { setUserSession, useUserSession } from '../userApi';
import { UserAccountPage, UserLoginPage, UserRegisterPage } from './UserAccount';

const member = { user: { id: 'member-one', username: 'same-name' }, csrf_token: 'member-csrf' };
const admin = { user: { id: 'admin-one', username: 'same-name' }, csrf_token: 'admin-csrf' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;
let unbind: () => void;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  setSession(null);
  setUserSession(null);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  unbind = bindSessionCache(client);
});
afterEach(() => { cleanup(); unbind(); client.clear(); setSession(null); setUserSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function Sessions() {
  const user = useUserSession();
  const administrator = useSession();
  return <output data-testid="sessions">{user?.user.id || 'no-member'} / {administrator?.user.id || 'no-admin'}</output>;
}
function show(path: string) {
  render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={[path]}><Sessions/><Routes>
    <Route path="/register" element={<UserRegisterPage/>}/>
    <Route path="/login" element={<UserLoginPage/>}/>
    <Route path="/account" element={<UserAccountPage/>}/>
    <Route path="/test" element={<h1>在线测试入口</h1>}/>
  </Routes></MemoryRouter></App></QueryClientProvider>);
}

describe('ordinary member forms', () => {
  it('validates confirmation, registers with only username and password, and starts the member session', async () => {
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(url.endsWith('/auth/register') ? json(member, 201) : json({ error: { message: '请登录' } }, 401)));
    vi.stubGlobal('fetch', fetcher);
    show('/register?next=%2Ftest');
    expect(await screen.findByRole('heading', { name: '创建你的用户账号' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: member.user.username } });
    fireEvent.change(screen.getByLabelText('密码', { exact: true }), { target: { value: 'member-password-123' } });
    fireEvent.change(screen.getByLabelText('确认密码'), { target: { value: 'different-password' } });
    fireEvent.click(screen.getByRole('button', { name: '注册并登录' }));
    expect(await screen.findByText('两次密码不一致')).toBeInTheDocument();
    expect(fetcher.mock.calls.filter(call => String(call[0]).endsWith('/auth/register'))).toHaveLength(0);
    fireEvent.change(screen.getByLabelText('确认密码'), { target: { value: 'member-password-123' } });
    fireEvent.click(screen.getByRole('button', { name: '注册并登录' }));
    expect(await screen.findByRole('heading', { name: '在线测试入口' })).toBeInTheDocument();
    expect(screen.getByTestId('sessions').textContent).toBe('member-one / no-admin');
    expect(client.getQueryData(['user-session'])).toEqual(member);
    const registration = fetcher.mock.calls.find(call => String(call[0]).endsWith('/auth/register'))!;
    expect(JSON.parse(registration[1].body)).toEqual({ username: member.user.username, password: 'member-password-123' });
  });

  it('logs the same username into the member API without replacing an administrator session', async () => {
    setSession(admin);
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(url.endsWith('/auth/login') ? json(member) : json({ error: { message: '请登录' } }, 401)));
    vi.stubGlobal('fetch', fetcher);
    show('/login?next=%2Ftest');
    expect(await screen.findByRole('button', { name: '登录用户端' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'same-name' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'member-password-123' } });
    fireEvent.click(screen.getByRole('button', { name: '登录用户端' }));
    expect(await screen.findByRole('heading', { name: '在线测试入口' })).toBeInTheDocument();
    expect(screen.getByTestId('sessions').textContent).toBe('member-one / admin-one');
    const login = fetcher.mock.calls.find(call => call[0] === '/api/v1/user/auth/login')!;
    expect(login).toBeDefined();
    expect(new Headers(login[1].headers).has('X-CSRF-Token')).toBe(false);
    expect(fetcher.mock.calls.every(call => String(call[0]).startsWith('/api/v1/user/'))).toBe(true);
  });

  it('shows a wrong-password error and signs out the member after a successful password change', async () => {
    let changed = false;
    let attempts = 0;
    const fetcher = vi.fn().mockImplementation((url: string) => {
      if (url.endsWith('/auth/password')) {
        if (++attempts === 1) return Promise.resolve(json({ error: { message: '原密码不正确' } }, 400));
        changed = true;
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return Promise.resolve(changed ? json({ error: { message: '请登录' } }, 401) : json(member));
    });
    vi.stubGlobal('fetch', fetcher);
    show('/account');
    expect(await screen.findByRole('heading', { name: '我的账号' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'old-password' } });
    fireEvent.change(screen.getByLabelText('新密码', { exact: true }), { target: { value: 'new-member-password-123' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'new-member-password-123' } });
    fireEvent.click(screen.getByRole('button', { name: '保存新密码' }));
    expect(await screen.findByText('原密码不正确')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '我的账号' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'correct-old-password' } });
    fireEvent.click(screen.getByRole('button', { name: '保存新密码' }));
    expect(await screen.findByRole('button', { name: '登录用户端' })).toBeInTheDocument();
    expect(screen.getByTestId('sessions').textContent).toBe('no-member / no-admin');
    const writes = fetcher.mock.calls.filter(call => String(call[0]).endsWith('/auth/password'));
    expect(JSON.parse(writes[1][1].body)).toEqual({ old_password: 'correct-old-password', new_password: 'new-member-password-123' });
    expect(new Headers(writes[1][1].headers).get('X-CSRF-Token')).toBe('member-csrf');
  });

  it('logs out the member and clears only member caches', async () => {
    setSession(admin);
    client.setQueryData(['admin-models'], [{ id: 'admin-cache' }]);
    client.setQueryData(['user-private'], { account: 'private' });
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(url.endsWith('/auth/logout') ? new Response(null, { status: 204 }) : json(member)));
    vi.stubGlobal('fetch', fetcher);
    show('/account');
    expect(await screen.findByRole('button', { name: '退出用户登录' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '退出用户登录' }));
    expect(await screen.findByRole('button', { name: '登录用户端' })).toBeInTheDocument();
    expect(screen.getByTestId('sessions').textContent).toBe('no-member / admin-one');
    await waitFor(() => expect(client.getQueryData(['user-private'])).toBeUndefined());
    expect(client.getQueryData(['admin-models'])).toEqual([{ id: 'admin-cache' }]);
    const logout = fetcher.mock.calls.find(call => String(call[0]).endsWith('/auth/logout'))!;
    expect(new Headers(logout[1].headers).get('X-CSRF-Token')).toBe('member-csrf');
  });
});
