import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Gallery from './Gallery';
import type { Artifact, IntelligenceData, IntelligenceGroup } from '../types';

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock('../api', () => ({ api: request }));
// These regressions exercise selection and metadata; thumbnail capture has its own tests.
vi.mock('../components/Thumbnail', () => ({
  Thumbnail: ({ title }: { title: string }) => <span>{title} thumbnail</span>,
}));

const clients: QueryClient[] = [];
const queryKey = ['intelligence', 'cn_html', undefined];

function artwork(id: string, overrides: Partial<Artifact> = {}): Artifact {
  return {
    id,
    job_id: `job-${id}`,
    model_id: 'model-fixture',
    channel_id: 'channel-fixture',
    template_id: 'cn_html',
    template_version: 1,
    prompt: 'exact independent fixture prompt',
    requested_model: `${id}-model`,
    display_name: `${id}-model`,
    returned_model: `${id}-returned`,
    reasoning_effort: 'low',
    thinking_mode: '',
    thinking_budget: 0,
    verified_group: '',
    group_verification_status: 'unconfigured',
    protocol: 'responses',
    kind: 'html',
    created_at: '2026-10-28T06:06:14Z',
    started_at: '2026-10-28T06:06:12Z',
    duration_ms: 2000,
    first_text_ms: 30,
    usage: { input_tokens: 10, output_tokens: 20, total_tokens: 30 },
    finish_reason: 'completed',
    parameters: JSON.stringify({ context_policy: 'independent' }),
    code: `<svg><text>${id}</text></svg>`,
    preview_document: `<html><body><svg><text>${id}</text></svg></body></html>`,
    ...overrides,
  } as Artifact;
}

function publicGroup(history: Artifact[]): IntelligenceGroup {
  return {
    id: 'channel-fixture',
    channel_id: 'channel-fixture',
    channel_name: '布局测试分组',
    platform_id: 'platform-fixture',
    platform_name: '布局测试平台',
    model_id: 'model-fixture',
    requested_model: 'plan-model',
    models: [{ id: 'model-fixture', upstream_id: 'plan-model', display_name: 'plan-model', protocol: 'responses' }],
    group_verification: { status: 'unconfigured', scope: 'token_binding' },
    reasoning_effort: 'high',
    thinking_mode: '',
    thinking_budget: 0,
    interval_seconds: 7200,
    next_run_at: '2026-10-28T08:00:00Z',
    automatic_enabled: true,
    schedule_state: 'running',
    schedule_message: '当前任务正在执行',
    latest_attempt: {
      id: 'current-inflight',
      state: 'running',
      requested_model: 'inflight-model',
      returned_model: 'inflight-returned',
      reasoning_effort: 'xhigh',
      started_at: '2026-10-28T06:10:00Z',
      progress_chars: 9876,
      context_policy: 'independent',
      usage: { input_tokens: 910, output_tokens: 920, total_tokens: 1830 },
    },
    history,
  };
}

function intelligence(group: IntelligenceGroup): IntelligenceData {
  return {
    groups: [group],
    history_scope: 'group',
    updated_at: '2026-10-28T06:10:05Z',
    worker_online: true,
  };
}

function setup(group: IntelligenceGroup, cacheDetails = true, extraGroups: IntelligenceGroup[] = []) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  clients.push(client);
  client.setQueryData(['templates'], [{ id: 'cn_html', version: 1, prompt: 'exact independent fixture prompt' }]);
  client.setQueryData<IntelligenceData>(queryKey, { ...intelligence(group), groups: [group, ...extraGroups] });
  if (cacheDetails) [group, ...extraGroups].flatMap(item => item.history).forEach(item => { client.setQueryData<Artifact>(['artifact', item.id], item); });
  render(<QueryClientProvider client={client}><Gallery /></QueryClientProvider>);
  return client;
}

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({
    matches: false, addListener: vi.fn(), removeListener: vi.fn(),
    addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  request.mockReset();
  request.mockRejectedValue(new Error('Unexpected fixture request'));
});

