import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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
  fingerprint_concurrency: 1,
  fingerprint_interval_seconds: 86400,
  fingerprint_manual_per_minute: 10,
  fingerprint_api_per_hour: 6,
  fingerprint_bank_dir: '',
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

const fingerprintLabels = ['指纹检测并发', '定时检测间隔', '手动粘贴比对', '通过 API 检测', '参考库本地目录'];

async function openFingerprint() {
  await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
  selectTab('指纹匹对');
  return screen.getByRole('tabpanel', { name: '指纹匹对' });
}

function written(fetcher: { mock: { calls: [RequestInfo | URL, RequestInit?][] } }) {
  return fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([, init]) => JSON.parse(String(init?.body)));
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
  it('opens with six setting categories and keeps password controls in account security', async () => {
    const { fetcher } = show();
    expect(screen.getByRole('heading', { level: 1, name: '系统设置' })).toBeInTheDocument();
    expect(screen.getAllByRole('tab').map(tab => tab.textContent)).toEqual(['任务调度', '请求策略', '数据与历史', '访客额度', '指纹匹对', '账号安全']);
    await waitFor(() => expect(screen.getByLabelText('健康检测并发')).toHaveValue('4'));
    expect(screen.getByRole('tab', { name: '任务调度' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: '更新密码' })).not.toBeInTheDocument();
    selectTab('账号安全');
    expect(screen.getByLabelText('当前密码')).toBeVisible();
    expect(screen.getByRole('button', { name: '更新密码' })).toBeVisible();
    expect(screen.queryByRole('button', { name: '保存设置' })).not.toBeInTheDocument();
    const security = screen.getByRole('tabpanel', { name: '账号安全' });
    expect(within(security).getByRole('heading', { level: 2, name: '账号安全' })).toBeVisible();
    expect(within(security).getByText('06 / 06')).toBeInTheDocument();
    expect(within(security).getByText('密码更新后，请使用新密码重新登录。')).toBeVisible();
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
    expect(screen.getByText('有 2 项更改尚未保存')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '撤销更改' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '撤销更改' }));
    expect(screen.getByLabelText('健康检测并发')).toHaveValue('4');
    selectTab('请求策略');
    expect(screen.getByLabelText('检测超时')).toHaveValue('60');
    expect(screen.queryByText(/项更改尚未保存/)).not.toBeInTheDocument();
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

  it('lists the fingerprint settings in their own category', async () => {
    const { fetcher } = show();
    expect(screen.getByLabelText('指纹检测并发')).not.toBeVisible();
    const panel = await openFingerprint();
    expect(screen.getByRole('tab', { name: '指纹匹对' })).toHaveAttribute('aria-selected', 'true');
    expect(within(panel).getByRole('heading', { level: 2, name: '指纹匹对' })).toBeVisible();
    expect(within(panel).getByText('05 / 06')).toBeInTheDocument();
    for (const name of ['平台检测', '自助检测', '参考库']) expect(within(panel).getByRole('heading', { level: 3, name })).toBeVisible();
    expect(fingerprintLabels.map(label => (within(panel).getByLabelText(label) as HTMLInputElement).value)).toEqual(['1', '86400', '10', '6', '']);
    for (const unit of ['个', '秒', '次 / 分钟', '轮 / 小时']) expect(within(panel).getByText(unit)).toBeVisible();
    expect(within(panel).getByText('每 24 小时检测一次')).toBeVisible();
    expect(within(panel).getByText('每个 IP 每分钟比对的上限')).toBeVisible();
    expect(within(panel).getByText('每个会员每小时检测的上限')).toBeVisible();
    expect(within(panel).getByPlaceholderText('留空使用在线参考库')).toBe(within(panel).getByLabelText('参考库本地目录'));
    expect(within(panel).getByText(/unified_bank\.json 和 shared_detector\.bin/)).toBeVisible();
    expect(within(panel).getByText(/定时检测默认关闭.*从保存时起按新间隔重新计时/)).toBeVisible();
    expect(screen.getByRole('button', { name: '保存设置' })).toBeVisible();
    expect(screen.queryByLabelText('当前密码')).not.toBeVisible();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('saves the fingerprint settings and refreshes the pages that show them', async () => {
    const { client, fetcher } = show();
    client.setQueryData(['admin-fingerprint'], { interval_seconds: 86400 });
    client.setQueryData(['fingerprint-overview'], { models: [] });
    await openFingerprint();
    fireEvent.change(screen.getByLabelText('指纹检测并发'), { target: { value: '3' } });
    fireEvent.change(screen.getByLabelText('定时检测间隔'), { target: { value: '21600' } });
    expect(screen.getByText('每 6 小时检测一次')).toBeVisible();
    fireEvent.change(screen.getByLabelText('定时检测间隔'), { target: { value: '5400' } });
    expect(screen.getByText('每 90 分钟检测一次')).toBeVisible();
    fireEvent.change(screen.getByLabelText('手动粘贴比对'), { target: { value: '20' } });
    fireEvent.change(screen.getByLabelText('通过 API 检测'), { target: { value: '12' } });
    fireEvent.change(screen.getByLabelText('参考库本地目录'), { target: { value: 'D:\\fingerprint-bank' } });
    expect(screen.getByText('有 5 项更改尚未保存')).toBeInTheDocument();
    expect(client.getQueryState(['admin-fingerprint'])?.isInvalidated).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(written(fetcher)).toHaveLength(1));
    expect(fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')?.[0]).toBe('/api/v1/admin/settings');
    expect(written(fetcher)[0]).toEqual({ ...initialSettings, fingerprint_concurrency: 3, fingerprint_interval_seconds: 5400, fingerprint_manual_per_minute: 20, fingerprint_api_per_hour: 12, fingerprint_bank_dir: 'D:\\fingerprint-bank' });
    await waitFor(() => expect(screen.getByRole('button', { name: '撤销更改' })).toBeDisabled());
    expect(client.getQueryState(['admin-fingerprint'])?.isInvalidated).toBe(true);
    expect(client.getQueryState(['fingerprint-overview'])?.isInvalidated).toBe(true);
    expect(screen.getByRole('tab', { name: '指纹匹对' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByLabelText('参考库本地目录')).toHaveValue('D:\\fingerprint-bank');
  });

  it('shows the bank directory the way the server stored it', async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => new Response(
      JSON.stringify(init?.method === 'PUT' ? { ...JSON.parse(String(init.body)), fingerprint_bank_dir: '/data/bank' } : { ...initialSettings, fingerprint_bank_dir: '/old/bank' }),
      { headers: { 'Content-Type': 'application/json' } },
    ));
    const { client } = show(fetcher, { cached: false });
    await waitFor(() => expect(screen.getByLabelText('参考库本地目录')).toHaveValue('/old/bank'));
    selectTab('指纹匹对');
    fireEvent.change(screen.getByLabelText('参考库本地目录'), { target: { value: '  /data/bank  ' } });
    expect(screen.getByText('有 1 项更改尚未保存')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(screen.getByLabelText('参考库本地目录')).toHaveValue('/data/bank'));
    expect(screen.getByText('暂无未保存的更改')).toBeInTheDocument();
    expect(client.getQueryData<Settings>(['admin-settings'])?.fingerprint_bank_dir).toBe('/data/bank');
    fireEvent.change(screen.getByLabelText('参考库本地目录'), { target: { value: '' } });
    expect(screen.getByText('有 1 项更改尚未保存')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(written(fetcher)).toHaveLength(2));
    expect(written(fetcher)[1].fingerprint_bank_dir).toBe('');
  });

  it('keeps fingerprint numbers inside their limits', async () => {
    show();
    await openFingerprint();
    for (const [label, low, min, high, max, fraction, whole] of [
      ['指纹检测并发', '0', '1', '9', '8', '2.6', '3'],
      ['定时检测间隔', '3599', '3600', '2592001', '2592000', '7200.6', '7201'],
      ['手动粘贴比对', '0', '1', '121', '120', '2.6', '3'],
      ['通过 API 检测', '0', '1', '61', '60', '2.6', '3'],
    ]) {
      const input = screen.getByLabelText(label);
      fireEvent.change(input, { target: { value: low } });
      fireEvent.blur(input);
      expect(input, `${label} lower limit`).toHaveValue(min);
      fireEvent.change(input, { target: { value: high } });
      fireEvent.blur(input);
      expect(input, `${label} upper limit`).toHaveValue(max);
      fireEvent.change(input, { target: { value: fraction } });
      fireEvent.blur(input);
      expect(input, `${label} whole numbers`).toHaveValue(whole);
    }
  });

  it('returns to the fingerprint category when one of its fields cannot be saved', async () => {
    const { fetcher } = show();
    await openFingerprint();
    fireEvent.change(screen.getByLabelText('定时检测间隔'), { target: { value: '' } });
    await waitFor(() => expect(screen.getByLabelText('定时检测间隔')).toHaveAttribute('aria-invalid', 'true'));
    expect(screen.getByText('请输入有效的执行间隔')).toBeVisible();
    selectTab('任务调度');
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(screen.getByRole('tab', { name: '指纹匹对' })).toHaveAttribute('aria-selected', 'true'));
    expect(await screen.findByText('请输入定时检测间隔')).toBeVisible();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('returns to the fingerprint category when the bank directory is refused', async () => {
    const { fetcher } = show();
    await openFingerprint();
    fireEvent.change(screen.getByLabelText('参考库本地目录'), { target: { value: 'bank' } });
    await waitFor(() => expect(screen.getByLabelText('参考库本地目录')).toHaveAttribute('aria-invalid', 'true'));
    selectTab('访客额度');
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(screen.getByRole('tab', { name: '指纹匹对' })).toHaveAttribute('aria-selected', 'true'));
    expect(screen.getByLabelText('参考库本地目录')).toHaveAttribute('aria-invalid', 'true');
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('accepts only an absolute bank directory', async () => {
    const { fetcher } = show();
    await openFingerprint();
    const input = screen.getByLabelText('参考库本地目录');
    // Validation clears the error mark as soon as it starts, so an accepted value is one the form has marked as passed.
    const passes = (value: string) => waitFor(() => expect(input.closest('.ant-form-item'), `accepts ${value}`).toHaveClass('ant-form-item-has-success'));
    for (const [refused, accepted] of [
      ['bank', '/data/bank'], ['./bank', 'D:\\bank'], ['D:bank', 'd:/bank'], ['~/bank', '\\\\nas\\share\\bank'], ['..\\bank', '  /data/bank  '], ['\\bank', '   '],
    ]) {
      fireEvent.change(input, { target: { value: refused } });
      await waitFor(() => expect(input, `refuses ${refused}`).toHaveAttribute('aria-invalid', 'true'));
      expect(await screen.findByText('请填写绝对路径，例如 /data/fingerprint-bank 或 D:\\fingerprint-bank')).toBeInTheDocument();
      fireEvent.change(input, { target: { value: accepted } });
      await passes(accepted);
    }
    fireEvent.change(input, { target: { value: `/${'库'.repeat(342)}` } });
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    expect(await screen.findByText('路径最多为 1024 字节')).toBeInTheDocument();
    fireEvent.change(input, { target: { value: `/${'库'.repeat(341)}` } });
    await passes('a path of exactly 1024 bytes');
    fireEvent.change(input, { target: { value: 'bank' } });
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    fireEvent.click(screen.getByRole('button', { name: '撤销更改' }));
    // Resetting the form mounts a fresh field, so look it up again.
    expect(screen.getByLabelText('参考库本地目录')).toHaveValue('');
    expect(screen.getByLabelText('参考库本地目录')).not.toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('暂无未保存的更改')).toBeInTheDocument();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('locks the fingerprint fields while a save is in flight', async () => {
    let finishSave!: (response: Response) => void;
    const pendingSave = new Promise<Response>(resolve => { finishSave = resolve; });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => init?.method === 'PUT' ? pendingSave
      : new Response(JSON.stringify(initialSettings), { headers: { 'Content-Type': 'application/json' } }));
    show(fetcher);
    await openFingerprint();
    for (const label of fingerprintLabels) expect(screen.getByLabelText(label)).toBeEnabled();
    fireEvent.change(screen.getByLabelText('指纹检测并发'), { target: { value: '2' } });
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }));
    await waitFor(() => expect(written(fetcher)).toHaveLength(1));
    for (const label of fingerprintLabels) expect(screen.getByLabelText(label), label).toBeDisabled();
    await act(async () => {
      finishSave(new Response(JSON.stringify({ ...initialSettings, fingerprint_concurrency: 2 }), { headers: { 'Content-Type': 'application/json' } }));
    });
    await waitFor(() => { for (const label of fingerprintLabels) expect(screen.getByLabelText(label), label).toBeEnabled(); });
    expect(screen.getByLabelText('指纹检测并发')).toHaveValue('2');
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
