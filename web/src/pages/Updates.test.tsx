import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpdatesPage } from './Updates';
import type { UpdateSnapshot } from '../types';

const snapshot: UpdateSnapshot = {
  enabled: true,
  repository: '70548887/relay-api-monitor',
  current: { version: '1.1.1', commit: '1234567890abcdef', os: 'windows', arch: 'amd64', build_time: '2026-10-26T00:00:00Z' },
  latest: { current_version: '1.1.1', latest_version: '1.2.0', published_at: '2026-10-26T01:00:00Z', compatible: true, available: true, notes: '修复升级流程', asset_name: 'relaylab-windows-amd64.exe', asset_size: 2048, sha256: 'abc123' },
  release: { tag_name: 'v1.2.0', name: 'v1.2.0', published_at: '2026-10-26T01:00:00Z', body: '修复升级流程', url: 'https://github.com/70548887/relay-api-monitor/releases/tag/v1.2.0', assets: [{ name: 'relaylab-windows-amd64.exe', os: 'windows', arch: 'amd64', size: 2048, sha256: 'abc123', verified: true }] },
  operation: { id: 'op-1', state: 'available', version: '1.2.0', progress: 0 },
  state: 'available',
};

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window);
  vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('online updates page', () => {
  it('shows release metadata and lets the administrator start a download', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    client.setQueryData(['admin-updates'], snapshot); client.setQueryData(['admin-updates-status'], snapshot.operation); client.setQueryData(['admin-updates-history'], []);
    const fetcher = vi.fn().mockResolvedValue(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })); vi.stubGlobal('fetch', fetcher);
    render(<QueryClientProvider client={client}><App><UpdatesPage/></App></QueryClientProvider>);
    expect(screen.getAllByText('1.2.0').length).toBeGreaterThan(0); expect(screen.getByText('relaylab-windows-amd64.exe')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '下载并校验' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/updates/download', expect.objectContaining({ method: 'POST' })));
    expect(fetcher.mock.calls[0][0]).toBe('/api/v1/admin/updates/download'); client.clear();
  });
});
