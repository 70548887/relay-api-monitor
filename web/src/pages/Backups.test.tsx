import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App, ConfigProvider } from 'antd';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BackupsPage, backupBytes, backupCountdown } from './Backups';
import { backupAPI, defaultBackupSchedule } from '../backupApi';
import type { BackupRun, BackupSnapshot } from '../backupApi';
import { api, setSession, useSession } from '../api';
import { RestoreProgressPage } from './RestoreProgress';
import { forgetRestore } from '../restoreTracking';

const clients: QueryClient[] = [];
function Location(){const location=useLocation();const session=useSession();return <output data-testid="backup-location">{location.pathname}:{session?'signed-in':'signed-out'}</output>;}
function snapshot(): BackupSnapshot { return { schedule: { ...defaultBackupSchedule }, next_runs: [], records: [], backup_dir: 'D:/test/backups', native_restore_supported: false, deployment_kind: 'unmanaged' }; }
function run(): BackupRun { return { id: 'backup-1', request_id: 'request-1', state: 'succeeded', trigger: 'manual', created_at: '2026-10-29T02:00:00Z', started_at: '2026-10-29T02:00:00Z', heartbeat_at: '2026-10-29T02:00:10Z', version: '1.5.0', database_schema: 4, recipient_id: 'key-1', file_name: 'relaylab_test.zip.age', sha256: 'a'.repeat(64), size_bytes: 4096, transferred_bytes: 0, local_state: 'verified', remote_state: '', protected: false, monthly_archive: false, archive_retain_count: 0 }; }
function show(data: BackupSnapshot, rows: BackupRun[] = [], accept?: (url: string, init?: RequestInit) => unknown) {
  const fetcher = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url); const custom = accept?.(path, init);
    if(custom instanceof Response)return custom;
    const body = custom ?? (path.includes('/records?') ? rows : path.endsWith('/reauth') ? { ok: true } : path.endsWith('/status') ? data : { ok: true });
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetcher);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client);
  render(<QueryClientProvider client={client}><ConfigProvider theme={{token:{motion:false}}}><App><MemoryRouter initialEntries={['/admin/backups']}><Location/><Routes><Route path="/admin/backups" element={<BackupsPage/>}/><Route path="/restore-progress/:id" element={<RestoreProgressPage/>}/></Routes></MemoryRouter></App></ConfigProvider></QueryClientProvider>);
  return fetcher;
}
beforeEach(() => {
  const getStyle = window.getComputedStyle.bind(window); vi.spyOn(window, 'getComputedStyle').mockImplementation(element => getStyle(element));
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  setSession({ user: { id: 'admin-1', username: 'admin' }, csrf_token: 'csrf-backup' } as Parameters<typeof setSession>[0]);
});
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); forgetRestore('a'.repeat(32));setSession(null); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

