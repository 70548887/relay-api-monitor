import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { shortTime, time } from '../lib';
import type { FingerprintAnalysis, FingerprintPublicModel, FingerprintPublicResults, FingerprintRound } from '../types';
import { FingerprintResults } from './FingerprintResults';

const gpt = { id: 'gpt-5.5', display_name: 'gpt-5.5', family: 'gpt', family_name: 'GPT' };
const sonnet = { id: 'claude-sonnet-4.6', display_name: 'claude-sonnet-4.6', family: 'claude', family_name: 'Claude' };
const opus = { id: 'claude-opus-4.5', display_name: 'claude-opus-4.5', family: 'claude', family_name: 'Claude' };
const builtAt = '2026-11-19T05:26:09.347356+00:00';

const candidate = (model: typeof gpt, probability: number | null) => ({ model: model.id, display_name: model.display_name, family: model.family, family_name: model.family_name, score: 0.5, probability });
const analysis = (change: Partial<FingerprintAnalysis> = {}): FingerprintAnalysis => ({
  verdict: 'match', level: 'full', reason: '', valid_answers: 3, expected: gpt, expected_rank: 1, confidence: 0.93,
  candidates: [candidate(gpt, 0.93), candidate(sonnet, 0.04)],
  answers: [{ numbers: 301, minimum: 166, valid: true }, { numbers: 310, minimum: 172, valid: true }, { numbers: 325, minimum: 179, valid: true }],
  bank_built_at: builtAt, ...change,
});
const round = (change: Partial<FingerprintRound> = {}): FingerprintRound => ({
  id: 'r1', job_id: '', model_id: 'm1', questions: 3, analysis: analysis(), errors: [], returned_model: 'gpt-5.5-2026-10-01',
  usage: { input_tokens: 120, output_tokens: 2400, total_tokens: 2520 }, duration_ms: 41250, created_at: '2026-11-20T04:00:00Z', ...change,
});
const model = (change: Partial<FingerprintPublicModel> = {}): FingerprintPublicModel => ({
  id: 'm1', upstream_id: 'openai/gpt-5.5', display_name: 'GPT 5.5', protocol: 'chat', channel_id: 'c1', channel_name: '默认分组', platform_id: 'p1', platform_name: '示例平台',
  summary: null, round: null, checked_at: null, ...change,
});

const familyRound = round({
  id: 'r2', model_id: 'm2', returned_model: 'claude-sonnet-4-6-20260101', created_at: '2026-11-20T01:00:00Z',
  analysis: analysis({
    verdict: 'family', level: 'reference', valid_answers: 2, expected: sonnet, expected_rank: 2, confidence: null,
    candidates: [candidate(opus, null), candidate(sonnet, null)], answers: [{ numbers: 300, minimum: 166, valid: true }, { numbers: 310, minimum: 172, valid: true }],
  }),
  errors: [{ question: 3, code: 'rate_limited', message: '上游请求失败' }],
});
const mismatchRound = round({
  id: 'r4', model_id: 'm4', returned_model: '',
  analysis: analysis({ verdict: 'mismatch', expected: opus, expected_rank: 4, confidence: 0.88, candidates: [candidate(gpt, 0.88), candidate(sonnet, 0.07)] }),
});
const unlistedRound = round({
  id: 'r5', model_id: 'm5', questions: 1,
  analysis: analysis({ verdict: 'unlisted', level: 'test', valid_answers: 1, expected: null, expected_rank: 0, confidence: null, candidates: [candidate(gpt, null), candidate(sonnet, null)], answers: [{ numbers: 300, minimum: 166, valid: true }] }),
});
const failedRound = round({
  id: 'r6', model_id: 'm6', returned_model: '',
  analysis: analysis({ verdict: 'undetermined', level: '', reason: 'no_valid_answers', valid_answers: 0, expected: opus, expected_rank: 0, confidence: null, candidates: [], answers: [] }),
  errors: [1, 2, 3].map(question => ({ question, code: 'timeout', message: '上游请求失败' })),
});
const weakRound = round({
  id: 'h1', created_at: '2026-11-19T04:00:00Z', returned_model: '',
  analysis: analysis({ verdict: 'mismatch', level: 'test', valid_answers: 1, expected_rank: 2, confidence: null, candidates: [candidate(sonnet, null), candidate(gpt, null)], answers: [{ numbers: 300, minimum: 166, valid: true }] }),
  errors: [{ question: 2, code: 'authentication_error', message: '上游请求失败' }, { question: 3, code: 'timeout', message: '上游请求失败' }],
});

