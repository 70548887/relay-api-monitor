import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setSession } from '../api';
import { bindSessionCache } from '../sessionCache';
import { AdminLayout, AdminLoginPage, AdminSetupPage } from './AdminAuth';
import { PublicLayout } from './PublicLayout';

const memberState = vi.hoisted(() => ({ signedIn: false }));
vi.mock('../userApi', () => ({
  useUserSession: () => memberState.signedIn ? { user: { id: 'member-one', username: 'viewer' }, csrf_token: 'member-csrf' } : null,
  refreshUserSession: vi.fn().mockResolvedValue(null),
  onUserSessionCleared: () => () => {},
}));

const session = { user: { id: 'admin-one', username: 'operator' }, csrf_token: 'csrf-one' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;
let unbind: () => void;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  unbind = bindSessionCache(client);
  memberState.signedIn = false;
  setSession(null);
});

afterEach(() => { cleanup(); unbind(); client.clear(); setSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function Location() { const location = useLocation(); return <output data-testid="location">{location.pathname}{location.search}{location.hash}</output>; }

function renderAdmin(path: string) {
  render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={[path]}><Location/><Routes>
    <Route path="/admin/login" element={<AdminLoginPage/>}/>
    <Route path="/admin/setup" element={<AdminSetupPage/>}/>
    <Route path="/admin" element={<AdminLayout/>}><Route index element={<h1>工作台</h1>}/><Route path="settings" element={<h1>设置内容</h1>}/></Route>
  </Routes></MemoryRouter></App></QueryClientProvider>);
}

describe('independent public and administration portals', () => {
  it('shows public navigation and member entry without requesting administrator state', () => {
    const fetcher = vi.fn();
    vi.stubGlobal('fetch', fetcher);
    render(<QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<PublicLayout/>}><Route index element={<h1>公开监控</h1>}/></Route></Routes></MemoryRouter></QueryClientProvider>);
    expect(screen.getByRole('navigation', { name: '主导航' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '登录' })).toHaveAttribute('href', '/login');
    expect(screen.getByRole('link', { name: '注册' })).toHaveAttribute('href', '/register');
    expect(screen.queryByRole('link', { name: '管理后台' })).not.toBeInTheDocument();
    expect(document.querySelector('a[href^="/admin"]')).toBeNull();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('redirects an unauthenticated deep link and returns there after administrator login', async () => {
    let loggedIn = false;
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: string) => {
      if (url.endsWith('/setup/status')) return Promise.resolve(json({ initialized: true }));
      if (url.endsWith('/auth/login')) { loggedIn = true; return Promise.resolve(json(session)); }
      return Promise.resolve(loggedIn ? json(session) : json({ error: { message: '请登录' } }, 401));
    }));
    renderAdmin('/admin/settings?tab=security#password');
    expect(await screen.findByRole('heading', { name: '欢迎回来', level: 1 })).toBeInTheDocument();
    expect(screen.getByTestId('location').textContent).toBe('/admin/login?next=%2Fadmin%2Fsettings%3Ftab%3Dsecurity%23password');
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'operator' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'fixture-password' } });
    fireEvent.click(screen.getByRole('button', { name: '登录工作台' }));
    expect(await screen.findByRole('heading', { name: '设置内容' })).toBeInTheDocument();
    expect(screen.getByTestId('location').textContent).toBe('/admin/settings?tab=security#password');
    expect(screen.queryByRole('navigation', { name: '主导航' })).not.toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: '管理导航' })).toBeInTheDocument();
  });

  it('restricts first-run setup to its dedicated page', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ initialized: false })));
    renderAdmin('/admin/settings');
    expect(await screen.findByRole('heading', { name: '创建首个管理员', level: 1 })).toBeInTheDocument();
    expect(screen.getByTestId('location').textContent).toBe('/admin/setup?next=%2Fadmin%2Fsettings');
    expect(screen.queryByRole('navigation', { name: '主导航' })).not.toBeInTheDocument();
  });

  it('keeps a signed-in ordinary member behind the administrator login', async () => {
    memberState.signedIn = true;
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: string) => Promise.resolve(url.endsWith('/setup/status') ? json({ initialized: true }) : json({ error: { message: '请登录管理员' } }, 401))));
    renderAdmin('/admin/settings');
    expect(await screen.findByRole('heading', { name: '欢迎回来', level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '设置内容' })).not.toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
  });

  it('returns logout to the administrator login and removes protected caches', async () => {
    let loggedIn = true;
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: string) => {
      if (url.endsWith('/setup/status')) return Promise.resolve(json({ initialized: true }));
      if (url.endsWith('/auth/logout')) { loggedIn = false; return Promise.resolve(new Response(null, { status: 204 })); }
      return Promise.resolve(loggedIn ? json(session) : json({ error: { message: '请登录' } }, 401));
    }));
    client.setQueryData(['admin-models'], [{ id: 'private-model' }]);
    client.setQueryData(['monitor'], { models: [] });
    renderAdmin('/admin');
    expect(await screen.findByRole('heading', { name: '工作台' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '退出登录' }));
    expect(await screen.findByRole('heading', { name: '欢迎回来', level: 1 })).toBeInTheDocument();
    expect(screen.getByTestId('location').textContent).toBe('/admin/login');
    expect(client.getQueryData(['admin-models'])).toBeUndefined();
    expect(client.getQueryData(['monitor'])).toEqual({ models: [] });
  });
});
