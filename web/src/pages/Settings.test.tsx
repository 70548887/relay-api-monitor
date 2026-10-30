import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setSession, useSession } from '../api';
import { bindSessionCache } from '../sessionCache';
import type { Session, Settings } from '../types';
import { SettingsPage } from './Settings';

const initialSettings: Settings = {
  probe_concurrency: 4,
  artifact_concurrency: 2,
  channel_concurrency: 2,
  probe_interval_seconds: 300,
  artifact_interval_seconds: 1800,
  probe_timeout_seconds: 60,
  artifact_timeout_seconds: 600,
  discovery_timeout_seconds: 20,
  slow_threshold_ms: 10000,
  retention_days: 30,
  online_test_retention_days: 30,
  artifact_history_scope: 'group',
  visitor_concurrency: 8,
  visitor_ip_concurrency: 2,
  visitor_models_per_minute: 5,
  visitor_generations_per_minute: 10,
};

const clients: QueryClient[] = [];
const unbinds: (() => void)[] = [];
function SessionState() {
  const session = useSession();
  return <output data-testid="settings-session">{session ? 'signed-in' : 'signed-out'}</output>;
}

function show(fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => new Response(
  JSON.stringify(init?.method === 'PUT' ? JSON.parse(String(init.body)) : initialSettings),
  { headers: { 'Content-Type': 'application/json' } },
)), options: { cached?: boolean } = {}) {
  vi.stubGlobal('fetch', fetcher);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  if (options.cached !== false) client.setQueryData(['admin-settings'], { ...initialSettings });
  clients.push(client);
  // Keep isolated route assertions synchronous; browser tests cover real transitions.
  const rendered = render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={['/admin/settings']} useTransitions={false}>
    <SessionState/>
    <Routes><Route path="/admin/settings" element={<SettingsPage/>}/><Route path="/admin/login" element={<h1>管理员登录</h1>}/></Routes>
  </MemoryRouter></App></QueryClientProvider>);
  return { client, fetcher, ...rendered };
}

function selectTab(name: string) {
  fireEvent.click(screen.getByRole('tab', { name }));
}

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  setSession(null);
});

