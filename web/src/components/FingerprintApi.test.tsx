import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { time } from '../lib';
import type { Connection } from '../privateTests';
import type { FingerprintAnalysis, FingerprintCheck } from '../types';
import { setUserSession } from '../userApi';
import { FingerprintApi } from './FingerprintApi';

const gpt = { id: 'gpt-5.5', display_name: 'gpt-5.5', family: 'gpt', family_name: 'GPT' };
const claude = { id: 'claude-sonnet-4.6', display_name: 'claude-sonnet-4.6', family: 'claude', family_name: 'Claude' };
const candidate = (model: typeof gpt, probability: number | null) => ({ model: model.id, display_name: model.display_name, family: model.family, family_name: model.family_name, score: 0.5, probability });

const matched: FingerprintAnalysis = {
  verdict: 'match', level: 'full', reason: '', valid_answers: 3, expected: claude, expected_rank: 1, confidence: 0.93,
  candidates: [candidate(claude, 0.93), candidate(gpt, 0.04)],
  answers: [{ numbers: 306, minimum: 168, valid: true }, { numbers: 310, minimum: 175, valid: true }, { numbers: 325, minimum: 179, valid: true }],
  bank_built_at: '2026-11-19T05:26:09.347356+00:00',
};
const closestOnly: FingerprintAnalysis = { ...matched, verdict: 'unlisted', expected: null, expected_rank: 0 };
const finished = (change: Partial<FingerprintCheck> = {}): FingerprintCheck => ({
  questions: 3, analysis: matched, errors: [], question_ms: [4600, 5210, 3980], expected_counts: [306, 318, 325],
  returned_model: 'claude-sonnet-4-6-20260101', usage: { input_tokens: 9, output_tokens: 15, total_tokens: 24 }, duration_ms: 5300, created_at: '2026-11-20T08:00:00Z', ...change,
});

const connection = (id: string, name: string, change: Partial<Connection> = {}): Connection => ({
  id, user_id: 'm1', name, base_url: `https://${id}.example.com/v1`, protocol: 'openai-chat', discovery_protocol: 'openai', models_path: '', generate_path: '',
  anthropic_version: '', chat_token_field: 'max_tokens', key_mask: 'sk-****abcd', revision: 3, disabled: false, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
  last_models_at: null, last_models_count: 0, last_models_error: '', ...change,
});
const member = { user: { id: 'm1', username: 'tester' }, csrf_token: 'csrf-1' };

type Sent = { body: Record<string, unknown>; csrf: string | null; signal?: AbortSignal | null };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const refused = (status: number, code: string, message: string) => json({ error: { code, message } }, status);

let client: QueryClient;
let signedIn: boolean;
let saved: Connection[];
let connectionReads: number;
let checks: Sent[];
let answer: () => Response | Promise<Response>;
let listings: Sent[];
let listing: () => Response;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  signedIn = true;
  saved = [connection('relay-a', '中转站 A'), connection('relay-b', '中转站 B', { revision: 7 }), connection('relay-c', '旧站', { disabled: true })];
  connectionReads = 0;
  checks = [];
  answer = () => json(finished());
  listings = [];
  listing = () => json({ models: ['model-a', 'model-b'], duration_ms: 12 });
  vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url), method = init?.method || 'GET';
    const sent = (): Sent => ({ body: JSON.parse(String(init?.body)), csrf: new Headers(init?.headers).get('X-CSRF-Token'), signal: init?.signal });
    if (path === '/api/v1/user/auth/me' && method === 'GET') return signedIn ? json(member) : refused(401, 'unauthorized', '请先登录');
    if (path === '/api/v1/user/connections' && method === 'GET') {
      connectionReads++;
      return json(saved);
    }
    if (path === '/api/v1/test/models' && method === 'POST') {
      listings.push(sent());
      return listing();
    }
    if (path === '/api/v1/test/fingerprint' && method === 'POST') {
      checks.push(sent());
      return new Promise<Response>((resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError')));
        void Promise.resolve(answer()).then(resolve);
      });
    }
    throw new Error(`unexpected request ${method} ${path}`);
  }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); setUserSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function Where() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

function show(ready = true) {
  if (signedIn) setUserSession(member);
  render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={['/fingerprint?tab=self&mode=api']}>
    <FingerprintApi models={[claude, gpt]} ready={ready} head={<h2>检测设置</h2>} hidden={false}/>
    <Where/>
  </MemoryRouter></App></QueryClientProvider>);
}

