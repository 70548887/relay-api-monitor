import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { time } from '../lib';
import type { AdminFingerprintModel, AdminFingerprintOverview, FingerprintAnalysis, FingerprintRound, Job } from '../types';
import { FingerprintAdminPage } from './FingerprintAdmin';

const gpt = { id: 'gpt-5.5', display_name: 'gpt-5.5', family: 'gpt', family_name: 'GPT' };
const claude = { id: 'claude-sonnet-4.6', display_name: 'claude-sonnet-4.6', family: 'claude', family_name: 'Claude' };
const bank = { ready: true, source: 'online', built_at: '2026-11-19T05:26:09.347356+00:00', models: 75, updated_at: '2026-11-20T04:11:00Z', error: '' };

const candidate = (model: typeof gpt, probability: number | null) => ({ model: model.id, display_name: model.display_name, family: model.family, family_name: model.family_name, score: 0.5, probability });
const analysis = (change: Partial<FingerprintAnalysis> = {}): FingerprintAnalysis => ({
  verdict: 'match', level: 'full', reason: '', valid_answers: 3, expected: gpt, expected_rank: 1, confidence: 0.93,
  candidates: [candidate(gpt, 0.93), candidate(claude, 0.04)],
  answers: [{ numbers: 301, minimum: 166, valid: true }, { numbers: 310, minimum: 172, valid: true }, { numbers: 325, minimum: 179, valid: true }],
  bank_built_at: bank.built_at, ...change,
});
const round = (change: Partial<FingerprintRound> = {}): FingerprintRound => ({
  id: 'r1', job_id: 'j1', model_id: 'm1', questions: 3, analysis: analysis(), errors: [], returned_model: 'gpt-5.5',
  usage: { input_tokens: 120, output_tokens: 2400, total_tokens: 2520 }, duration_ms: 41250, created_at: '2026-11-20T04:00:00Z', ...change,
});
const job = (change: Partial<Job> = {}) => ({ id: 'j1', model_id: 'm1', channel_id: 'c1', kind: 'fingerprint', state: 'succeeded', error_code: '', error_message: '', created_at: '2026-11-20T03:59:00Z', ...change }) as Job;
const row = (change: Partial<AdminFingerprintModel> = {}): AdminFingerprintModel => ({
  id: 'm1', upstream_id: 'gpt-5.5', display_name: 'GPT 5.5', channel_id: 'c1', channel_name: '默认分组', platform_id: 'p1', platform_name: '示例平台',
  runnable: true, settings: { mode: 'auto', model: '', questions: 3, scheduled: false }, expected: 'gpt-5.5', latest: null, summary: null, job: null, next_run_at: null, ...change,
});
const weakRound = round({
  id: 'r0', job_id: 'j0', questions: 3, returned_model: '', created_at: '2026-11-19T04:00:00Z',
  analysis: analysis({ verdict: 'mismatch', level: 'test', valid_answers: 1, expected_rank: 2, confidence: null, candidates: [candidate(claude, null), candidate(gpt, null)] }),
  errors: [{ question: 2, code: 'authentication_error', message: 'Upstream returned HTTP 403' }],
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const refused = (status: number, code: string, message: string) => json({ error: { code, message } }, status);
type Sent = { method: string; path: string; body: unknown };
let client: QueryClient;
let overview: AdminFingerprintOverview;
let loads: number;
let sent: Sent[];
let rounds: FingerprintRound[];
let reply: (request: Sent) => Response;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  overview = { bank, interval_seconds: 86400, references: [claude, gpt], models: [row()] };
  loads = 0;
  sent = [];
  rounds = [];
  reply = () => { throw new Error('no reply prepared'); };
  vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url).replace('/api/v1', ''), method = init?.method || 'GET';
    if (path === '/admin/fingerprint' && method === 'GET') { loads++; return json(overview); }
    if (path === '/admin/fingerprint/models/m1/results' && method === 'GET') return json(rounds);
    if (method === 'GET') throw new Error(`unexpected request ${method} ${path}`);
    const request = { method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined };
    sent.push(request);
    return reply(request);
  }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function show() {
  render(<QueryClientProvider client={client}><App><FingerprintAdminPage/></App></QueryClientProvider>);
}
const line = async (name: string) => within((await screen.findByText(name)).closest('tr')!);
const detectButton = (cells: Awaited<ReturnType<typeof line>>) => cells.getByRole('button', { name: /检测/ });
async function choose(label: string, option: string) {
  const select = screen.getByRole('combobox', { name: label });
  await waitFor(() => expect(select).toBeEnabled());
  fireEvent.mouseDown(select);
  fireEvent.click(await screen.findByTitle(option));
}

