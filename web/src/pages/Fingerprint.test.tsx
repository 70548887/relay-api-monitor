import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { FingerprintAnalysis, FingerprintChallenge, FingerprintOverview, FingerprintPublicResults } from '../types';
import Fingerprint from './Fingerprint';

const gpt = { id: 'gpt-5.5', display_name: 'gpt-5.5', family: 'gpt', family_name: 'GPT' };
const claude = { id: 'claude-sonnet-4.6', display_name: 'claude-sonnet-4.6', family: 'claude', family_name: 'Claude' };
const readyBank: FingerprintOverview = {
  bank: { ready: true, source: 'online', built_at: '2026-11-19T05:26:09.347356+00:00', models: 75, updated_at: '2026-11-19T06:00:00Z', error: '' },
  models: [claude, gpt],
};
const firstRound: FingerprintChallenge[] = [
  { id: 'probe-1-a', prompt: '请写出 301 个整数', expected_count: 301 },
  { id: 'probe-2-a', prompt: 'Write 312 integers', expected_count: 312 },
  { id: 'probe-3-a', prompt: '整数を 325 個書いてください', expected_count: 325 },
];
const secondRound: FingerprintChallenge[] = firstRound.map((question, index) => ({ ...question, id: `probe-${index + 1}-b`, expected_count: question.expected_count + 1 }));

const candidate = (model: typeof gpt, probability: number | null) => ({ model: model.id, display_name: model.display_name, family: model.family, family_name: model.family_name, score: 0.5, probability });
const fullMatch: FingerprintAnalysis = {
  verdict: 'match', level: 'full', reason: '', valid_answers: 3, expected: gpt, expected_rank: 1, confidence: 0.93,
  candidates: [candidate(gpt, 0.93), candidate(claude, 0.04)],
  answers: [{ numbers: 301, minimum: 166, valid: true }, { numbers: 310, minimum: 172, valid: true }, { numbers: 325, minimum: 179, valid: true }],
  bank_built_at: readyBank.bank.built_at,
};
const partial: FingerprintAnalysis = {
  verdict: 'unlisted', level: 'test', reason: '', valid_answers: 1, expected: null, expected_rank: 0, confidence: null,
  candidates: [candidate(claude, null), candidate(gpt, null)],
  answers: [{ numbers: 300, minimum: 166, valid: true }, { numbers: 40, minimum: 179, valid: false }],
  bank_built_at: readyBank.bank.built_at,
};
const undetermined: FingerprintAnalysis = {
  verdict: 'undetermined', level: '', reason: 'not_model_like', valid_answers: 1, expected: null, expected_rank: 0, confidence: null,
  candidates: [], answers: [{ numbers: 300, minimum: 166, valid: true }], bank_built_at: readyBank.bank.built_at,
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;
let overview: FingerprintOverview;
let results: FingerprintPublicResults;
let rounds: FingerprintChallenge[][];
let analyze: (body: unknown) => Response;
let submitted: unknown[];

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  overview = readyBank;
  results = { models: [] };
  rounds = [firstRound, secondRound];
  analyze = () => json(fullMatch);
  submitted = [];
  vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url), method = init?.method || 'GET';
    if (path === '/api/v1/public/fingerprint' && method === 'GET') return json(overview);
    if (path === '/api/v1/public/fingerprint/results' && method === 'GET') return json(results);
    if (path === '/api/v1/public/fingerprint/challenges?count=3' && method === 'GET') return json({ challenges: rounds.shift() });
    if (path === '/api/v1/public/fingerprint/analyze' && method === 'POST') {
      const body = JSON.parse(String(init?.body));
      submitted.push(body);
      return analyze(body);
    }
    throw new Error(`unexpected request ${method} ${path}`);
  }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function show(path = '/fingerprint?tab=self') {
  render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={[path]}><Fingerprint/></MemoryRouter></App></QueryClientProvider>);
}
const card = (number: number) => within(screen.getByRole('region', { name: `题目 ${number}` }));
const paste = (number: number, text: string) => fireEvent.change(screen.getByLabelText(`题目 ${number} 的回答`), { target: { value: text } });
const compareButton = () => screen.getByRole('button', { name: /开始比对/ });