// The real Ant Design table and confirmation dialogs are rendered together.
// Keep per-assertion waits bounded while allowing slower Windows CI render work.
describe('backup administration', { timeout: 30000 }, () => {
  it('shows a failed storage check separately from saved configuration',async()=>{
    const data=snapshot();data.storage={id:'storage',revision:1,endpoint:'https://storage.example',region:'auto',bucket:'backups',prefix:'backups',force_path_style:false,secret_configured:true,health:{status:'failed',checked_at:'2026-10-29T02:00:00Z',message:'云端读取失败'}};
    show(data);expect(await screen.findByText('云端检查失败')).toBeInTheDocument();fireEvent.click(screen.getByRole('tab',{name:'存储与恢复密钥'}));expect(await screen.findByText('云端读取失败')).toBeInTheDocument();expect(screen.getByText('最近检查失败')).toBeInTheDocument();
  });
  it('accepts the 3 GiB import contract and clears an older selection after rejection',async()=>{
    show(snapshot());fireEvent.click(await screen.findByRole('button',{name:/导入并预检/}));
    const input=screen.getByLabelText('选择备份文件');const allowed=new File(['fixture'],'allowed.zip.age');Object.defineProperty(allowed,'size',{value:2.5*1024**3});fireEvent.change(input,{target:{files:[allowed]}});expect(screen.getByRole('button',{name:'开始预检'})).toBeEnabled();
    const oversized=new File(['fixture'],'oversized.zip.age');Object.defineProperty(oversized,'size',{value:3*1024**3+1});fireEvent.change(input,{target:{files:[oversized]}});expect(await screen.findByText('备份文件超过 3 GiB 接收上限')).toBeInTheDocument();expect(screen.getByRole('button',{name:'开始预检'})).toBeDisabled();
  });
  it('keeps the administrator signed in after a wrong second password',async()=>{
    const fetcher=show(snapshot(),[run()],url=>url.endsWith('/reauth')?new Response(JSON.stringify({error:{code:'invalid_password',message:'管理员密码不正确'}}),{status:403}):undefined);
    fireEvent.click(await screen.findByRole('button',{name:/删\s*除/}));const buttons=await screen.findAllByRole('button',{name:/删\s*除/});fireEvent.click(buttons[buttons.length-1]);
    fireEvent.change(await screen.findByLabelText('管理员密码'),{target:{value:'wrong-password'}});fireEvent.click(screen.getByRole('button',{name:'验证并继续'}));
    expect(await screen.findByText('管理员密码不正确')).toBeInTheDocument();expect(screen.getByTestId('backup-location')).toHaveTextContent('/admin/backups:signed-in');expect(fetcher.mock.calls.some(([,init])=>init?.method==='DELETE')).toBe(false);
  });
  it('sends Beijing date filters to the server and resets them',async()=>{
    const fetcher=show(snapshot());await screen.findByRole('button',{name:'筛选'});
    fireEvent.change(screen.getByLabelText('开始日期 · 北京时间'),{target:{value:'2026-10-29'}});fireEvent.change(screen.getByLabelText('结束日期 · 北京时间'),{target:{value:'2026-10-30'}});fireEvent.click(screen.getByRole('button',{name:'筛选'}));
    await waitFor(()=>expect(fetcher.mock.calls.some(([url])=>String(url).includes('from='))).toBe(true));
    const filtered=new URL(String(fetcher.mock.calls.find(([url])=>String(url).includes('from='))![0]),'http://localhost');expect(filtered.searchParams.get('from')).toBe('2026-10-28T16:00:00.000Z');expect(filtered.searchParams.get('to')).toBe('2026-10-30T15:59:59.999Z');
    fireEvent.click(screen.getByRole('button',{name:'重置'}));await waitFor(()=>expect(screen.getByText('尚无备份，设置恢复密钥后可创建第一份备份')).toBeInTheDocument());
    await backupAPI.records(2,{state:'failed',trigger:'scheduled',protection:'monthly'});const filteredURL=String(fetcher.mock.calls[fetcher.mock.calls.length-1][0]);expect(filteredURL).toContain('offset=20');expect(filteredURL).toContain('state=failed');expect(filteredURL).toContain('trigger=scheduled');expect(filteredURL).toContain('protection=monthly');
  });
  it('previews affected copies and requires confirmation before a retention write',async()=>{
    const data=snapshot();data.schedule.recipient_verified=true;
    const fetcher=show(data,[],(url,init)=>url.endsWith('/retention-preview')?{count:1,local_copies:1,remote_copies:1,total_bytes:4096,items:[run()]}:url.endsWith('/schedule')&&init?.method==='PUT'?{...data.schedule,retain_days:1}:undefined);
    await screen.findByRole('tab',{name:'定时与保留'});fireEvent.click(screen.getByRole('tab',{name:'定时与保留'}));fireEvent.change(screen.getByLabelText('保留天数'),{target:{value:'1'}});fireEvent.click(screen.getByRole('button',{name:'保存策略'}));
    expect(await screen.findByText('新策略将使 1 份旧备份进入清理候选')).toBeInTheDocument();expect(fetcher.mock.calls.some(([,init])=>init?.method==='PUT')).toBe(false);
    fireEvent.click(screen.getByRole('button',{name:'确认影响并保存策略'}));await waitFor(()=>expect(fetcher.mock.calls.filter(([,init])=>init?.method==='PUT')).toHaveLength(1));const written=fetcher.mock.calls.find(([,init])=>init?.method==='PUT')!;expect(JSON.parse(String(written[1]?.body)).retain_days).toBe(1);
  });
  it('tracks an applied restore outside administrator access after the old session is revoked',async()=>{
    const data=snapshot();data.schedule.recipient_verified=true;data.native_restore_supported=true;const id='a'.repeat(32);let completed=false;let revoke=false;
    const fetcher=show(data,[run()],(url)=>url.endsWith('/restore/preflight')?{id:'preflight',manifest:{database_schema:4},expires_at:new Date(Date.now()+60000).toISOString(),native_restore_supported:true,deployment_kind:'launcher',docker_instructions:[]}:url.endsWith('/restore/apply')?{operation_id:id,status_token:'operation-only-token'}:url.endsWith('/operations/'+id)?{id,state:completed?'succeeded':'starting',message:'正在恢复'}:url.endsWith('/status')&&revoke?new Response(JSON.stringify({error:{code:'unauthorized'}}),{status:401}):undefined);
    // Scope expensive accessible-name queries to their owning row/dialog. The
    // real table, password form and modal lifecycle remain part of this test.
    const row=(await screen.findByText('relaylab_test.zip.age')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button',{name:'恢复预检'}));
    const restore=within(await screen.findByRole('dialog',{name:'恢复预检与执行'}));
    fireEvent.change(restore.getByLabelText('恢复私钥',{exact:true}),{target:{value:'transient-private-fixture'}});
    fireEvent.click(restore.getByRole('button',{name:'开始预检'}));
    const firstPassword=await screen.findByLabelText('管理员密码');
    const firstConfirmation=within(firstPassword.closest('[role="dialog"]') as HTMLElement);
    fireEvent.change(firstPassword,{target:{value:'fixture-password'}});
    fireEvent.click(firstConfirmation.getByRole('button',{name:'验证并继续'}));
    await restore.findByText('预检通过，当前数据尚未更改');
    await waitFor(()=>expect(firstPassword).not.toBeInTheDocument());
    expect(restore.queryByLabelText('恢复私钥',{exact:true})).not.toBeInTheDocument();
    const apply=restore.getByRole('button',{name:'验证密码并恢复'});
    await waitFor(()=>expect(apply).toBeEnabled());fireEvent.click(apply);
    const secondPassword=await screen.findByLabelText('管理员密码');
    const secondConfirmation=within(secondPassword.closest('[role="dialog"]') as HTMLElement);
    fireEvent.change(secondPassword,{target:{value:'fixture-password'}});
    fireEvent.click(secondConfirmation.getByRole('button',{name:'验证并继续'}));
    await waitFor(()=>expect(fetcher.mock.calls.filter(([url])=>String(url).endsWith('/restore/apply'))).toHaveLength(1));
    expect(await screen.findByText('整站恢复进度',{selector:'h1'})).toBeInTheDocument();
    revoke=true;await act(async()=>{await api('/admin/backups/status').catch(()=>null);});
    expect(screen.getByTestId('backup-location')).toHaveTextContent('/restore-progress/'+id+':signed-out');
    completed=true;await act(async()=>{await clients[clients.length-1].invalidateQueries({queryKey:['restore-progress',id]});});
    expect(await screen.findByText('恢复已完成')).toBeInTheDocument();
    const statusCalls=fetcher.mock.calls.filter(([url])=>String(url).endsWith('/operations/'+id));expect(statusCalls.length).toBeGreaterThan(0);
    expect(new Headers(statusCalls[0][1]?.headers).get('X-Restore-Status-Token')).toBe('operation-only-token');
    expect(fetcher.mock.calls.filter(([url])=>String(url).endsWith('/reauth'))).toHaveLength(2);
    expect(fetcher.mock.calls.filter(([url])=>String(url).endsWith('/restore/apply'))).toHaveLength(1);
    expect(JSON.stringify(localStorage)).not.toContain('operation-only-token');expect(JSON.stringify(sessionStorage)).not.toContain('operation-only-token');
  },30000);
  it('refreshes queued records promptly when the active snapshot already completed', async () => {
    vi.useFakeTimers({toFake:['setInterval','clearInterval']});
    const data=snapshot();data.schedule.recipient_verified=true;let reads=0;
    show(data,[],url=>url.includes('/records?') ? [++reads===1 ? {...run(),state:'queued',local_state:'pending'}:run()] :undefined);
    await screen.findByText('等待执行');
    await act(async()=>{await vi.advanceTimersByTimeAsync(2100);});
    await waitFor(()=>expect(screen.getByRole('button',{name:/下\s*载/})).toBeEnabled(),{timeout:5000});
    expect(reads).toBeGreaterThan(1);
  });
  it('requires a verified recovery key and never starts a backup when reading the page', async () => {
    const fetcher = show(snapshot());
    expect(await screen.findByText('先设置并验证恢复密钥')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /立即备份/ })).toBeDisabled();
    expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  });
  it('creates only after an explicit click with a new idempotency key', async () => {
    const data = snapshot(); data.schedule.recipient_verified = true;
    const fetcher = show(data, [], (url, init) => url.endsWith('/records') && init?.method === 'POST' ? { ...run(), state: 'queued' } : undefined);
    const create = await screen.findByRole('button', { name: /立即备份/ });
    await waitFor(() => expect(create).toBeEnabled()); fireEvent.click(create);
    await waitFor(() => expect(fetcher.mock.calls.some(([url, init]) => String(url).endsWith('/records') && init?.method === 'POST')).toBe(true));
    const posts = fetcher.mock.calls.filter(([url, init]) => String(url).endsWith('/records') && init?.method === 'POST');
    expect(posts).toHaveLength(1); expect(JSON.parse(String(posts[0][1]?.body)).request_id).toBeTruthy();
  });
  it('shows separate local and remote states without pretending upload is verified', async () => {
    const data = snapshot(); const item = { ...run(), state: 'failed', remote_state: 'failed', storage_id: 's3-1', message: '远端校验失败' };
    show(data, [item]);
    expect(await screen.findByText('relaylab_test.zip.age')).toBeInTheDocument();
    expect(screen.getByText('远端校验失败')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试上传' })).toBeEnabled();
    expect(screen.getByText('已校验')).toBeInTheDocument();
  });
  it('restores persisted operation state after reopening and blocks reconciliation while uncertain', async () => {
    const data = snapshot(); data.schedule.reconciliation_required = true; data.restore_operation = { id: 'op-1', state: 'recovery_required', message: '数据替换需要核对' };
    show(data);
    expect(await screen.findByText('需要人工恢复')).toBeInTheDocument();
    expect(screen.getByText('恢复结果需要核对，请勿重复提交')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '核对备份索引' })).toBeDisabled();
    expect(screen.getByRole('button', { name: /立即备份/ })).toBeDisabled();
  });
  it('asks for the administrator password before deleting a backup', async () => {
    const fetcher = show(snapshot(), [run()]);
    fireEvent.click(await screen.findByRole('button', { name: /删\s*除/ }));
    const confirmation = (await screen.findByText('删除这份备份及其已登记副本？')).closest('.ant-popconfirm') as HTMLElement;
    expect(confirmation).toBeTruthy();fireEvent.click(within(confirmation).getByRole('button', {name:/删\s*除/}));
    expect(await screen.findByLabelText('管理员密码')).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
    fireEvent.change(screen.getByLabelText('管理员密码'), { target: { value: 'test-password' } });
    fireEvent.click(screen.getByRole('button', { name: '验证并继续' }));
    await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true));
    const reauthIndex = fetcher.mock.calls.findIndex(([url]) => String(url).endsWith('/reauth'));
    const deleteIndex = fetcher.mock.calls.findIndex(([, init]) => init?.method === 'DELETE'); expect(reauthIndex).toBeLessThan(deleteIndex);
  },30000);
  it('imports a file with multipart CSRF without overriding its boundary', async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ id: 'preflight-1' }), { status: 200 })); vi.stubGlobal('fetch', fetcher);
    const form = new FormData(); form.append('file', new Blob(['fixture']), 'backup.zip.age'); form.append('identity', 'private-fixture');
    await backupAPI.preflight(form, 'csrf-backup');
    const [, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(init.body).toBe(form); expect(init.headers).toEqual({ 'X-CSRF-Token': 'csrf-backup' });
    expect(localStorage.length).toBe(0);
  });
  it('formats backup size and future execution time accurately', () => {
    expect(backupBytes(64 * 1024 ** 2)).toBe('64.0 MiB');
    expect(backupCountdown('2026-10-29T03:40:00Z', new Date('2026-10-29T02:00:00Z').getTime())).toBe('1 小时 40 分后');
    expect(backupCountdown('2026-10-29T02:00:00Z', new Date('2026-10-29T02:01:00Z').getTime())).toContain('已到计划时间');
  });
});