const other = { platform_id: 'p2', platform_name: '另一个平台', channel_id: 'c3', channel_name: 'Claude 专线' };
const ago = (milliseconds: number) => new Date(Date.now() - milliseconds).toISOString();

/** Six public models on two platforms, one for each thing the list can say about a model. */
function sample(): FingerprintPublicModel[] {
  const recent = ago(5 * 60000 + 5000), earlier = ago(3 * 3600000 + 60000);
  return [
    model({ summary: { verdict: 'match', agree: 2, verdicts: ['match', 'match', 'mismatch'], round_id: 'r1' }, round: round({ created_at: recent }), checked_at: recent }),
    model({ id: 'm2', upstream_id: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6', protocol: 'anthropic', summary: { verdict: 'family', agree: 1, verdicts: ['family'], round_id: 'r2' }, round: familyRound, checked_at: earlier }),
    model({ id: 'm3', upstream_id: 'mystery-model', display_name: '', protocol: 'responses', channel_id: 'c2', channel_name: '特价分组' }),
    model({ ...other, id: 'm4', upstream_id: 'claude-opus-4-5', display_name: 'Claude Opus', summary: { verdict: 'mismatch', agree: 3, verdicts: ['mismatch', 'mismatch', 'mismatch'], round_id: 'r4' }, round: mismatchRound, checked_at: earlier }),
    model({ ...other, id: 'm5', upstream_id: 'house-model', display_name: '自研模型', summary: { verdict: 'unlisted', agree: 1, verdicts: ['undetermined', 'unlisted'], round_id: 'r5' }, round: unlistedRound, checked_at: earlier }),
    model({ ...other, id: 'm6', upstream_id: 'broken-model', display_name: '常失败的模型', summary: { verdict: 'undetermined', agree: 2, verdicts: ['undetermined', 'undetermined'], round_id: 'r6' }, round: failedRound, checked_at: earlier }),
  ];
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
let client: QueryClient;
let results: FingerprintPublicResults;
let history: FingerprintRound[];
let requested: string[];
let refuse: (() => Response) | null;
let onSelf: () => void;

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  results = { models: sample() };
  history = [];
  requested = [];
  refuse = null;
  onSelf = vi.fn();
  vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url).replace('/api/v1', ''), method = init?.method || 'GET';
    requested.push(`${method} ${path}`);
    if (path === '/public/fingerprint/results' && method === 'GET') return refuse ? refuse() : json(results);
    if (path === '/public/fingerprint/models/m1/results' && method === 'GET') return json(history);
    throw new Error(`unexpected request ${method} ${path}`);
  }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

/** Renders the list and waits for its models. */
async function show() {
  render(<QueryClientProvider client={client}><App><FingerprintResults onSelf={onSelf}/></App></QueryClientProvider>);
  await screen.findByRole('region', { name: '平台指纹结果' });
}

const element = (selector: string) => document.querySelector<HTMLElement>(selector);
/** The table row of a model. The page also draws every model as a card for narrow screens, so names appear twice. */
const line = (id: string) => within(element(`tr[data-row-key="${id}"]`)!);
type Scope = ReturnType<typeof line>;
/** The row a table opens under the row of a model. */
function opened(id: string) {
  const next = element(`tr[data-row-key="${id}"]`)!.nextElementSibling as HTMLElement | null;
  return next?.classList.contains('ant-table-expanded-row') ? next : null;
}
const group = (name: string) => screen.getByRole('region', { name: `分组 ${name}` });
const cardsOf = (name: string) => within(group(name)).getAllByRole('article');
const platform = (name: string) => within(screen.getByRole('group', { name: '平台导航' })).getByRole('button', { name });
const total = (label: string) => within(screen.getByRole('region', { name: '指纹总览' })).getByText(label).parentElement;
const fact = (scope: Scope, label: string) => scope.getByText(label, { selector: 'dt' }).nextElementSibling;
const bars = (scope: Scope, name: string) => Array.from(scope.getByRole('img', { name }).children).map(bar => bar.className);
const search = (text: string) => fireEvent.change(screen.getByRole('textbox', { name: '搜索平台、分组或模型' }), { target: { value: text } });
const loads = () => requested.filter(request => request === 'GET /public/fingerprint/results').length;

describe('platform fingerprint results', () => {
  it('counts the models by conclusion and lists those of the first platform', async () => {
    await show();

    expect(total('检测范围')).toHaveTextContent('检测范围2 个平台3 个分组 · 6 个公开模型');
    expect(total('一致')).toHaveTextContent('一致1指纹与声明的模型对得上');
    expect(total('同家族不同型号')).toHaveTextContent('同家族不同型号1同一家族，型号不同');
    expect(total('不一致')).toHaveTextContent('不一致1最接近其他家族的模型');
    expect(total('未收录 / 无法判定')).toHaveTextContent('未收录 / 无法判定21 未收录 · 1 无法判定');
    expect(total('待检测')).toHaveTextContent('待检测1还没有检测记录');

    expect(platform('示例平台')).toHaveAttribute('aria-pressed', 'true');
    expect(platform('示例平台')).toHaveTextContent('1 一致 · 1 同家族 · 1 待检测');
    expect(platform('另一个平台')).toHaveAttribute('aria-pressed', 'false');
    expect(platform('另一个平台')).toHaveTextContent('1 不一致 · 1 未收录 · 1 无法判定');
    const heading = within(screen.getByRole('heading', { name: '示例平台' }).closest<HTMLElement>('.monitor-results-heading')!);
    expect(heading.getByText('2 个分组 / 3 个模型')).toBeInTheDocument();
    expect(heading.getByText('1 一致')).toBeInTheDocument();
    expect(heading.getByText('1 同家族不同型号')).toBeInTheDocument();
    expect(heading.getByText('1 待检测')).toBeInTheDocument();
    expect(heading.queryByText(/不一致/)).not.toBeInTheDocument();
    expect(screen.getByText('当前展示 3 / 6 个模型')).toBeInTheDocument();
    expect(element('tr[data-row-key="m4"]')).not.toBeInTheDocument();

    const matched = line('m1'), checked = results.models[0].checked_at!;
    expect(matched.getByText('GPT 5.5')).toBeInTheDocument();
    expect(matched.getByText('openai/gpt-5.5')).toBeInTheDocument();
    expect(matched.getByText('Chat Completions')).toBeInTheDocument();
    expect(matched.getByText('gpt-5.5')).toBeInTheDocument();
    expect(matched.getByText('GPT')).toBeInTheDocument();
    expect(matched.getByText('93.0%')).toBeInTheDocument();
    expect(matched.getByText('一致')).toBeInTheDocument();
    expect(matched.queryByText(/有效回答/)).not.toBeInTheDocument();
    expect(bars(matched, '最近 3 轮，从旧到新：不一致、一致、一致')).toEqual(['mismatch', 'match', 'match']);
    expect(matched.getByText('2 / 3 一致')).toBeInTheDocument();
    expect(matched.getByText(shortTime(checked))).toBeInTheDocument();
    expect(matched.getByText('5 分钟前')).toBeInTheDocument();

    const family = line('m2');
    expect(family.getByText('Anthropic')).toBeInTheDocument();
    expect(family.getByText('claude-opus-4.5')).toBeInTheDocument();
    expect(family.getByText('—')).toBeInTheDocument();
    expect(family.getByText('同家族不同型号')).toBeInTheDocument();
    expect(family.getByText('参考 · 2 个有效回答')).toBeInTheDocument();
    expect(bars(family, '最近 1 轮，从旧到新：同家族不同型号')).toEqual(['family', 'none', 'none']);
    expect(family.getByText('1 / 1 同家族')).toBeInTheDocument();
    expect(family.getByText('3 小时前')).toBeInTheDocument();

    const pending = line('m3');
    expect(pending.getByText('mystery-model')).toBeInTheDocument();
    expect(pending.getByText('Responses')).toBeInTheDocument();
    expect(pending.getByText('待检测')).toBeInTheDocument();
    expect(pending.getAllByText('—')).toHaveLength(2);
    expect(bars(pending, '尚未检测')).toEqual(['none', 'none', 'none']);
    expect(pending.getByText('尚未检测')).toBeInTheDocument();
    expect(pending.getByText('等待后台发起')).toBeInTheDocument();
    expect(pending.queryByRole('button')).not.toBeInTheDocument();
  });

  it('draws every model as a card for narrow screens', async () => {
    await show();
    const cards = cardsOf('默认分组');
    expect(cards).toHaveLength(2);
    const card = within(cards[0]);
    expect(card.getByText('GPT 5.5')).toBeInTheDocument();
    expect(card.getByText('一致')).toBeInTheDocument();
    expect(card.getByText('gpt-5.5')).toBeInTheDocument();
    expect(card.getByText('93.0%')).toBeInTheDocument();
    expect(bars(card, '最近 3 轮，从旧到新：不一致、一致、一致')).toEqual(['mismatch', 'match', 'match']);
    expect(card.getByText('5 分钟前')).toBeInTheDocument();
    expect(card.getByRole('button', { name: '展开 GPT 5.5 的检测详情' })).toHaveTextContent('详情');
    expect(card.getByRole('button', { name: '查看 GPT 5.5 的指纹历史' })).toHaveTextContent('历史');

    const pending = within(cardsOf('特价分组')[0]);
    expect(pending.getByText('待检测')).toBeInTheDocument();
    expect(pending.queryByRole('button')).not.toBeInTheDocument();
  });

  it('switches to the models of another platform', async () => {
    await show();
    fireEvent.click(platform('另一个平台'));

    expect(platform('另一个平台')).toHaveAttribute('aria-pressed', 'true');
    expect(platform('示例平台')).toHaveAttribute('aria-pressed', 'false');
    const heading = within(screen.getByRole('heading', { name: '另一个平台' }).closest<HTMLElement>('.monitor-results-heading')!);
    expect(heading.getByText('1 个分组 / 3 个模型')).toBeInTheDocument();
    expect(heading.getByText('1 不一致')).toBeInTheDocument();
    expect(heading.getByText('1 未收录')).toBeInTheDocument();
    expect(heading.getByText('1 无法判定')).toBeInTheDocument();
    expect(element('tr[data-row-key="m1"]')).not.toBeInTheDocument();
    expect(screen.getByText('当前展示 3 / 6 个模型')).toBeInTheDocument();

    const mismatch = line('m4');
    expect(mismatch.getByText('不一致')).toBeInTheDocument();
    expect(mismatch.getByText('gpt-5.5')).toBeInTheDocument();
    expect(mismatch.getByText('88.0%')).toBeInTheDocument();
    expect(mismatch.getByText('3 / 3 不一致')).toBeInTheDocument();

    const unlisted = line('m5');
    expect(unlisted.getByText('未收录')).toBeInTheDocument();
    expect(unlisted.getByText('测试 · 1 个有效回答')).toBeInTheDocument();
    expect(bars(unlisted, '最近 2 轮，从旧到新：未收录、无法判定')).toEqual(['unlisted', 'undetermined', 'none']);
    expect(unlisted.getByText('1 / 2 未收录')).toBeInTheDocument();

    const failed = line('m6');
    expect(failed.getByText('无法判定')).toBeInTheDocument();
    expect(failed.getAllByText('—')).toHaveLength(2);
    expect(failed.getByText('2 / 2 无法判定')).toBeInTheDocument();
  });

  it('searches the names of platforms, channels and models', async () => {
    await show();
    search(' CLAUDE ');
    expect(platform('示例平台')).toHaveTextContent('1 个匹配模型');
    expect(platform('另一个平台')).toHaveTextContent('3 个匹配模型');
    expect(screen.getByText('2 个分组 / 3 个模型 · 1 个匹配')).toBeInTheDocument();
    expect(element('tr[data-row-key="m2"]')).toBeInTheDocument();
    expect(element('tr[data-row-key="m1"]')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '分组 特价分组' })).not.toBeInTheDocument();
    expect(screen.getByText('当前展示 1 / 6 个模型')).toBeInTheDocument();
    const heading = within(screen.getByRole('heading', { name: '示例平台' }).closest<HTMLElement>('.monitor-results-heading')!);
    expect(heading.getByText('1 一致')).toBeInTheDocument();
    expect(heading.getByText('1 待检测')).toBeInTheDocument();

    search('另一个');
    expect(platform('示例平台')).toBeDisabled();
    expect(platform('另一个平台')).toHaveTextContent('3 个匹配模型');
    expect(screen.getByText('当前展示 3 / 6 个模型')).toBeInTheDocument();

    search('opus');
    expect(platform('示例平台')).toBeDisabled();
    expect(platform('示例平台')).toHaveTextContent('0 个匹配模型');
    expect(platform('另一个平台')).toHaveAttribute('aria-pressed', 'true');
    expect(element('tr[data-row-key="m4"]')).toBeInTheDocument();
    expect(element('tr[data-row-key="m2"]')).not.toBeInTheDocument();
    expect(element('tr[data-row-key="m5"]')).not.toBeInTheDocument();

    search('特价');
    expect(platform('示例平台')).toHaveAttribute('aria-pressed', 'true');
    expect(element('tr[data-row-key="m3"]')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '分组 默认分组' })).not.toBeInTheDocument();

    search('没有这个模型');
    expect(screen.getByText('没有符合条件的模型')).toBeInTheDocument();
    expect(screen.getByText('当前展示 0 / 6 个模型')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '折叠全部' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));
    expect(screen.getByRole('textbox', { name: '搜索平台、分组或模型' })).toHaveValue('');
    expect(screen.getByText('当前展示 3 / 6 个模型')).toBeInTheDocument();
    expect(element('tr[data-row-key="m1"]')).toBeInTheDocument();
  });

  it('keeps only the models whose fingerprint is closest to another model', async () => {
    await show();
    fireEvent.click(screen.getByRole('switch', { name: '仅看存疑' }));
    expect(element('tr[data-row-key="m2"]')).toBeInTheDocument();
    expect(element('tr[data-row-key="m1"]')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '分组 特价分组' })).not.toBeInTheDocument();
    expect(platform('另一个平台')).toHaveTextContent('1 个匹配模型');
    expect(screen.getByText('当前展示 1 / 6 个模型')).toBeInTheDocument();

    fireEvent.click(platform('另一个平台'));
    expect(element('tr[data-row-key="m4"]')).toBeInTheDocument();
    expect(element('tr[data-row-key="m5"]')).not.toBeInTheDocument();
    expect(element('tr[data-row-key="m6"]')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('switch', { name: '仅看存疑' }));
    expect(element('tr[data-row-key="m5"]')).toBeInTheDocument();
    expect(screen.getByText('当前展示 3 / 6 个模型')).toBeInTheDocument();
  });

  it('clears the doubt filter too when no model is in doubt', async () => {
    results = { models: [sample()[0]] };
    await show();
    fireEvent.click(screen.getByRole('switch', { name: '仅看存疑' }));
    expect(screen.getByText('没有符合条件的模型')).toBeInTheDocument();
    expect(platform('示例平台')).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));
    expect(screen.getByRole('switch', { name: '仅看存疑' })).not.toBeChecked();
    expect(element('tr[data-row-key="m1"]')).toBeInTheDocument();
    expect(platform('示例平台')).toBeEnabled();
  });

  it('folds one channel or all of them', async () => {
    await show();
    const toggle = (name: string) => within(group(name)).getByRole('button', { name: new RegExp(name) });
    expect(toggle('默认分组')).toHaveTextContent('2 个模型');
    expect(toggle('默认分组')).toHaveTextContent('1 一致 · 1 同家族');
    expect(toggle('默认分组')).toHaveAttribute('aria-expanded', 'true');

    fireEvent.click(toggle('默认分组'));
    expect(toggle('默认分组')).toHaveAttribute('aria-expanded', 'false');
    expect(element('tr[data-row-key="m1"]')).not.toBeInTheDocument();
    expect(within(group('默认分组')).queryByRole('article')).not.toBeInTheDocument();
    expect(element('tr[data-row-key="m3"]')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '折叠全部' }));
    expect(toggle('特价分组')).toHaveAttribute('aria-expanded', 'false');
    expect(element('tr[data-row-key="m3"]')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '展开全部' }));
    expect(toggle('默认分组')).toHaveAttribute('aria-expanded', 'true');
    expect(element('tr[data-row-key="m1"]')).toBeInTheDocument();
    expect(element('tr[data-row-key="m3"]')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '折叠全部' })).toBeEnabled();
  });

  it('opens the round behind a conclusion, naming failed questions by their code only', async () => {
    await show();
    expect(opened('m2')).not.toBeInTheDocument();
    fireEvent.click(line('m2').getByRole('button', { name: '展开 Claude Sonnet 4.6 的检测详情' }));

    const detail = within(opened('m2')!);
    expect(detail.getByText('候选排名（前 2）')).toBeInTheDocument();
    expect(detail.getByText('claude-opus-4.5')).toBeInTheDocument();
    expect(detail.getByText('声明的模型', { selector: 'em' }).parentElement).toHaveTextContent('claude-sonnet-4.6');
    expect(detail.getByText('本轮 3 道题')).toBeInTheDocument();
    expect(detail.getByText('回答 1').parentElement).toHaveTextContent('写出 300 个整数 · 至少需要 166 个');
    expect(detail.getByText('回答 2').parentElement).toHaveTextContent('写出 310 个整数 · 至少需要 172 个');
    expect(detail.getAllByText('有效')).toHaveLength(2);
    expect(detail.getByText('第 3 题').parentElement).toHaveTextContent('上游请求失败 · rate_limited');
    expect(detail.getAllByText('请求失败')).toHaveLength(1);
    expect(fact(detail, '检测时间')).toHaveTextContent(time('2026-11-20T01:00:00Z'));
    expect(fact(detail, '结果等级')).toHaveTextContent('参考（2 个回答有效，只给出排名）');
    expect(fact(detail, '声明的模型')).toHaveTextContent('claude-sonnet-4.6');
    expect(fact(detail, '声明模型的排名')).toHaveTextContent('第 2 位');
    expect(fact(detail, '上游返回的模型名')).toHaveTextContent('claude-sonnet-4-6-20260101');
    expect(fact(detail, '请求协议')).toHaveTextContent('Anthropic');
    expect(fact(detail, '参考库版本')).toHaveTextContent('2026-11-19');
    expect(within(cardsOf('默认分组')[1]).getByText('本轮 3 道题')).toBeInTheDocument();
    expect(within(cardsOf('默认分组')[0]).queryByText(/本轮/)).not.toBeInTheDocument();
    expect(opened('m1')).not.toBeInTheDocument();

    const close = line('m2').getByRole('button', { name: '收起 Claude Sonnet 4.6 的检测详情' });
    expect(close).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(close);
    expect(opened('m2')).not.toBeVisible();
    expect(within(cardsOf('默认分组')[1]).queryByText(/本轮/)).not.toBeInTheDocument();
    expect(line('m2').getByRole('button', { name: '展开 Claude Sonnet 4.6 的检测详情' })).toHaveAttribute('aria-expanded', 'false');
  });

  it('says why a round could not be compared', async () => {
    await show();
    fireEvent.click(platform('另一个平台'));
    fireEvent.click(line('m6').getByRole('button', { name: '展开 常失败的模型 的检测详情' }));
    const failed = within(opened('m6')!);
    expect(failed.getByText('候选排名')).toBeInTheDocument();
    expect(failed.getByText('这一轮的请求都没有成功，没有回答可以比对。')).toBeInTheDocument();
    expect(failed.getAllByText('请求失败')).toHaveLength(3);
    expect(failed.getAllByText('上游请求失败 · timeout')).toHaveLength(3);
    expect(failed.queryByText(/^回答/)).not.toBeInTheDocument();
    expect(fact(failed, '结果等级')).toHaveTextContent('无法判定');
    expect(fact(failed, '声明的模型')).toHaveTextContent('claude-opus-4.5');
    expect(failed.queryByText('声明模型的排名')).not.toBeInTheDocument();
    expect(fact(failed, '上游返回的模型名')).toHaveTextContent('—');

    fireEvent.click(line('m5').getByRole('button', { name: '展开 自研模型 的检测详情' }));
    const unlisted = within(opened('m5')!);
    expect(unlisted.getByText('本轮 1 道题')).toBeInTheDocument();
    expect(fact(unlisted, '结果等级')).toHaveTextContent('测试（1 个回答有效，仅作测试）');
    expect(fact(unlisted, '声明的模型')).toHaveTextContent('参考库未收录');
  });

  it('says when the numbers of a round do not look like those of a model', async () => {
    const odd = round({ analysis: analysis({ verdict: 'undetermined', level: '', reason: 'not_model_like', valid_answers: 1, expected_rank: 0, confidence: null, candidates: [], answers: [{ numbers: 300, minimum: 166, valid: true }] }) });
    const short = round({ analysis: analysis({ verdict: 'undetermined', level: '', reason: 'no_valid_answers', valid_answers: 0, expected_rank: 0, confidence: null, candidates: [], answers: [{ numbers: 12, minimum: 166, valid: false }] }) });
    const undetermined = { verdict: 'undetermined', agree: 1, verdicts: ['undetermined'], round_id: 'r1' } as const;
    results = { models: [model({ summary: { ...undetermined, verdicts: ['undetermined'] }, round: odd, checked_at: odd.created_at }), model({ id: 'm2', display_name: '写不够的模型', summary: { ...undetermined, verdicts: ['undetermined'] }, round: short, checked_at: short.created_at })] };
    await show();
    expect(total('未收录 / 无法判定')).toHaveTextContent('未收录 / 无法判定20 未收录 · 2 无法判定');
    expect(total('待检测')).toHaveTextContent('待检测0还没有检测记录');
    fireEvent.click(line('m1').getByRole('button', { name: '展开 GPT 5.5 的检测详情' }));
    expect(within(opened('m1')!).getByText('这一轮的数字不像是模型直接写出来的（例如按顺序排列），无法比对。')).toBeInTheDocument();

    fireEvent.click(line('m2').getByRole('button', { name: '展开 写不够的模型 的检测详情' }));
    const detail = within(opened('m2')!);
    expect(detail.getByText('这一轮没有一个回答的数字够数，无法比对。')).toBeInTheDocument();
    expect(detail.getByText('回答 1').parentElement).toHaveTextContent('写出 12 个整数 · 至少需要 166 个');
    expect(detail.getByText('无效')).toBeInTheDocument();
  });

  it('shows the rounds of one model, newest first, each with its detail', async () => {
    history = [round({ id: 'h2' }), weakRound];
    await show();
    expect(requested).toEqual(['GET /public/fingerprint/results']);
    fireEvent.click(line('m1').getByRole('button', { name: '查看 GPT 5.5 的指纹历史' }));

    const dialog = within(await screen.findByRole('dialog', { name: '指纹历史 · GPT 5.5' }));
    expect(dialog.getByText('示例平台 / 默认分组')).toBeInTheDocument();
    expect(dialog.getByText('openai/gpt-5.5')).toBeInTheDocument();
    expect(dialog.getByText('最近 3 轮中 2 轮的结论')).toBeInTheDocument();
    const summary = (label: string) => dialog.getByText(label, { selector: '.monitor-history-summary span' }).nextElementSibling;
    expect(summary('声明的模型')).toHaveTextContent('gpt-5.5');
    expect(summary('指纹最接近')).toHaveTextContent('gpt-5.5');
    expect(summary('置信度')).toHaveTextContent('93.0%');
    expect(dialog.getByText(/每个模型保留最近 30 轮/)).toBeInTheDocument();

    const full = within((await dialog.findByText(time('2026-11-20T04:00:00Z'))).closest('tr')!);
    expect(requested).toEqual(['GET /public/fingerprint/results', 'GET /public/fingerprint/models/m1/results']);
    expect(full.getByText('一致')).toBeInTheDocument();
    expect(full.getByText('gpt-5.5')).toBeInTheDocument();
    expect(full.getByText('93.0%')).toBeInTheDocument();
    expect(full.getByText('3 / 3')).toBeInTheDocument();
    expect(full.getByText('2026-11-19')).toBeInTheDocument();
    const weak = within(dialog.getByText(time('2026-11-19T04:00:00Z')).closest('tr')!);
    expect(weak.getByText('不一致')).toBeInTheDocument();
    expect(weak.getByText('测试 · 1 个有效回答')).toBeInTheDocument();
    expect(weak.getByText('claude-sonnet-4.6')).toBeInTheDocument();
    expect(weak.getByText('—')).toBeInTheDocument();
    expect(weak.getByText('1 / 3')).toBeInTheDocument();
    const times = dialog.getAllByText(/^2026\/11\/(19|20) /).map(cell => cell.textContent);
    expect(times).toEqual([time('2026-11-20T04:00:00Z'), time('2026-11-19T04:00:00Z')]);
    expect(dialog.queryByText(/本轮/)).not.toBeInTheDocument();

    fireEvent.click(weak.getByRole('button'));
    expect(await dialog.findByText('本轮 3 道题')).toBeInTheDocument();
    expect(dialog.getByText('第 2 题').parentElement).toHaveTextContent('上游请求失败 · authentication_error');
    expect(dialog.getByText('第 3 题').parentElement).toHaveTextContent('上游请求失败 · timeout');
    expect(fact(dialog, '声明模型的排名')).toHaveTextContent('第 2 位');
    expect(fact(dialog, '请求协议')).toHaveTextContent('Chat Completions');

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('says when a model has no rounds left to show', async () => {
    await show();
    fireEvent.click(line('m1').getByRole('button', { name: '查看 GPT 5.5 的指纹历史' }));
    const dialog = within(await screen.findByRole('dialog', { name: '指纹历史 · GPT 5.5' }));
    expect(await dialog.findByText('还没有检测记录')).toBeInTheDocument();
  });

  it('leads to the self check while there are no public models', async () => {
    results = { models: [] };
    render(<QueryClientProvider client={client}><App><FingerprintResults onSelf={onSelf}/></App></QueryClientProvider>);
    expect(await screen.findByText('还没有平台检测结果')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '平台指纹结果' })).not.toBeInTheDocument();
    expect(onSelf).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '去自助检测' }));
    expect(onSelf).toHaveBeenCalledTimes(1);
  });

  it('reports a list that could not be read', async () => {
    refuse = () => json({ error: { code: 'internal_error', message: '数据库暂时不可用' } }, 500);
    render(<QueryClientProvider client={client}><App><FingerprintResults onSelf={onSelf}/></App></QueryClientProvider>);
    expect(await screen.findByText('读取失败')).toBeInTheDocument();
    expect(screen.getByText('数据库暂时不可用')).toBeInTheDocument();
    expect(screen.queryByText('还没有平台检测结果')).not.toBeInTheDocument();
  });

  it('reads the list again every minute', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await show();
    expect(line('m1').getByText('一致')).toBeInTheDocument();
    expect(loads()).toBe(1);

    results = { models: [model({ summary: { verdict: 'mismatch', agree: 2, verdicts: ['mismatch', 'mismatch', 'match'], round_id: 'r4' }, round: mismatchRound, checked_at: mismatchRound.created_at })] };
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(loads()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(31000); });
    await waitFor(() => expect(loads()).toBe(2));
    expect(await line('m1').findByText('不一致')).toBeInTheDocument();
    expect(line('m1').getByText('2 / 3 不一致')).toBeInTheDocument();
  }, 15000);
});
