import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, expect, it, vi } from 'vitest';
import { ChannelModelSyncButton } from './ChannelModelSyncButton';
import type { Channel, Model } from '../types';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it('synchronizes only the selected channel and replaces only its model cache', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const channel = { id: 'channel-a', name: 'Group A' } as Channel;
  const other = { id: 'model-b', channel_id: 'channel-b', upstream_id: 'keep-me' } as Model;
  const old = { id: 'model-old', channel_id: channel.id, upstream_id: 'old-model' } as Model;
  const fresh = { id: 'model-new', channel_id: channel.id, upstream_id: 'new-model' } as Model;
  client.setQueryData(['admin-models'], [other, old]);
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ count: 1, models: [fresh] }), { headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetcher);

  render(<QueryClientProvider client={client}><App><ChannelModelSyncButton channel={channel}/></App></QueryClientProvider>);
  fireEvent.click(screen.getByRole('button', { name: '同步模型' }));
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/channels/channel-a/sync-models', expect.objectContaining({ method: 'POST' }));
  await waitFor(() => expect(client.getQueryData(['admin-models'])).toEqual([other, fresh]));
  expect(fetcher.mock.calls.some(call => String(call[0]).includes('channel-b/sync-models'))).toBe(false);
  client.clear();
});