const startButton = () => screen.getByRole('button', { name: /开始检测|再检测一轮/ });
const typeModel = (value: string) => fireEvent.change(screen.getByLabelText('模型'), { target: { value } });
const sample = (number: number) => within(screen.getByRole('region', { name: `样本 ${number}` }));
const result = async () => within(await screen.findByRole('region', { name: '比对结果' }));
async function pick(label: string, option: string) {
  fireEvent.mouseDown(screen.getByLabelText(label));
  fireEvent.click(await screen.findByTitle(option));
}
/** Opens the form of a signed-in member and fills in the model to check. */
async function openForm(model = 'claude-sonnet-4-6') {
  show();
  await screen.findByLabelText('我的连接');
  typeModel(model);
}

describe('fingerprint check through a saved connection', () => {
  it('asks a visitor to sign in and comes back to this page afterwards', async () => {
    signedIn = false;
    show();
    expect(await screen.findByText('登录后才能通过 API 检测')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '检测设置' })).toBeInTheDocument();
    expect(screen.queryByLabelText('我的连接')).not.toBeInTheDocument();
    expect(connectionReads).toBe(0);

    fireEvent.click(screen.getByRole('button', { name: /注\s*册/ }));
    expect(screen.getByTestId('location').textContent).toBe('/register?next=%2Ffingerprint%3Ftab%3Dself%26mode%3Dapi');
    fireEvent.click(screen.getByRole('button', { name: /登\s*录/ }));
    expect(screen.getByTestId('location').textContent).toBe('/login?next=%2Ffingerprint%3Ftab%3Dself%26mode%3Dapi');
  });

  it('sends a member without a usable connection to the saved connections', async () => {
    saved = [];
    show();
    expect(await screen.findByText(/还没有保存的连接。先在「保存的连接」里添加一个连接/)).toBeInTheDocument();
    expect(screen.queryByLabelText('模型')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /开始检测/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '管理保存的连接' }));
    expect(screen.getByTestId('location').textContent).toBe('/account/connections');

    cleanup();
    client.clear();
    saved = [connection('relay-c', '旧站', { disabled: true })];
    show();
    expect(await screen.findByText(/保存的连接都已停用。先在「保存的连接」里启用一个连接/)).toBeInTheDocument();
    expect(screen.queryByLabelText('模型')).not.toBeInTheDocument();
  });

  it('runs a round through the chosen connection and shows what each question came to', async () => {
    let finish: (response: Response) => void = () => {};
    answer = () => new Promise<Response>(resolve => { finish = resolve; });
    show();
    await screen.findByLabelText('我的连接');
    expect(screen.getByText('还没有检测结果')).toBeInTheDocument();
    expect(startButton()).toBeDisabled();
    fireEvent.mouseDown(screen.getByLabelText('我的连接'));
    expect((await screen.findByTitle('旧站 · relay-c.example.com（已停用）')).closest('.ant-select-item-option')).toHaveClass('ant-select-item-option-disabled');
    fireEvent.click(screen.getByTitle('中转站 B · relay-b.example.com'));
    typeModel('  claude-sonnet-4-6 ');
    expect(startButton()).toHaveTextContent('开始检测');
    fireEvent.click(startButton());

    const waiting = within(await screen.findByRole('status', { name: '正在检测' }));
    expect(waiting.getAllByText('等待回答')).toHaveLength(3);
    expect(screen.getByRole('button', { name: /检测中/ })).toBeInTheDocument();
    expect(waiting.getByText(/已同时发出 3 道题/)).toBeInTheDocument();
    expect(screen.queryByText('还没有检测结果')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /取\s*消/ })).toBeInTheDocument();
    expect(screen.getByLabelText('模型')).toBeDisabled();
    expect(checks).toEqual([{
      body: { connection_id: 'relay-b', connection_revision: 7, model: 'claude-sonnet-4-6', questions: 3, mode: 'auto', expected_model: '' },
      csrf: 'csrf-1', signal: expect.anything(),
    }]);

    finish(json(finished()));
    const outcome = await result();
    expect(screen.queryByRole('status', { name: '正在检测' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /取\s*消/ })).not.toBeInTheDocument();
    expect(sample(1).getByText('要求 306 个 · 识别 306 个')).toBeInTheDocument();
    expect(sample(1).getByText('用时 4.6 秒')).toBeInTheDocument();
    expect(sample(1).getByText('有效')).toBeInTheDocument();
    expect(sample(2).getByText('要求 318 个 · 识别 310 个')).toBeInTheDocument();
    expect(sample(2).getByText('用时 5.2 秒')).toBeInTheDocument();
    expect(sample(3).getByText('要求 325 个 · 识别 325 个')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '样本 4' })).not.toBeInTheDocument();

    expect(outcome.getByRole('heading', { name: 'claude-sonnet-4.6' })).toBeInTheDocument();
    expect(outcome.getByText('一致')).toBeInTheDocument();
    expect(outcome.getAllByText('93.0%')).toHaveLength(2);
    expect(outcome.getByText('3 / 3')).toBeInTheDocument();
    const facts: Record<string, string> = {
      匹配方式: '按模型名自动匹配', 检测的模型: 'claude-sonnet-4-6', 接口返回的模型名: 'claude-sonnet-4-6-20260101', 本轮用时: '5.3 秒',
      'Token 用量': '输入 9 · 输出 15 · 合计 24', 检测时间: time('2026-11-20T08:00:00Z'),
    };
    for (const [name, value] of Object.entries(facts)) expect(outcome.getByText(name).nextElementSibling).toHaveTextContent(value);
    expect(outcome.getByText(/结果只显示在这个页面，不保存，也不会公开/)).toBeInTheDocument();
    expect(startButton()).toHaveTextContent('再检测一轮');
    expect(startButton()).toBeEnabled();
  });

  it('sends the declared model and the number of questions that were chosen', async () => {
    answer = () => json(finished({ questions: 2, question_ms: [4600, 5210], expected_counts: [306, 318], analysis: { ...matched, expected: gpt, verdict: 'mismatch', expected_rank: 2, level: 'reference', confidence: null, valid_answers: 2, answers: matched.answers.slice(0, 2) } }));
    await openForm();
    expect(screen.getByText(/同时发 3 次请求/)).toBeInTheDocument();
    await pick('声明的模型', 'gpt-5.5');
    fireEvent.click(screen.getByRole('radio', { name: '2 题' }));
    expect(screen.getByText(/同时发 2 次请求/)).toBeInTheDocument();
    fireEvent.click(startButton());
    let outcome = await result();
    expect(checks[0].body).toEqual({ connection_id: 'relay-a', connection_revision: 3, model: 'claude-sonnet-4-6', questions: 2, mode: 'manual', expected_model: 'gpt-5.5' });
    expect(outcome.getByText('匹配方式').nextElementSibling).toHaveTextContent('手动指定');
    expect(outcome.getByText('不一致')).toBeInTheDocument();
    expect(outcome.getByText('2 / 2')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '样本 3' })).not.toBeInTheDocument();

    answer = () => json(finished({ analysis: closestOnly }));
    await pick('声明的模型', '不指定，只看最接近的模型');
    fireEvent.click(screen.getByRole('radio', { name: '1 题' }));
    fireEvent.click(startButton());
    await waitFor(() => expect(checks).toHaveLength(2));
    expect(checks[1].body).toEqual({ connection_id: 'relay-a', connection_revision: 3, model: 'claude-sonnet-4-6', questions: 1, mode: 'unlisted', expected_model: '' });
    outcome = within(await screen.findByRole('region', { name: '比对结果' }));
    await waitFor(() => expect(outcome.getByText('没有指定声明的模型，只给出最接近的模型 · Claude 家族')).toBeInTheDocument());
    expect(outcome.getByText('声明的模型').nextElementSibling).toHaveTextContent('未指定');
    expect(outcome.queryByText('匹配方式')).not.toBeInTheDocument();

    await pick('声明的模型', '按模型名自动匹配');
    typeModel('my-private-model');
    fireEvent.click(startButton());
    await waitFor(() => expect(checks).toHaveLength(3));
    expect(checks[2].body).toEqual({ connection_id: 'relay-a', connection_revision: 3, model: 'my-private-model', questions: 1, mode: 'auto', expected_model: '' });
    await waitFor(() => expect(screen.getByText('参考库里没有和「my-private-model」对应的模型，只给出最接近的模型 · Claude 家族')).toBeInTheDocument());
    expect(screen.getByText('声明的模型', { selector: 'dt' }).nextElementSibling).toHaveTextContent('未匹配到');
    expect(screen.getByText('检测的模型').nextElementSibling).toHaveTextContent('my-private-model');
  });

  it('shows the questions that failed or did not count beside the one that did', async () => {
    answer = () => json(finished({
      errors: [{ question: 2, code: 'rate_limited', message: 'quota used up' }], question_ms: [4600, 310, 3980], returned_model: '', usage: { input_tokens: null, output_tokens: null, total_tokens: null },
      analysis: { ...closestOnly, level: 'test', confidence: null, valid_answers: 1, candidates: [candidate(claude, null)], answers: [matched.answers[0], { numbers: 0, minimum: 175, valid: false }, { numbers: 40, minimum: 179, valid: false }] },
    }));
    await openForm();
    fireEvent.click(startButton());
    const outcome = await result();
    expect(sample(1).getByText('有效')).toBeInTheDocument();
    expect(sample(2).getByText('请求失败')).toBeInTheDocument();
    expect(sample(2).getByText('quota used up · rate_limited')).toBeInTheDocument();
    expect(sample(2).getByText('要求 318 个 · 识别 0 个')).toBeInTheDocument();
    expect(sample(2).queryByText(/至少需要/)).not.toBeInTheDocument();
    expect(sample(3).getByText('未采用')).toBeInTheDocument();
    expect(sample(3).getByText('要求 325 个 · 识别 40 个')).toBeInTheDocument();
    expect(sample(3).getByText('至少需要 179 个整数')).toBeInTheDocument();
    expect(sample(1).queryByText(/至少需要|rate_limited/)).not.toBeInTheDocument();
    expect(outcome.getByText('1 / 3')).toBeInTheDocument();
    expect(outcome.getByText('接口返回的模型名').nextElementSibling).toHaveTextContent('未返回');
    expect(outcome.getByText('Token 用量').nextElementSibling).toHaveTextContent('接口未返回');
    expect(screen.queryByText(/都没有成功/)).not.toBeInTheDocument();
  });

  it('says that nothing could be compared when no question was answered, and keeps the facts of an undetermined round', async () => {
    const nothing: FingerprintAnalysis = { ...closestOnly, verdict: 'undetermined', level: '', reason: 'no_valid_answers', confidence: null, valid_answers: 0, candidates: [], answers: [1, 2, 3].map(() => ({ numbers: 0, minimum: 170, valid: false })) };
    answer = () => json(finished({ analysis: nothing, errors: [1, 2, 3].map(question => ({ question, code: 'blocked_target', message: `第 ${question} 次被拦截` })), returned_model: '', question_ms: [0, 0, 0] }));
    await openForm();
    fireEvent.click(startButton());
    expect(await screen.findByText('本轮 3 道题的请求都没有成功，无法比对')).toBeInTheDocument();
    expect(screen.getByText(/这一轮已计入每小时的检测次数/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '比对结果' })).not.toBeInTheDocument();
    [1, 2, 3].forEach(number => expect(sample(number).getByText(`第 ${number} 次被拦截 · blocked_target`)).toBeInTheDocument());
    expect(startButton()).toHaveTextContent('再检测一轮');

    answer = () => json(finished({ analysis: { ...nothing, reason: 'not_model_like', valid_answers: 3, answers: matched.answers } }));
    fireEvent.click(startButton());
    const outcome = await result();
    expect(outcome.getByRole('heading', { name: '无法判定' })).toBeInTheDocument();
    expect(outcome.getByText(/不像是模型直接写出来的/)).toBeInTheDocument();
    expect(outcome.getByText('接口返回的模型名').nextElementSibling).toHaveTextContent('claude-sonnet-4-6-20260101');
    expect(outcome.getByText('Token 用量').nextElementSibling).toHaveTextContent('输入 9 · 输出 15 · 合计 24');
    expect(screen.queryByText(/都没有成功/)).not.toBeInTheDocument();
  });

  it('shows why the server refused a round and retries a changed connection with its new revision', async () => {
    answer = () => refused(429, 'rate_limited', '本小时的检测次数已用完，请 59 分钟后再试');
    await openForm();
    fireEvent.click(startButton());
    expect(await screen.findByText('本小时的检测次数已用完，请 59 分钟后再试')).toBeInTheDocument();
    expect(screen.getByText('检测没有完成')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '样本 1' })).not.toBeInTheDocument();
    expect(screen.queryByText('还没有检测结果')).not.toBeInTheDocument();
    expect(startButton()).toHaveTextContent('开始检测');
    expect(connectionReads).toBe(1);

    answer = () => refused(409, 'connection_changed', '连接配置已变化，请重新确认后测试');
    saved = [connection('relay-a', '中转站 A', { revision: 4 })];
    fireEvent.click(startButton());
    expect(await screen.findByText('连接配置已变化，请重新确认后测试')).toBeInTheDocument();
    expect(screen.queryByText('本小时的检测次数已用完，请 59 分钟后再试')).not.toBeInTheDocument();
    await waitFor(() => expect(connectionReads).toBe(2));

    answer = () => json(finished());
    await waitFor(() => expect(startButton()).toBeEnabled());
    fireEvent.click(startButton());
    await result();
    expect(checks.map(sent => sent.body.connection_revision)).toEqual([3, 3, 4]);
    expect(screen.queryByText('检测没有完成')).not.toBeInTheDocument();
  });

  it('stops waiting when the round is cancelled', async () => {
    answer = () => new Promise<Response>(() => {});
    await openForm();
    fireEvent.click(screen.getByRole('radio', { name: '1 题' }));
    fireEvent.click(startButton());
    const waiting = within(await screen.findByRole('status', { name: '正在检测' }));
    expect(waiting.getAllByText('等待回答')).toHaveLength(1);
    expect(waiting.getByText(/已同时发出 1 道题/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /取\s*消/ }));
    expect(await screen.findByText('已取消本轮检测')).toBeInTheDocument();
    expect(screen.getByText(/这一轮也计入每小时的检测次数/)).toBeInTheDocument();
    expect(checks[0].signal?.aborted).toBe(true);
    expect(screen.queryByText('检测没有完成')).not.toBeInTheDocument();
    expect(screen.queryByRole('status', { name: '正在检测' })).not.toBeInTheDocument();
    expect(startButton()).toHaveTextContent('开始检测');
    expect(startButton()).toBeEnabled();

    answer = () => json(finished());
    fireEvent.click(startButton());
    await result();
    expect(screen.queryByText('已取消本轮检测')).not.toBeInTheDocument();
    expect(checks[1].signal?.aborted).toBe(false);
  });

  it('lists the models of the connection only when asked to', async () => {
    show();
    await screen.findByLabelText('我的连接');
    expect(screen.getByText('填写发给连接的模型 ID，或点「获取模型」从连接读取列表。')).toBeInTheDocument();
    expect(listings).toEqual([]);

    fireEvent.click(screen.getByRole('button', { name: '获取模型' }));
    expect(await screen.findByText('已获取 2 个模型，可以在输入框里选择或搜索。')).toBeInTheDocument();
    expect(listings).toEqual([{ body: { connection_id: 'relay-a', connection_revision: 3 }, csrf: 'csrf-1', signal: undefined }]);
    await pick('模型', 'model-b');
    expect(screen.getByLabelText('模型')).toHaveValue('model-b');
    fireEvent.click(startButton());
    await result();
    expect(checks[0].body.model).toBe('model-b');

    await pick('我的连接', '中转站 B · relay-b.example.com');
    expect(screen.getByText('填写发给连接的模型 ID，或点「获取模型」从连接读取列表。')).toBeInTheDocument();
    listing = () => refused(502, 'upstream_error', '上游返回 502');
    fireEvent.click(screen.getByRole('button', { name: '获取模型' }));
    expect(await screen.findByText('模型列表获取失败：上游返回 502。可以直接填写模型 ID。')).toBeInTheDocument();
    expect(listings[1].body).toEqual({ connection_id: 'relay-b', connection_revision: 7 });

    await pick('我的连接', '中转站 A · relay-a.example.com');
    expect(screen.getByText('已获取 2 个模型，可以在输入框里选择或搜索。')).toBeInTheDocument();
    expect(screen.queryByText(/模型列表获取失败/)).not.toBeInTheDocument();

    await pick('我的连接', '中转站 B · relay-b.example.com');
    listing = () => json({ models: [], duration_ms: 3 });
    fireEvent.click(screen.getByRole('button', { name: '获取模型' }));
    expect(await screen.findByText('连接没有返回模型，请直接填写模型 ID。')).toBeInTheDocument();
  });

  it('does not start while the reference bank is unavailable, and asks to sign in again once the session ended', async () => {
    show(false);
    await screen.findByLabelText('我的连接');
    typeModel('claude-sonnet-4-6');
    expect(screen.getByText('参考库就绪后才能检测。页面会自动重试。')).toBeInTheDocument();
    expect(startButton()).toBeDisabled();

    cleanup();
    client.clear();
    answer = () => refused(401, 'unauthorized', '请先登录');
    await openForm();
    expect(screen.queryByText('参考库就绪后才能检测。页面会自动重试。')).not.toBeInTheDocument();
    fireEvent.click(startButton());
    expect(await screen.findByText('登录后才能通过 API 检测')).toBeInTheDocument();
    expect(screen.queryByLabelText('我的连接')).not.toBeInTheDocument();
  });
});