describe('admin fingerprint page', () => {
  it('lists every model with its expected model and its latest detection', async () => {
    overview.models = [
      row({ latest: round(), summary: { verdict: 'match', agree: 2, verdicts: ['match', 'mismatch', 'match'], round_id: 'r1' }, job: job() }),
      row({ id: 'm2', display_name: 'Claude 运行中', upstream_id: 'claude-x', settings: { mode: 'unlisted', model: '', questions: 2, scheduled: false }, expected: '', job: job({ id: 'j2', model_id: 'm2', state: 'running' }) }),
      row({ id: 'm3', display_name: '失败的模型', upstream_id: 'mystery', expected: '', latest: weakRound, summary: { verdict: 'mismatch', agree: 1, verdicts: ['mismatch'], round_id: 'r0' }, job: job({ id: 'j3', model_id: 'm3', state: 'failed', error_code: 'authentication_error', error_message: '三道题都没有拿到回答' }) }),
      row({ id: 'm4', display_name: '已下线的模型', runnable: false, settings: { mode: 'manual', model: 'retired-model', questions: 3, scheduled: false }, expected: '' }),
    ];
    show();

    expect(await screen.findByText('参考库 2026-11-19 版')).toBeInTheDocument();
    expect(screen.getByText(`收录 75 个模型 · 来自 lm-detector · 上次更新 ${time(bank.updated_at)}`)).toBeInTheDocument();

    const matched = await line('GPT 5.5');
    expect(matched.getByText('示例平台 / 默认分组')).toBeInTheDocument();
    expect(matched.getByTitle('自动匹配')).toBeInTheDocument();
    expect(matched.getByText('匹配到 gpt-5.5')).toBeInTheDocument();
    expect(matched.getByText('一致')).toBeInTheDocument();
    expect(matched.getByText('最接近 gpt-5.5')).toBeInTheDocument();
    expect(matched.getByText('完整 · 93.0%')).toBeInTheDocument();
    expect(matched.getByText(`${time('2026-11-20T04:00:00Z')} · 有效回答 3 / 3`)).toBeInTheDocument();
    expect(matched.getByText('多轮结论：一致（最近 3 轮中 2 轮）')).toBeInTheDocument();
    expect(matched.queryByText('已完成')).not.toBeInTheDocument();
    expect(detectButton(matched)).toBeEnabled();

    const busy = await line('Claude 运行中');
    expect(busy.getByTitle('未收录（只看最接近的模型）')).toBeInTheDocument();
    expect(busy.getByTitle('2 题')).toBeInTheDocument();
    expect(busy.getByText('运行中')).toBeInTheDocument();
    expect(detectButton(busy)).toBeDisabled();
    expect(detectButton(busy)).toHaveAttribute('title', '检测正在进行');

    const failed = await line('失败的模型');
    expect(failed.getByText('参考库里没有对应的模型，只给出最接近的模型')).toBeInTheDocument();
    expect(failed.getByText('失败')).toBeInTheDocument();
    expect(failed.getByText('三道题都没有拿到回答')).toBeInTheDocument();
    expect(failed.getByText('不一致')).toBeInTheDocument();
    expect(failed.getByText('最接近 claude-sonnet-4.6')).toBeInTheDocument();
    expect(failed.getByText('测试')).toBeInTheDocument();
    expect(failed.getByText(`上一轮 · ${time('2026-11-19T04:00:00Z')} · 有效回答 1 / 3`)).toBeInTheDocument();
    expect(failed.getByText('多轮结论：不一致（最近 1 轮中 1 轮）')).toBeInTheDocument();
    expect(busy.queryByText(/多轮结论/)).not.toBeInTheDocument();
    expect(detectButton(failed)).toBeEnabled();

    const retired = await line('已下线的模型');
    expect(retired.getByText('不可检测')).toBeInTheDocument();
    expect(retired.getByTitle('retired-model')).toBeInTheDocument();
    expect(retired.getByText('参考库里已没有这个模型，只给出最接近的模型')).toBeInTheDocument();
    expect(retired.getByText('尚未检测')).toBeInTheDocument();
    expect(detectButton(retired)).toBeDisabled();
    expect(detectButton(retired)).toHaveAttribute('title', '模型在上游已不存在，或所在分组已停用');

    fireEvent.change(screen.getByLabelText('搜索模型'), { target: { value: ' MYSTERY ' } });
    expect(screen.getByText('失败的模型')).toBeInTheDocument();
    expect(screen.queryByText('GPT 5.5')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('搜索模型'), { target: { value: '不存在' } });
    expect(screen.getByText('没有匹配的模型')).toBeInTheDocument();
  });

  it('saves the expected model and the question count as soon as they change, keeping the other settings', async () => {
    overview.models = [row({ settings: { mode: 'auto', model: '', questions: 3, scheduled: true } })];
    reply = request => {
      const settings = request.body as AdminFingerprintModel['settings'];
      overview = { ...overview, models: [row({ settings, expected: settings.mode === 'manual' ? settings.model : '' })] };
      return json(settings);
    };
    show();
    const cells = await line('GPT 5.5');

    await choose('GPT 5.5 的期望模型', 'claude-sonnet-4.6');
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({ method: 'PUT', path: '/admin/fingerprint/models/m1', body: { mode: 'manual', model: 'claude-sonnet-4.6', questions: 3, scheduled: true } });
    expect(await screen.findByText('已保存')).toBeInTheDocument();
    await waitFor(() => expect(cells.queryByText('匹配到 gpt-5.5')).not.toBeInTheDocument());

    await choose('GPT 5.5 的题数', '1 题');
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1].body).toEqual({ mode: 'manual', model: 'claude-sonnet-4.6', questions: 1, scheduled: true });

    await choose('GPT 5.5 的期望模型', '未收录（只看最接近的模型）');
    await waitFor(() => expect(sent).toHaveLength(3));
    expect(sent[2].body).toEqual({ mode: 'unlisted', model: '', questions: 1, scheduled: true });

    reply = () => refused(400, 'invalid_fingerprint_model', '参考库中没有这个模型');
    await choose('GPT 5.5 的期望模型', '自动匹配');
    expect(await screen.findByText('参考库中没有这个模型')).toBeInTheDocument();
    expect(sent[3].body).toEqual({ mode: 'auto', model: '', questions: 1, scheduled: true });
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'GPT 5.5 的期望模型' })).toBeEnabled());
    expect(cells.getByTitle('未收录（只看最接近的模型）')).toBeInTheDocument();
  });

  it('switches scheduled detection per model and shows when the next one is due', async () => {
    overview = { ...overview, interval_seconds: 172800, models: [
      row(),
      row({ id: 'm2', display_name: '定时中的模型', settings: { mode: 'unlisted', model: '', questions: 2, scheduled: true }, expected: '', next_run_at: '2026-11-21T03:12:00Z' }),
      row({ id: 'm3', display_name: '停用分组的模型', runnable: false, settings: { mode: 'auto', model: '', questions: 3, scheduled: true } }),
    ] };
    reply = request => {
      const settings = request.body as AdminFingerprintModel['settings'], id = request.path.split('/').pop();
      overview = { ...overview, models: overview.models.map(item => item.id === id ? { ...item, settings, next_run_at: settings.scheduled ? '2026-11-20T19:40:00Z' : null } : item) };
      return json(settings);
    };
    show();

    const off = await line('GPT 5.5');
    expect(screen.getByText(/打开定时的模型每 2 天自动检测一次/)).toBeInTheDocument();
    expect(off.getByRole('switch', { name: 'GPT 5.5 的定时检测' })).not.toBeChecked();
    expect(off.queryByText(/^下次/)).not.toBeInTheDocument();
    expect(off.queryByText('不可检测，已暂停')).not.toBeInTheDocument();

    const on = await line('定时中的模型');
    expect(on.getByRole('switch', { name: '定时中的模型 的定时检测' })).toBeChecked();
    expect(on.getByText('下次 11/21 11:12')).toHaveAttribute('title', time('2026-11-21T03:12:00Z'));

    const paused = await line('停用分组的模型');
    expect(paused.getByRole('switch')).toBeChecked();
    expect(paused.getByText('不可检测，已暂停')).toBeInTheDocument();

    fireEvent.click(off.getByRole('switch'));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({ method: 'PUT', path: '/admin/fingerprint/models/m1', body: { mode: 'auto', model: '', questions: 3, scheduled: true } });
    expect(await off.findByText('下次 11/21 03:40')).toBeInTheDocument();
    expect(off.getByRole('switch')).toBeChecked();

    await waitFor(() => expect(on.getByRole('switch')).toBeEnabled());
    fireEvent.click(on.getByRole('switch'));
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toEqual({ method: 'PUT', path: '/admin/fingerprint/models/m2', body: { mode: 'unlisted', model: '', questions: 2, scheduled: false } });
    await waitFor(() => expect(on.getByRole('switch')).toBeEnabled());
    expect(on.getByRole('switch')).not.toBeChecked();
    expect(on.queryByText(/^下次/)).not.toBeInTheDocument();

    reply = () => refused(503, 'fingerprint_unavailable', '指纹匹对暂不可用');
    fireEvent.click(on.getByRole('switch'));
    expect(await screen.findByText('指纹匹对暂不可用')).toBeInTheDocument();
    await waitFor(() => expect(on.getByRole('switch')).toBeEnabled());
    expect(on.getByRole('switch')).not.toBeChecked();
  });

  it('queues a detection and shows why a second one is refused', async () => {
    reply = () => {
      overview = { ...overview, models: [row({ job: job({ id: 'j9', state: 'queued' }) })] };
      return json(job({ id: 'j9', state: 'queued' }), 201);
    };
    show();
    const cells = await line('GPT 5.5');
    fireEvent.click(detectButton(cells));

    expect(await screen.findByText('已加入队列，由 Worker 执行')).toBeInTheDocument();
    expect(sent).toEqual([{ method: 'POST', path: '/admin/jobs', body: { model_id: 'm1', kind: 'fingerprint' } }]);
    expect(await cells.findByText('排队中')).toBeInTheDocument();
    expect(detectButton(cells)).toBeDisabled();

    cleanup();
    client.clear();
    overview = { ...overview, models: [row()] };
    reply = () => {
      overview = { ...overview, models: [row({ job: job({ id: 'j10', state: 'running' }) })] };
      return refused(409, 'model_busy', '该模型已有指纹检测任务在排队或执行');
    };
    show();
    const again = await line('GPT 5.5');
    fireEvent.click(detectButton(again));
    expect(await screen.findByText('该模型已有指纹检测任务在排队或执行')).toBeInTheDocument();
    expect(await again.findByText('运行中')).toBeInTheDocument();
  });

  it('follows a running detection until its round arrives', async () => {
    overview.models = [row({ job: job({ state: 'running' }) })];
    show();
    const cells = await line('GPT 5.5');
    expect(cells.getByText('运行中')).toBeInTheDocument();

    overview = { ...overview, models: [row({ latest: round(), job: job() })] };
    expect(await cells.findByText('一致', undefined, { timeout: 8000 })).toBeInTheDocument();
    expect(cells.queryByText('运行中')).not.toBeInTheDocument();
    expect(loads).toBe(2);
    expect(sent).toEqual([]);
  }, 15000);

  it('loads again when a scheduled detection is due and shows it running', async () => {
    const scheduled = { mode: 'auto', model: '', questions: 3, scheduled: true } as const;
    overview.models = [row({ settings: scheduled, next_run_at: new Date(Date.now() + 1500).toISOString() })];
    show();
    const cells = await line('GPT 5.5');
    expect(cells.getByText('尚未检测')).toBeInTheDocument();

    overview = { ...overview, models: [row({ settings: scheduled, next_run_at: '2099-01-01T00:00:00Z', job: job({ id: 'j5', state: 'running', schedule_id: 's1' }) })] };
    expect(await cells.findByText('运行中', undefined, { timeout: 8000 })).toBeInTheDocument();
    expect(loads).toBe(2);
  }, 15000);

  it('updates the reference bank and reports whether a newer build arrived', async () => {
    const newer = { ...bank, built_at: '2026-11-21T05:00:00+00:00', models: 78 };
    reply = () => { overview = { ...overview, bank: newer }; return json({ bank: newer, changed: true }); };
    show();
    await screen.findByText('参考库 2026-11-19 版');
    const update = screen.getByRole('button', { name: /更新参考库/ });
    fireEvent.click(update);

    expect(await screen.findByText('参考库已更新到 2026-11-21 版')).toBeInTheDocument();
    expect(sent).toEqual([{ method: 'POST', path: '/admin/fingerprint/bank/refresh', body: undefined }]);
    expect(await screen.findByText('参考库 2026-11-21 版')).toBeInTheDocument();

    reply = () => json({ bank: newer, changed: false });
    await waitFor(() => expect(update).not.toHaveClass('ant-btn-loading'));
    fireEvent.click(update);
    expect(await screen.findByText('参考库已是最新')).toBeInTheDocument();

    reply = () => refused(502, 'fingerprint_refresh_failed', '参考库更新失败，原有参考库不受影响：timeout');
    await waitFor(() => expect(update).not.toHaveClass('ant-btn-loading'));
    fireEvent.click(update);
    expect(await screen.findByText('参考库更新失败，原有参考库不受影响：timeout')).toBeInTheDocument();
    expect(screen.getByText('参考库 2026-11-21 版')).toBeInTheDocument();
  });

  it('blocks detection while the bank is unavailable and online updates of a local bank', async () => {
    overview = { ...overview, bank: { ready: false, source: 'online', built_at: '', models: 0, updated_at: null, error: 'download bundle.json: connection refused' }, references: [], models: [row({ expected: '' })] };
    show();
    expect(await screen.findByText('参考库暂不可用，暂时不能检测')).toBeInTheDocument();
    expect(screen.getByText('download bundle.json: connection refused')).toBeInTheDocument();
    const cells = await line('GPT 5.5');
    expect(detectButton(cells)).toBeDisabled();
    expect(detectButton(cells)).toHaveAttribute('title', '参考库暂不可用');
    expect(cells.queryByText(/参考库里没有对应的模型/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /更新参考库/ })).toBeEnabled();

    cleanup();
    client.clear();
    overview = { ...overview, bank: { ...bank, source: 'local' } };
    show();
    expect(await screen.findByText(`收录 75 个模型 · 来自本地目录 · 上次更新 ${time(bank.updated_at)}`)).toBeInTheDocument();
    const update = screen.getByRole('button', { name: /更新参考库/ });
    expect(update).toBeDisabled();
    expect(update).toHaveAttribute('title', '参考库来自本地目录，不能在线更新');
    expect(detectButton(await line('GPT 5.5'))).toBeEnabled();
  });

  it('shows the rounds of one model, with the full result and the failed questions of a round', async () => {
    rounds = [round(), weakRound];
    show();
    fireEvent.click((await line('GPT 5.5')).getByRole('button', { name: /^历\s?史$/ }));

    const dialog = within(await screen.findByRole('dialog', { name: '检测历史 · GPT 5.5' }));
    const full = within((await dialog.findByText(time('2026-11-20T04:00:00Z'))).closest('tr')!);
    expect(full.getByText('一致')).toBeInTheDocument();
    expect(full.getByText('3 / 3')).toBeInTheDocument();
    expect(full.getByText('gpt-5.5')).toBeInTheDocument();
    expect(full.getByText('41.25 s')).toBeInTheDocument();
    expect(full.getByText('2520')).toBeInTheDocument();
    const weak = within(dialog.getByText(time('2026-11-19T04:00:00Z')).closest('tr')!);
    expect(weak.getByText('不一致')).toBeInTheDocument();
    expect(weak.getByText('1 / 3')).toBeInTheDocument();
    expect(weak.getByText('—')).toBeInTheDocument();
    expect(dialog.queryByRole('region', { name: '比对结果' })).not.toBeInTheDocument();

    fireEvent.click(weak.getByRole('button'));
    const result = within(await dialog.findByRole('region', { name: '比对结果' }));
    expect(result.getByRole('heading', { name: 'claude-sonnet-4.6' })).toBeInTheDocument();
    expect(result.getByText('1 / 3')).toBeInTheDocument();
    expect(dialog.getByText('第 2 题：Upstream returned HTTP 403（authentication_error）')).toBeInTheDocument();

    cleanup();
    client.clear();
    rounds = [];
    show();
    fireEvent.click((await line('GPT 5.5')).getByRole('button', { name: /^历\s?史$/ }));
    expect(await screen.findByText('还没有检测记录')).toBeInTheDocument();
  });
});
