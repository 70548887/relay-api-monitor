import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setSession } from '../api';
import { UsersPage } from './Users';

const administrator = { user: { id: 'admin-one', username: 'operator' }, csrf_token: 'admin-csrf' };
const firstMember = { id: 'member-one', username: 'member-viewer', disabled: false, created_at: '' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  setSession(administrator);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); setSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function show() { render(<QueryClientProvider client={client}><App><UsersPage/></App></QueryClientProvider>); }

describe('administrator account management', () => {
  it('loads member and administrator lists separately and disables actions for the current administrator', async () => {
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(json({ items: url.endsWith('/members') ? [{ ...firstMember, password: 'should-never-be-rendered', password_hash: 'hash-must-stay-hidden' }] : [{ ...administrator.user, disabled: false, created_at: '', is_current: true }, { id: 'admin-two', username: 'other-administrator', disabled: false, created_at: '' }] })));
    vi.stubGlobal('fetch', fetcher);
    show();
    expect(await screen.findByText('member-viewer')).toBeInTheDocument();
    expect(screen.queryByText('should-never-be-rendered')).not.toBeInTheDocument();
    expect(screen.queryByText('hash-must-stay-hidden')).not.toBeInTheDocument();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/members', expect.any(Object));
    fireEvent.click(screen.getByRole('tab', { name: '管理员账号' }));
    expect(await screen.findByText('other-administrator')).toBeInTheDocument();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/users', expect.any(Object));
    const currentRow = screen.getByText('operator').closest('tr')!;
    expect(within(currentRow).getByRole('button', { name: /停\s*用/ })).toBeDisabled();
    expect(within(currentRow).getByRole('button', { name: '重置密码' })).toBeDisabled();
    expect(within(currentRow).getByText('当前账号')).toBeInTheDocument();
    const otherRow = screen.getByText('other-administrator').closest('tr')!;
    expect(within(otherRow).getByRole('button', { name: /停\s*用/ })).toBeEnabled();
    expect(within(otherRow).getByRole('button', { name: '重置密码' })).toBeEnabled();
  });

  it('waits for member creation, then refreshes the list without displaying the password', async () => {
    let finish!: (response: Response) => void;
    let created = false;
    const fetcher = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/members') && init?.method === 'POST') return new Promise<Response>(resolve => { finish = response => { created = true; resolve(response); }; });
      return Promise.resolve(json({ items: created ? [firstMember, { id: 'member-two', username: 'new-member', disabled: false, created_at: '' }] : [firstMember] }));
    });
    vi.stubGlobal('fetch', fetcher);
    show();
    expect(await screen.findByText('member-viewer')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '新增普通用户' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'new-member' } });
    fireEvent.change(screen.getByLabelText('初始密码'), { target: { value: 'initial-password-123' } });
    fireEvent.click(screen.getByRole('button', { name: '创建账号' }));
    await waitFor(() => expect(finish).toBeTypeOf('function'));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.queryByText('new-member', { selector: 'td *' })).not.toBeInTheDocument();
    const create = fetcher.mock.calls.find(call => call[1]?.method === 'POST')!;
    expect(create[0]).toBe('/api/v1/admin/members');
    expect(JSON.parse(String(create[1]?.body))).toEqual({ username: 'new-member', password: 'initial-password-123' });
    expect(new Headers(create[1]?.headers).get('X-CSRF-Token')).toBe('admin-csrf');
    await act(async () => { finish(json({ id: 'member-two' }, 201)); });
    expect(await screen.findByText('new-member')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.queryByText('initial-password-123')).not.toBeInTheDocument();
    expect(fetcher.mock.calls.filter(call => call[0] === '/api/v1/admin/members' && !call[1]?.method).length).toBe(2);
  });

  it('shows password reset errors in the modal and posts an administrator reset only after validation', async () => {
    const fetcher = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'POST') return Promise.resolve(json({ error: { message: '密码重置失败，请重试' } }, 400));
      return Promise.resolve(json({ items: url.endsWith('/users') ? [{ id: 'admin-two', username: 'other-administrator', disabled: false, created_at: '' }] : [] }));
    });
    vi.stubGlobal('fetch', fetcher);
    show();
    fireEvent.click(screen.getByRole('tab', { name: '管理员账号' }));
    expect(await screen.findByText('other-administrator')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '重置密码' }));
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'short' } });
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '重置密码' }));
    expect(await screen.findByText('密码至少 12 位')).toBeInTheDocument();
    expect(fetcher.mock.calls.filter(call => call[1]?.method === 'POST')).toHaveLength(0);
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'reset-password-123' } });
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '重置密码' }));
    expect(await within(screen.getByRole('dialog')).findByText('密码重置失败，请重试')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    const reset = fetcher.mock.calls.find(call => call[1]?.method === 'POST')!;
    expect(reset[0]).toBe('/api/v1/admin/users/admin-two/reset-password');
    expect(JSON.parse(String(reset[1]?.body))).toEqual({ password: 'reset-password-123' });
  });
});
