import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CloudBackupBrowser } from './CloudBackupBrowser';
import type { CloudCandidate } from '../backupApi';
import { setSession } from '../api';

const first: CloudCandidate = { id: 'opaque-candidate-1', file_name: 'backup-first.zip.age', size_bytes: 8192, last_modified: '2026-10-30T02:00:00Z', status: 'pending', imported: false };
const second: CloudCandidate = { ...first, id: 'opaque-candidate-2', file_name: 'backup-second.zip.age' };
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }); }
function show(fetcher: ReturnType<typeof vi.fn>, configured = true) {
  vi.stubGlobal('fetch', fetcher);
  const imported = vi.fn(); const configure = vi.fn(); const close = vi.fn();
  render(<App><CloudBackupBrowser open storageConfigured={configured} onClose={close} onConfigure={configure} onImported={imported} /></App>);
  return { imported, configure, close };
}
async function scan() { fireEvent.click(screen.getByRole('button', { name: /扫描云端备份/ })); }
async function selectAndConfirm(identity = 'AGE-SECRET-KEY-fixture') {
  fireEvent.click((await screen.findAllByRole('button', { name: /选择导入/ }))[0]);
  fireEvent.change(screen.getByLabelText('云端备份恢复私钥'), { target: { value: identity } });
  fireEvent.click(screen.getByRole('button', { name: '验证密码并导入' }));
  fireEvent.change(await screen.findByLabelText('管理员密码'), { target: { value: 'admin-password-fixture' } });
  fireEvent.click(screen.getByRole('button', { name: '验证并导入' }));
}

beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window); vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  setSession({ user: { id: 'cloud-admin', username: 'admin' }, csrf_token: 'csrf-cloud' } as Parameters<typeof setSession>[0]);
});
afterEach(() => { cleanup(); setSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('cloud backup directory', () => {
  it('requires saved storage and explicit scanning', async () => {
    const fetcher = vi.fn(); const view = show(fetcher, false);
    expect(screen.getByRole('button', { name: /扫描云端备份/ })).toBeDisabled();
    expect(fetcher).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '配置云端存储' }));
    expect(view.configure).toHaveBeenCalledOnce(); expect(view.close).toHaveBeenCalledOnce();
  });
  it('shows empty and failed scans without automatically retrying', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], truncated: false })).mockResolvedValueOnce(response({ error: { code: 's3_unavailable', message: '云端目录暂不可读取' } }, 502));
    show(fetcher); expect(fetcher).not.toHaveBeenCalled(); await scan();
    expect(await screen.findByText('当前前缀没有可导入的备份文件')).toBeInTheDocument();
    await scan(); expect(await screen.findByText('云端目录暂不可读取')).toBeInTheDocument();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
  it('continues a paginated scan and deduplicates overlapping candidates', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [first], continuation_token: 'opaque-next-page', truncated: true })).mockResolvedValueOnce(response({ items: [first, second], truncated: false }));
    show(fetcher); await scan(); expect(await screen.findByText(first.file_name)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '继续扫描' }));
    expect(await screen.findByText(second.file_name)).toBeInTheDocument();
    expect(screen.getAllByText(first.file_name)).toHaveLength(1);
    expect(JSON.parse(String(fetcher.mock.calls[1][1].body))).toEqual({ continuation_token: 'opaque-next-page' });
    expect(new Headers(fetcher.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('csrf-cloud');
    expect(screen.queryByRole('button', { name: '继续扫描' })).not.toBeInTheDocument();
  });
  it('reauthenticates before one import and never persists the recovery key', async () => {
    let resolveImport: (value: Response) => void = () => {};
    const fetcher = vi.fn(async (url: RequestInfo | URL, _init?: RequestInit) => String(url).endsWith('/scan') ? response({ items: [first], truncated: false }) : String(url).endsWith('/reauth') ? response({ ok: true }) : new Promise<Response>(resolve => { resolveImport = resolve; }));
    const view = show(fetcher); await scan(); await screen.findByText(first.file_name); await selectAndConfirm();
    await waitFor(() => expect(fetcher.mock.calls.some(([url]) => String(url).endsWith('/import'))).toBe(true));
    const confirm = screen.getByRole('button', { name: /验证并导入$/ }); fireEvent.click(confirm); fireEvent.click(confirm);
    expect(fetcher.mock.calls.filter(([url]) => String(url).endsWith('/import'))).toHaveLength(1);
    expect(fetcher.mock.calls.map(([url]) => String(url))).toEqual(['/api/v1/admin/backups/cloud/scan', '/api/v1/admin/backups/reauth', '/api/v1/admin/backups/cloud/import']);
    const init = fetcher.mock.calls[2][1] as RequestInit;
    expect(JSON.parse(String(init.body))).toEqual({ candidate_id: first.id, identity: 'AGE-SECRET-KEY-fixture' });
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('csrf-cloud');
    await act(async () => resolveImport(response({ id: 'imported-record', file_name: first.file_name })));
    await waitFor(() => expect(view.imported).toHaveBeenCalledOnce());
    expect(screen.queryByLabelText('云端备份恢复私钥')).not.toBeInTheDocument();
    const row = screen.getAllByRole('row').find(value => value.textContent?.includes(first.file_name));
    expect(screen.getByText('已导入')).toBeInTheDocument();
    if (row) expect(within(row as HTMLElement).getByRole('button', { name: /选择导入/ })).toBeDisabled();
    expect(JSON.stringify(localStorage) + JSON.stringify(sessionStorage)).not.toContain('AGE-SECRET-KEY-fixture');
  });
  it('keeps reauthentication errors local and clears the private key after an import failure', async () => {
    let authCalls = 0;
    const fetcher = vi.fn(async (url: RequestInfo | URL) => {
      if (String(url).endsWith('/scan')) return response({ items: [first], truncated: false });
      if (String(url).endsWith('/reauth')) return ++authCalls === 1 ? response({ error: { code: 'invalid_password', message: '管理员密码不正确' } }, 403) : response({ ok: true });
      return response({ error: { code: 'invalid_backup', message: '备份校验失败' } }, 400);
    });
    show(fetcher); await scan(); await screen.findByText(first.file_name); await selectAndConfirm();
    expect(await screen.findByText('管理员密码不正确')).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([url]) => String(url).endsWith('/import'))).toBe(false);
    fireEvent.change(screen.getByLabelText('管理员密码'), { target: { value: 'correct-fixture-password' } });
    fireEvent.click(screen.getByRole('button', { name: '验证并导入' }));
    expect(await screen.findByText('备份校验失败')).toBeInTheDocument();
    expect(screen.getByLabelText('云端备份恢复私钥')).toHaveValue('');
    expect(fetcher.mock.calls.filter(([url]) => String(url).endsWith('/import'))).toHaveLength(1);
  });
});
