import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AdminVersion, useAdminVersion } from './AdminVersion';
import type { UpdateSnapshot } from '../types';

const clients: QueryClient[] = [];
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.unstubAllGlobals(); });

function data(): UpdateSnapshot {
  return { enabled: true, install_enabled: false, install_mode: 'container', repository: '70548887/relay-api-monitor', current: { version: '1.4.2' } };
}

function LiveVersion() {
  const query = useAdminVersion();
  return <AdminVersion snapshot={query.data} failed={query.isError}/>;
}

function show(snapshot?: UpdateSnapshot, failed = false) {
  return render(<MemoryRouter><AdminVersion snapshot={snapshot} failed={failed}/></MemoryRouter>);
}

describe('administrator version notice', () => {
  it('shows the server version and links to updates', () => {
    show(data());
    expect(screen.getByRole('link', { name: /当前版本 v1.4.2/ })).toHaveAttribute('href', '/admin/updates');
    expect(screen.getByText('v1.4.2')).toBeInTheDocument();
    expect(screen.queryByText(/新版本/)).not.toBeInTheDocument();
  });

  it('announces newer releases even when container installation is unavailable', () => {
    const snapshot = data();
    snapshot.latest = { current_version: '1.4.2', latest_version: 'v1.4.3', available: true, compatible: true };
    show(snapshot);
    expect(screen.getByText('新版本 v1.4.3')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /发现新版本 v1.4.3/ })).toBeInTheDocument();
  });

  it('does not mistake an existing release for an available update', () => {
    const snapshot = data();
    snapshot.latest = { current_version: '1.4.2', latest_version: 'v1.4.2', available: false, compatible: true };
    show(snapshot);
    expect(screen.queryByText(/新版本/)).not.toBeInTheDocument();
  });

  it('keeps the last known version with an explicit failed status', () => {
    show(data(), true);
    expect(screen.getByText('v1.4.2')).toBeInTheDocument();
    expect(screen.getByText('状态读取失败')).toBeInTheDocument();
    expect(screen.queryByText(/已是最新/)).not.toBeInTheDocument();
  });

  it.each([
    { kind: undefined, label: '更新操作失败' },
    { kind: 'check', label: '版本检查失败' },
    { kind: 'download', label: '更新操作失败' },
    { kind: 'apply', label: '更新操作失败' },
    { kind: 'rollback', label: '更新操作失败' },
  ])('explains a persisted $kind operation failure without mislabeling it', ({ kind, label }) => {
    const snapshot = data();
    snapshot.operation = { state: 'failed', kind, error_code: 'timeout' };
    show(snapshot);
    expect(screen.getByText('v1.4.2')).toBeInTheDocument();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: new RegExp(`${label}，点击查看详情`) })).toHaveAttribute('href', '/admin/updates');
    expect(screen.queryByText('状态读取失败')).not.toBeInTheDocument();
    if (kind !== 'check') expect(screen.queryByText('版本检查失败')).not.toBeInTheDocument();
  });

  it('shows an upstream check failure returned in an HTTP 200 snapshot', async () => {
    const snapshot = data();
    snapshot.operation = { state: 'failed', kind: 'check', error_code: 'timeout' };
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(snapshot), { status: 200 })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    clients.push(client);
    render(<QueryClientProvider client={client}><MemoryRouter><LiveVersion/></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText('版本检查失败')).toBeInTheDocument();
    expect(screen.getByText('v1.4.2')).toBeInTheDocument();
    expect(screen.queryByText('状态读取失败')).not.toBeInTheDocument();
  });

  it('never fabricates a version when the request fails or returns no build', () => {
    show(undefined, true);
    expect(screen.getByText('版本信息暂不可用')).toBeInTheDocument();
    expect(screen.queryByText('dev')).not.toBeInTheDocument();
    cleanup();
    show({} as UpdateSnapshot);
    expect(screen.getByText('获取版本中…')).toBeInTheDocument();
  });

  it('only reads cached update status and never starts update operations', async () => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify(data()), { status: 200 }));
    vi.stubGlobal('fetch', fetcher);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    clients.push(client);
    render(<QueryClientProvider client={client}><MemoryRouter><LiveVersion/></MemoryRouter></QueryClientProvider>);
    await screen.findByText('v1.4.2');
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/updates/status', expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(fetcher.mock.calls.every(([, init]) => !init?.method || init.method === 'GET')).toBe(true);
    expect(client.getQueryData(['admin-updates'])).toEqual(data());
  });
});