afterEach(() => {
  cleanup();
  unbinds.splice(0).forEach(unbind => unbind());
  clients.splice(0).forEach(client => client.clear());
  setSession(null);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('system settings', () => {
  it('opens with five setting categories and keeps password controls in account security', async () => {
    const { fetcher } = show();
    expect(screen.getByRole('heading', { level: 1, name: '系统设置' })).toBeInTheDocument();
    expect(screen.getAllByRole('tab')).toHaveLength(5);
    for (const name of ['任务调度', '请求策略', '数据与历史', '访客额度', '账号安全']) expect(screen.getByRole('tab', { name })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    expect(screen.getByRole('tab', { name: '任务调度' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: '更新密码' })).not.toBeInTheDocument();
    selectTab('账号安全');
    expect(screen.getByLabelText('当前密码')).toBeVisible();
    expect(screen.getByRole('button', { name: '更新密码' })).toBeVisible();
    expect(screen.queryByRole('button', { name: '保存设置' })).not.toBeInTheDocument();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('keeps edits when changing tabs and restores the saved values on reset', async () => {
    show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '7' } });
    selectTab('请求策略');
    fireEvent.change(screen.getByLabelText('检测超时'), { target: { value: '75' } });
    selectTab('任务调度');
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('7');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '撤销更改' }));
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('4');
    selectTab('请求策略');
    expect(screen.getByLabelText('检测超时')).toHaveValue('60');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled();
  });

  it('submits all settings, including edits and untouched values from inactive tabs', async () => {
    const { fetcher } = show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '7' } });
    selectTab('请求策略');
    fireEvent.change(screen.getByLabelText('检测超时'), { target: { value: '75' } });
    selectTab('数据与历史');
    fireEvent.change(screen.getByLabelText('检测数据保留'), { target: { value: '45' } });
    fireEvent.click(screen.getByRole('radio', { name: /每个模型最近\s*8\s*次/ }));
    selectTab('访客额度');
    fireEvent.change(screen.getByLabelText('访客总并发'), { target: { value: '12' } });
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1));
    const [url, init] = fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')!;
    expect(url).toBe('/api/v1/admin/settings');
    expect(JSON.parse(String(init?.body))).toEqual({ ...initialSettings, probe_concurrency: 7, probe_timeout_seconds: 75, retention_days: 45, artifact_history_scope: 'model', visitor_concurrency: 12 });
    await waitFor(() => expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled());
  });

  it('blocks password changes while a settings save is pending and restores them afterwards', async () => {
    let finishSave!: (response: Response) => void;
    const pendingSave = new Promise<Response>(resolve => { finishSave = resolve; });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return pendingSave;
      return new Response(JSON.stringify(initialSettings), { headers: { 'Content-Type': 'application/json' } });
    });
    show(fetcher);
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '6' } });
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1));
    selectTab('账号安全');
    for (const label of ['当前密码', '新密码', '确认新密码']) expect(screen.getByLabelText(label)).toBeDisabled();
    expect(screen.getByRole('button', { name: '更新密码' })).toBeDisabled();
    expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
    await act(async () => {
      finishSave(new Response(JSON.stringify({ ...initialSettings, probe_concurrency: 6 }), { headers: { 'Content-Type': 'application/json' } }));
    });
    await waitFor(() => expect(screen.getByRole('button', { name: '更新密码' })).toBeEnabled());
    for (const label of ['当前密码', '新密码', '确认新密码']) expect(screen.getByLabelText(label)).toBeEnabled();
  });

  it('retains the draft after a failed save and leaves reset available', async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => new Response(
      JSON.stringify(init?.method === 'PUT' ? { error: { code: 'unavailable', message: '保存暂不可用' } } : initialSettings),
      { status: init?.method === 'PUT' ? 503 : 200, headers: { 'Content-Type': 'application/json' } },
    ));
    show(fetcher);
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '6' } });
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    expect(await screen.findByText('保存暂不可用')).toBeInTheDocument();
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('6');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeEnabled();
  });

  it('reveals the category containing an invalid field before attempting to save', async () => {
    const { fetcher } = show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    selectTab('请求策略');
    fireEvent.change(screen.getByLabelText('检测超时'), { target: { value: '' } });
    expect(screen.getByLabelText('检测超时')).toHaveValue('');
    await waitFor(() => expect(screen.getByLabelText('检测超时')).toHaveAttribute('aria-invalid', 'true'));
    selectTab('访客额度');
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(screen.getByRole('tab', { name: '请求策略' })).toHaveAttribute('aria-selected', 'true'));
    expect(screen.getByLabelText('检测超时')).toHaveAttribute('aria-invalid', 'true');
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('does not overwrite a local draft when cached settings refresh in the background', async () => {
    const { client } = show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '9' } });
    await act(async () => { client.setQueryData(['admin-settings'], { ...initialSettings, probe_concurrency: 5 }); });
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('9');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeEnabled();
  });

  it('merges untouched server updates and resets drafts to the latest server settings', async () => {
    const { client, fetcher } = show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '9' } });
    const latest = { ...initialSettings, probe_concurrency: 5, artifact_concurrency: 3 };
    await act(async () => { client.setQueryData(['admin-settings'], latest); });
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('9');
    expect(screen.getByLabelText('作品生成并发')).toHaveValue('3');
    fireEvent.click(screen.getByRole('button', { name: '撤销更改' }));
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('5');
    expect(screen.getByLabelText('作品生成并发')).toHaveValue('3');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true));
    const written = JSON.parse(String(fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')?.[1]?.body));
    expect(written).toEqual(latest);
  });

  it('keeps account security usable when reading system settings fails', async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => init?.method === 'POST'
      ? new Response(JSON.stringify({ ok: true }))
      : new Response(JSON.stringify({ error: { code: 'settings_read_failed', message: '设置服务暂不可用' } }), { status: 503 }));
    show(fetcher, { cached: false });
    await screen.findByText('设置服务暂不可用');
    selectTab('账号安全');
    expect(screen.getByLabelText('当前密码')).toBeEnabled();
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
  });

  it('does not wait for the settings read before accepting a password change', async () => {
    let finishRead!: (response: Response) => void;
    const pendingRead = new Promise<Response>(resolve => { finishRead = resolve; });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => init?.method === 'POST'
      ? new Response(JSON.stringify({ ok: true })) : pendingRead);
    show(fetcher, { cached: false });
    selectTab('账号安全');
    expect(screen.getByLabelText('当前密码')).toBeEnabled();
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    await act(async () => { finishRead(new Response(JSON.stringify(initialSettings))); });
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET');
    expect(writes).toHaveLength(1);
    expect(writes[0][0]).toBe('/api/v1/auth/password');
  });

  it.each(['logout', 'replacement'] as const)('ignores a late settings save after session %s', async transition => {
    setSession({ user: { id: 'first-admin', username: 'first' }, csrf_token: 'first-csrf' });
    let finishSave!: (response: Response) => void;
    const pendingSave = new Promise<Response>(resolve => { finishSave = resolve; });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => init?.method === 'PUT'
      ? pendingSave : new Response(JSON.stringify(initialSettings)));
    const { client, unmount } = show(fetcher);
    unbinds.push(bindSessionCache(client));
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '6' } });
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true));
    const mutation = client.getMutationCache().getAll()[0];
    unmount();
    act(() => setSession(null));
    expect(client.getQueryData(['admin-settings'])).toBeUndefined();
    const replacement = { ...initialSettings, probe_concurrency: 8 };
    if (transition === 'replacement') {
      setSession({ user: { id: 'second-admin', username: 'second' }, csrf_token: 'second-csrf' });
      client.setQueryData(['admin-settings'], replacement);
    }
    await act(async () => { finishSave(new Response(JSON.stringify({ ...initialSettings, probe_concurrency: 6 }))); });
    await waitFor(() => expect(mutation.state.status).toBe('success'));
    expect(client.getQueryData(['admin-settings'])).toEqual(transition === 'replacement' ? replacement : undefined);
  });

  it('validates password length and confirmation without submitting settings', async () => {
    const { fetcher } = show();
    selectTab('账号安全');
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'short' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'different' } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    await waitFor(() => expect(screen.getByLabelText('新密码')).toHaveAttribute('aria-invalid', 'true'));
    expect(fetcher).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'different-secure-password' } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    await waitFor(() => expect(screen.getByLabelText('确认新密码')).toHaveAttribute('aria-invalid', 'true'));
    expect(fetcher).not.toHaveBeenCalled();
  });

  it.each([
    ['ASCII', 'a'.repeat(257)],
    ['UTF-8', '汉'.repeat(86)],
  ])('rejects a %s password above 256 UTF-8 bytes before submitting', async (_kind, value) => {
    const { fetcher } = show();
    selectTab('账号安全');
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    await waitFor(() => expect(screen.getByLabelText('新密码')).toHaveAttribute('aria-invalid', 'true'));
    expect(fetcher).not.toHaveBeenCalled();
  });

  it.each([
    ['ASCII', 'a'.repeat(256)],
    ['UTF-8', '汉'.repeat(85) + 'a'],
  ])('accepts a %s password at the 256-byte boundary', async (_kind, value) => {
    const { fetcher } = show();
    selectTab('账号安全');
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(writes).toHaveLength(1);
    expect(JSON.parse(String(writes[0][1]?.body)).new_password).toBe(value);
  });

  it('updates the password independently, clears the session, and opens login', async () => {
    setSession({ user: { id: 'admin-1', username: 'admin' }, csrf_token: 'test-csrf' } as Session);
    const { fetcher } = show();
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    fireEvent.change(screen.getByLabelText('健康检测并发'), { target: { value: '6' } });
    selectTab('账号安全');
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'existing-password' } });
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: 'new-secure-password' } });
    fireEvent.click(screen.getByRole('button', { name: '更新密码' }));
    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET');
    expect(writes).toHaveLength(1);
    expect(writes[0][0]).toBe('/api/v1/auth/password');
    expect(writes[0][1]?.method).toBe('POST');
    expect(JSON.parse(String(writes[0][1]?.body))).toEqual({ old_password: 'existing-password', new_password: 'new-secure-password' });
    expect(screen.getByTestId('settings-session')).toHaveTextContent('signed-out');
  });
});