describe('public fingerprint page', () => {
  it('shows the reference bank and leads from the empty results tab to the self check', async () => {
    show('/fingerprint');
    expect(await screen.findByText('参考库 2026-11-19 版')).toBeInTheDocument();
    expect(screen.getByText('收录 75 个模型 · 来自 lm-detector')).toBeInTheDocument();
    expect(await screen.findByText('还没有平台检测结果')).toBeInTheDocument();
    expect(screen.getByText('结论取每个模型最近 3 轮的多数结果 · 检测由后台发起')).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalledWith('/api/v1/public/fingerprint/challenges?count=3', expect.anything());

    fireEvent.click(screen.getByRole('button', { name: '去自助检测' }));
    expect(await screen.findByText('请写出 301 个整数')).toBeInTheDocument();
    expect(screen.getByText('自助检测的结果只给你自己看，不会出现在平台检测结果里')).toBeInTheDocument();
    expect(screen.queryByText(/检测由后台发起/)).not.toBeInTheDocument();
    expect(card(2).getByText('要求 312 个整数')).toBeInTheDocument();
    expect(card(3).getByText('待粘贴')).toBeInTheDocument();
    expect(compareButton()).toBeDisabled();
  });

  it('lists the conclusions of the platform detections on the results tab', async () => {
    results = { models: [{
      id: 'm1', upstream_id: 'openai/gpt-5.5', display_name: 'GPT 5.5', protocol: 'chat', channel_id: 'c1', channel_name: '默认分组', platform_id: 'p1', platform_name: '示例平台',
      summary: { verdict: 'match', agree: 1, verdicts: ['match'], round_id: 'r1' }, checked_at: '2026-11-20T04:00:00Z',
      round: { id: 'r1', job_id: '', model_id: 'm1', questions: 3, analysis: fullMatch, errors: [], returned_model: 'gpt-5.5', usage: { input_tokens: 120, output_tokens: 2400, total_tokens: 2520 }, duration_ms: 41250, created_at: '2026-11-20T04:00:00Z' },
    }] };
    show('/fingerprint');
    const list = within(await screen.findByRole('region', { name: '平台指纹结果' }));
    expect(list.getByRole('region', { name: '分组 默认分组' })).toBeInTheDocument();
    expect(list.getAllByText('GPT 5.5').length).toBeGreaterThan(0);
    expect(screen.queryByText('还没有平台检测结果')).not.toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalledWith('/api/v1/public/fingerprint/challenges?count=3', expect.anything());
  });

  it('scores only the pasted answers and reports each of them', async () => {
    analyze = () => json(partial);
    show();
    await screen.findByText('请写出 301 个整数');
    paste(1, ' 12 300 45 ');
    paste(3, '7 8 9');
    expect(card(1).getByText('已粘贴')).toBeInTheDocument();
    expect(screen.getByText('已粘贴 2 / 3 个回答')).toBeInTheDocument();
    fireEvent.click(compareButton());

    const result = within(await screen.findByRole('region', { name: '比对结果' }));
    expect(submitted).toEqual([{ answers: [{ text: '12 300 45', expected_count: 301 }, { text: '7 8 9', expected_count: 325 }], expected_model: '' }]);
    expect(card(1).getByText('已识别 300 个 · 有效')).toBeInTheDocument();
    expect(card(2).getByText('未提交')).toBeInTheDocument();
    expect(card(3).getByText('只识别到 40 个 · 至少需要 179 个')).toBeInTheDocument();
    expect(result.getByRole('heading', { name: 'claude-sonnet-4.6' })).toBeInTheDocument();
    expect(result.getByText('测试')).toBeInTheDocument();
    expect(result.getByText('1 / 2')).toBeInTheDocument();
    expect(result.getByText(/没有选择声明的模型/)).toBeInTheDocument();
    expect(result.queryByText(/置信度（/)).not.toBeInTheDocument();
    expect(result.queryByText('未收录')).not.toBeInTheDocument();

    paste(2, '4 5 6');
    expect(screen.queryByRole('region', { name: '比对结果' })).not.toBeInTheDocument();
    expect(card(1).getByText('已粘贴')).toBeInTheDocument();
  });

  it('compares with the claimed model and shows the verdict with its confidence', async () => {
    show();
    await screen.findByText('请写出 301 个整数');
    fireEvent.mouseDown(screen.getByLabelText('声明的模型（可选）'));
    fireEvent.click(await screen.findByTitle('gpt-5.5'));
    [1, 2, 3].forEach(number => paste(number, `answer ${number}`));
    fireEvent.click(compareButton());

    const result = within(await screen.findByRole('region', { name: '比对结果' }));
    expect(submitted).toEqual([{ answers: [{ text: 'answer 1', expected_count: 301 }, { text: 'answer 2', expected_count: 312 }, { text: 'answer 3', expected_count: 325 }], expected_model: 'gpt-5.5' }]);
    expect(result.getByRole('heading', { name: 'gpt-5.5' })).toBeInTheDocument();
    expect(result.getByText('一致')).toBeInTheDocument();
    expect(result.getByText(/与声明的 gpt-5.5 对得上/)).toBeInTheDocument();
    expect(result.getAllByText('93.0%')).toHaveLength(2);
    expect(result.getByText('4.0%')).toBeInTheDocument();
    expect(result.getByText('声明的模型', { selector: 'em' })).toBeInTheDocument();
    expect(result.getByText('2026-11-19')).toBeInTheDocument();
  });

  it('keeps the answers and shows the reason when scoring is refused or undetermined', async () => {
    analyze = () => json({ error: { code: 'rate_limited', message: '请求过于频繁，请一分钟后重试' } }, 429);
    show();
    await screen.findByText('请写出 301 个整数');
    paste(1, '1 2 3');
    fireEvent.click(compareButton());
    expect(await screen.findByText('请求过于频繁，请一分钟后重试')).toBeInTheDocument();
    expect(screen.getByLabelText('题目 1 的回答')).toHaveValue('1 2 3');
    expect(screen.queryByRole('region', { name: '比对结果' })).not.toBeInTheDocument();

    analyze = () => json(undetermined);
    await waitFor(() => expect(compareButton()).toBeEnabled());
    fireEvent.click(compareButton());
    const result = within(await screen.findByRole('region', { name: '比对结果' }));
    expect(result.getByRole('heading', { name: '无法判定' })).toBeInTheDocument();
    expect(result.getByText(/不像是模型直接写出来的/)).toBeInTheDocument();
    expect(screen.queryByText('请求过于频繁，请一分钟后重试')).not.toBeInTheDocument();
  });

  it('does not score while the bank is unavailable and asks before replacing answered questions', async () => {
    overview = { bank: { ready: false, source: 'online', built_at: '', models: 0, updated_at: null, error: '' }, models: [] };
    show();
    await screen.findByText('请写出 301 个整数');
    expect(await screen.findByText('页面会自动重试')).toBeInTheDocument();
    paste(1, '1 2 3');
    expect(compareButton()).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: /换一组题目/ }));
    expect(screen.getByLabelText('题目 1 的回答')).toHaveValue('1 2 3');
    fireEvent.click(await screen.findByRole('button', { name: '确认换题' }));
    expect(await screen.findByText('要求 302 个整数')).toBeInTheDocument();
    expect(screen.getByLabelText('题目 1 的回答')).toHaveValue('');
    expect(submitted).toEqual([]);
  });
});