afterEach(() => {
  cleanup();
  clients.splice(0).forEach(client => client.clear());
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('intelligence side-by-side workbench', () => {
  it('shows selected historical metrics separately from the current execution', () => {
    const latest = artwork('latest');
    const older = artwork('older', {
      reasoning_effort: 'medium', duration_ms: 4567,
      usage: { input_tokens: 210, output_tokens: 777, total_tokens: 987 },
    });
    setup(publicGroup([latest, older]));
    fireEvent.click(screen.getByRole('button', { name: '预览 older-model 2' }));

    const information = within(screen.getByRole('region', { name: '所选作品信息' }));
    expect(information.getByText('older-returned')).toBeInTheDocument();
    expect(information.getByText('medium')).toBeInTheDocument();
    expect(information.getByText('4.57 s')).toBeInTheDocument();
    expect(information.queryByText('inflight-returned')).not.toBeInTheDocument();
    expect(information.queryByText('xhigh')).not.toBeInTheDocument();
    expect(screen.getByTitle('older-model · 当前作品')).toHaveAttribute('sandbox', 'allow-scripts');

    const execution = within(screen.getByRole('region', { name: '当前分组状态' }));
    expect(execution.getByText('inflight-model')).toBeInTheDocument();
    expect(execution.queryByText('older-returned')).not.toBeInTheDocument();
  });

  it('returns to the latest successful work only when explicitly requested', () => {
    setup(publicGroup([artwork('latest'), artwork('older')]));
    expect(screen.queryByRole('button', { name: '回到最新' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '预览 older-model 2' }));
    expect(screen.getByTitle('older-model · 当前作品')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '回到最新' }));
    expect(screen.getByTitle('latest-model · 当前作品')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '回到最新' })).not.toBeInTheDocument();
  });

  it('falls back to a currently public work when the pinned one disappears from the refreshed list', async () => {
    const latest = artwork('latest');
    const older = artwork('older');
    const client = setup(publicGroup([latest, older]));
    fireEvent.click(screen.getByRole('button', { name: '预览 older-model 2' }));
    expect(screen.getByTitle('older-model · 当前作品')).toBeInTheDocument();
    await act(async () => { client.setQueryData<IntelligenceData>(queryKey, intelligence(publicGroup([latest]))); });
    expect(await screen.findByTitle('latest-model · 当前作品')).toBeInTheDocument();
    expect(screen.queryByTitle('older-model · 当前作品')).not.toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: '所选作品信息' })).queryByText('older-returned')).not.toBeInTheDocument();
  });

  it('does not borrow token usage from the running task when selected usage was not supplied', () => {
    setup(publicGroup([artwork('no-usage', { usage: undefined })]));
    const information = within(screen.getByRole('region', { name: '所选作品信息' }));
    expect(information.getAllByText('未提供').length).toBeGreaterThan(0);
    expect(information.queryByText('1,830')).not.toBeInTheDocument();
    expect(information.queryByText('1830')).not.toBeInTheDocument();
  });

  it('keeps catalog navigation and cross-group comparison selections when a model filter returns one group', async () => {
    const first = artwork('primary');
    const other = artwork('other', { channel_id: 'other-channel', model_id: 'other-model' });
    const primaryGroup = publicGroup([first]);
    const otherGroup = {
      ...publicGroup([other]), id: 'other-channel', channel_id: 'other-channel',
      channel_name: '另一分组', model_id: 'other-model', requested_model: 'other-model',
      models: [{ id: 'other-model', upstream_id: 'other-model', display_name: 'other-model', protocol: 'responses' }],
    };
    const otherPlatform = {
      ...otherGroup, id: 'third-channel', channel_id: 'third-channel', channel_name: '第三分组',
      platform_id: 'other-platform', platform_name: '另一平台', history: [],
    };
    request.mockImplementation((path: string) => path === '/public/intelligence?template_id=cn_html&model_id=model-fixture'
      ? Promise.resolve(intelligence(primaryGroup))
      : Promise.reject(new Error(`Unexpected fixture request: ${path}`)));
    setup(primaryGroup, true, [otherGroup, otherPlatform]);

    const groups = within(screen.getByRole('group', { name: '分组' }));
    fireEvent.click(groups.getByRole('button', { name: '另一分组' }));
    fireEvent.click(screen.getByLabelText('选择作品 1'));
    fireEvent.click(groups.getByRole('button', { name: '布局测试分组' }));
    expect(screen.getByText('已选 1/2')).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '筛选模型' }));
    fireEvent.click(await screen.findByText('plan-model', { selector: '.ant-select-item-option-content' }));
    await waitFor(() => expect(request).toHaveBeenCalledWith(
      '/public/intelligence?template_id=cn_html&model_id=model-fixture',
      expect.objectContaining({ signal: expect.anything() }),
    ));
    expect(await screen.findByTitle('primary-model · 当前作品')).toBeInTheDocument();
    expect(within(screen.getByRole('group', { name: '平台' })).getByRole('button', { name: '另一平台' })).toBeInTheDocument();
    expect(within(screen.getByRole('group', { name: '分组' })).getByRole('button', { name: '另一分组' })).toBeInTheDocument();
    expect(screen.getByText('已选 1/2')).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText('选择作品 1'));
    expect(screen.getByText('已选 2/2')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '并排比较' }));
    expect(await screen.findByTitle('比较 · other-model')).toBeInTheDocument();
    expect(screen.getByTitle('比较 · primary-model')).toBeInTheDocument();
  });

  it('keeps a failed private or unavailable detail out of the executable preview', async () => {
    request.mockRejectedValue(new Error('fixture detail unavailable'));
    setup(publicGroup([artwork('unavailable', { code: undefined, preview_document: undefined })]), false);
    expect(await screen.findByText('fixture detail unavailable')).toBeInTheDocument();
    expect(screen.queryByTitle('unavailable-model · 当前作品')).not.toBeInTheDocument();
    expect(request).toHaveBeenCalledWith('/public/artifacts/unavailable', expect.objectContaining({ signal: expect.anything() }));
  });
});
