import '../backups.css';
import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { NavLink, useNavigate } from 'react-router-dom';
import { Alert, App, Button, Checkbox, Descriptions, Empty, Form, Input, InputNumber, Modal, Popconfirm, Progress, Select, Space, Steps, Switch, Table, Tabs, Tag } from 'antd';
import { CloudUploadOutlined, DatabaseOutlined, DownloadOutlined, LockOutlined, PlusOutlined, ReloadOutlined, SafetyOutlined, UploadOutlined } from '@ant-design/icons';
import { backupAPI, defaultBackupSchedule } from '../backupApi';
import type { BackupFilter, BackupRun, BackupSchedule, BackupStorageInput, RetentionPreview, RestorePreflight } from '../backupApi';
import { useSession } from '../api';
import { PageHeading, QueryState } from '../components/Common';
import { downloadText, time } from '../lib';
import { trackRestore } from '../restoreTracking';
import { CloudBackupBrowser } from '../components/CloudBackupBrowser';

const activeStates = new Set(['queued', 'snapshotting', 'validating', 'encrypting', 'local_ready', 'uploading', 'verifying_remote']);
const terminalRestoreStates = new Set(['succeeded', 'failed', 'rolled_back']);
const labels: Record<string, string> = { queued: '等待执行', snapshotting: '创建一致性快照', validating: '校验数据库', encrypting: '压缩与加密', local_ready: '本地已保存', uploading: '上传中', verifying_remote: '云端回读校验', verifying: '回读校验中', succeeded: '已完成', failed: '失败', interrupted: '已中断', canceled: '已取消', skipped: '已跳过', deleting: '删除中', delete_failed: '删除失败', deleted: '已删除', unavailable:'副本不可用', missing:'本地副本缺失', verified: '已校验', ready: '已保存', pending: '待处理', none: '未配置', not_requested: '未启用', accepted: '恢复已受理', draining: '排空请求', stopping: '停止服务', backing_up: '保存当前安全快照', replacing: '替换数据', starting: '启动并验证', validating_worker: '验证 Worker', rolled_back: '已恢复操作前数据', recovering: '恢复中', committed: '数据已验证，等待解除维护', unknown: '恢复提交结果待核对', recovery_required: '需要人工恢复' };
labels.unverified = '待重新校验';
labels.unavailable = '副本遗失已确认';
labels.missing = '本地副本缺失';
const label = (state?: string) => state ? labels[state] || state : '—';
function State({ state }: { state?: string }) { return <Tag color={state === 'succeeded' || state === 'verified' ? 'success' : state === 'failed' || state === 'delete_failed' ? 'error' : state && activeStates.has(state) ? 'processing' : 'default'}>{label(state)}</Tag>; }
export function backupBytes(n?: number) { if (n == null) return '—'; if (n < 1024) return `${n} B`; if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`; if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`; return `${(n / 1024 ** 3).toFixed(2)} GiB`; }
export function backupCountdown(value?: string, now = Date.now()) { if (!value) return '未安排'; const delta = Math.ceil((new Date(value).getTime() - now) / 1000); if (!Number.isFinite(delta)) return '未安排'; if (delta <= 0) return '已到计划时间，请查看执行状态'; return delta >= 3600 ? `${Math.floor(delta / 3600)} 小时 ${Math.floor(delta % 3600 / 60)} 分后` : delta >= 60 ? `${Math.floor(delta / 60)} 分 ${delta % 60} 秒后` : `${delta} 秒后`; }
const newRequestID = () => typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;

export function BackupsPage() {
  const { message } = App.useApp();
  const client = useQueryClient();
  const navigate = useNavigate();
  const session = useSession();
  const [tab, setTab] = useState('records');
  const [page, setPage] = useState(1);
  const [cloudOpen, setCloudOpen] = useState(false);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now());
  const [detail, setDetail] = useState<BackupRun>();
  const [sensitive, setSensitive] = useState<{ title: string; description: string; action: () => Promise<void> }>();
  const [password, setPassword] = useState('');
  const [passwordError, setPasswordError] = useState('');
  const [missingRecord, setMissingRecord] = useState<BackupRun>();
  const [missingConfirmation, setMissingConfirmation] = useState('');
  const [keyPair, setKeyPair] = useState<{ identity: string; recipient: string }>();
  const [exported, setExported] = useState(false);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restoreRecord, setRestoreRecord] = useState<BackupRun>();
  const [restoreFile, setRestoreFile] = useState<File>();
  const [identity, setIdentity] = useState('');
  const [preflight, setPreflight] = useState<RestorePreflight>();
  const [restoreError, setRestoreError] = useState('');
  const [handoff, setHandoff] = useState(false);
  const handoffRef = useRef(false);
  const [filters, setFilters] = useState<BackupFilter>({});
  const [filterForm] = Form.useForm<BackupFilter>();
  const [retentionReview,setRetentionReview]=useState<{values:BackupSchedule;preview:RetentionPreview}>();
  const [scheduleForm] = Form.useForm<BackupSchedule>();
  const [storageForm] = Form.useForm<BackupStorageInput>();
  const [recipientForm] = Form.useForm<{ recipient: string; identity: string }>();
  const [identityForm] = Form.useForm<{ identity: string }>();
  const [nextRuns, setNextRuns] = useState<string[]>();
  const draft = Form.useWatch([], scheduleForm) as Partial<BackupSchedule> | undefined;
  const snapshot = useQuery({ queryKey: ['admin-backups'], queryFn: ({signal})=>backupAPI.status(signal), enabled:!handoff, retry: false, refetchInterval: query => handoff ? false : query.state.data?.active ? 2000 : 30000, refetchIntervalInBackground: true });
  const records = useQuery({ queryKey: ['admin-backup-records', page,filters], queryFn: ({signal}) => backupAPI.records(page,filters,signal), enabled:!handoff, retry: false, refetchInterval: query => handoff ? false : snapshot.data?.active || query.state.data?.some(run => activeStates.has(run.state)) ? 2000 : 30000, refetchIntervalInBackground: true });
  const recordDetail=useQuery({queryKey:['admin-backup-detail',detail?.id],queryFn:({signal})=>backupAPI.record(detail!.id,signal),enabled:!!detail&&!handoff,retry:false,refetchInterval:query=>query.state.data&&activeStates.has(query.state.data.state)?2000:false});
  const data = snapshot.data;
  const restoreState = data?.restore_operation;
  const restorePending = !!restoreState && !terminalRestoreStates.has(restoreState.state);
  const schedule = data?.schedule || defaultBackupSchedule;
  const canCreate = schedule.recipient_verified && !schedule.reconciliation_required && !data?.active && !restorePending;
  const storageHealth = data?.storage?.health;

  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  useEffect(() => { if (data?.schedule && !scheduleForm.isFieldsTouched()) scheduleForm.setFieldsValue(data.schedule); }, [data?.schedule, scheduleForm]);
  useEffect(() => { if (data?.storage && !storageForm.isFieldsTouched()) storageForm.setFieldsValue({ ...data.storage, access_key_id: '', secret_access_key: '' }); }, [data?.storage, storageForm]);
  useEffect(() => { if (data?.schedule && !recipientForm.isFieldsTouched()) recipientForm.setFieldValue('recipient', data.schedule.recipient); }, [data?.schedule, recipientForm]);
  async function refresh() { if(handoffRef.current)return;await Promise.all([client.invalidateQueries({ queryKey: ['admin-backups'] }), client.invalidateQueries({ queryKey: ['admin-backup-records'] })]); }
  async function perform(key: string, action: () => Promise<void>) { setBusy(key); setError(''); try { await action(); await refresh(); } catch (failure) { setError((failure as Error).message); } finally { setBusy(''); } }
  function requirePassword(title: string, description: string, action: () => Promise<void>) { setPassword(''); setPasswordError(''); setSensitive({ title, description, action }); }
  async function confirmSensitive() {
    if (!sensitive || !password) return;
    setBusy('reauth'); setPasswordError('');
    try { await backupAPI.reauth(password); setPassword(''); await sensitive.action(); setSensitive(undefined); await refresh(); }
    catch (failure) { setPasswordError((failure as Error).message); }
    finally { setBusy(''); }
  }
  function openRestore(record?: BackupRun) { setRestoreRecord(record); setRestoreFile(undefined); setIdentity(''); setPreflight(undefined); setRestoreError(''); setRestoreOpen(true); }
  function closeRestore() { if (busy === 'preflight' || busy === 'reauth') return; setRestoreOpen(false); setIdentity(''); setRestoreFile(undefined); setPreflight(undefined); setRestoreError(''); }
  async function runPreflight() {
    if ((!restoreRecord && !restoreFile) || !session) return;
    setBusy('preflight'); setRestoreError('');
    try {
      const body = new FormData(); if (restoreRecord) body.append('record_id', restoreRecord.id); if (restoreFile) body.append('file', restoreFile); body.append('identity', identity);
      setPreflight(await backupAPI.preflight(body, session.csrf_token)); setIdentity('');
    } catch (failure) { setRestoreError((failure as Error).message); }
    finally { setBusy(''); }
  }
  async function commitSchedule(values:BackupSchedule){const saved=await backupAPI.saveSchedule(values);scheduleForm.resetFields();scheduleForm.setFieldsValue(saved);setNextRuns(undefined);setRetentionReview(undefined);void message.success('备份策略已保存');}
  async function saveSchedule(values: BackupSchedule) {
    const proposed={...schedule,...values};
    const changed=(['retain_days','retain_count','monthly_enabled','monthly_days','monthly_month_end','monthly_retain_count'] as const).some(key=>JSON.stringify(proposed[key])!==JSON.stringify(schedule[key]));
    if(changed){const preview=await backupAPI.retentionPreview(proposed);if(preview.count>0){setRetentionReview({values:proposed,preview});return;}}
    await commitSchedule(proposed);
  }
  function applyFilters(values:BackupFilter){
    const next={...values};
    if(values.from)next.from=new Date(`${values.from}T00:00:00+08:00`).toISOString();
    if(values.to)next.to=new Date(`${values.to}T23:59:59.999+08:00`).toISOString();
    if(next.from&&next.to&&next.from>next.to){setError('开始日期不能晚于结束日期');return;}
    setFilters(next);setPage(1);setError('');
  }
  async function submitRestore(id:string){
    handoffRef.current=true;setHandoff(true);
    await client.cancelQueries({predicate:query=>query.queryKey[0]==='session'||typeof query.queryKey[0]==='string'&&query.queryKey[0].startsWith('admin-')});
    try{const result=await backupAPI.apply(id);if(result.status_token)trackRestore(result.operation_id,result.status_token);navigate(`/restore-progress/${encodeURIComponent(result.operation_id)}`,{replace:true});}
    catch(failure){handoffRef.current=false;setHandoff(false);throw failure;}
  }
  async function saveStorage(values: BackupStorageInput) { await backupAPI.saveStorage(values); storageForm.resetFields(); void message.success('存储配置已保存，凭据只显示掩码'); }
  const hasFilters=Object.values(filters).some(Boolean);
  const rows = records.data || (!hasFilters && page === 1 ? data?.records || [] : []);
  const updating = !!busy||handoff;
  const currentDetail=recordDetail.data||detail;

  return <div className="backups-page">
    <PageHeading eyebrow="DATA / 备份与恢复" title="为每一份数据，保留恢复的余地。" description="加密保存整站数据，管理备份策略并验证恢复。仅管理员可访问。" extra={<Space wrap><Button icon={<ReloadOutlined spin={snapshot.isFetching} />} onClick={() => void refresh()}>刷新</Button><Button type="primary" icon={<PlusOutlined />} disabled={!canCreate || updating} loading={busy === 'create'} onClick={() => void perform('create', async () => { await backupAPI.create(newRequestID()); setTab('records'); void message.info('备份已受理，正在创建快照'); })}>立即备份</Button></Space>} />
    {error && <Alert showIcon type="error" message="操作未完成" description={error} closable onClose={() => setError('')} />}
    <QueryState loading={snapshot.isLoading} error={!data ? snapshot.error : null}>{data && <>
      {snapshot.isError && <Alert showIcon type="warning" message="状态暂时无法读取" description="操作可能仍在服务端进行，连接恢复后再确认结果。" />}
      {!!data.reconciliation_warnings?.length && <Alert type="warning" showIcon message="部分旧备份目录需要核对" description={<ul>{data.reconciliation_warnings.map((warning,index)=><li key={index}>{warning}</li>)}</ul>} />}
      {schedule.reconciliation_required && <Alert showIcon type="warning" message="恢复后需要核对备份索引" description="备份和自动清理已暂停。核对本地与云端副本后，仍需手动重新启用计划。未知对象不会自动删除。" action={<Button loading={busy === 'reauth'} disabled={updating || restorePending} onClick={() => requirePassword('核对备份索引', '根据本机外部目录与现有副本核对备份记录；不会删除未知对象，也不会自动开启定时计划。', async () => { await backupAPI.reconcile(); void message.success('备份索引已核对，请检查记录并按需启用计划'); })}>核对备份索引</Button>} />}
      {!schedule.recipient_verified && <Alert showIcon type="info" message="先设置并验证恢复密钥" description="导出恢复私钥并完成校验后，才能创建或启用加密备份。" action={<Button size="small" onClick={() => setTab('storage')}>设置恢复密钥</Button>} />}
      <div className="backup-summary">
        <Summary title="最近成功备份" value={data.last_success ? time(data.last_success.finished_at || data.last_success.started_at) : '尚无成功备份'} note={data.last_success ? backupBytes(data.last_success.size_bytes) : '完成校验后显示'} />
        <Summary title="下次预计执行 · 北京时间" value={schedule.enabled && schedule.next_run_at ? time(schedule.next_run_at) : '定时备份未开启'} note={schedule.enabled ? backupCountdown(schedule.next_run_at, now) : '手动备份不受此开关影响'} />
        <Summary title="当前操作" value={data.active ? label(data.active.state) : '空闲'} note={data.active ? data.active.id.slice(0, 12) : '备份与模型任务使用独立执行额度'} />
        <Summary title="存储状态" value={!data.storage ? '仅本地加密存储' : storageHealth?.status==='ok' ? '云端连接已验证' : storageHealth?.status==='failed' ? '云端检查失败' : '云端配置尚未验证'} note={data.storage ? `${data.storage.bucket} · ${storageHealth?.checked_at ? time(storageHealth.checked_at) : '请先测试连接'}` : '尚未配置云端存储'} />
      </div>
      {restoreState && <section className="panel backup-operation" aria-live="polite"><div className="section-toolbar"><h2>最近恢复操作</h2><State state={restoreState.state} /></div><p>{restoreState.message}</p><code>{restoreState.id || restoreState.operation_id}</code>{(restoreState.state==='unknown'||restoreState.state==='recovery_required')&&<Alert type="error" showIcon message="恢复结果需要核对，请勿重复提交" description="请检查启动器恢复日志与当前数据状态。"/>}<div className="backup-form-actions"><NavLink to={`/restore-progress/${restoreState.id||restoreState.operation_id}`}>打开独立恢复进度页</NavLink></div></section>}
      <section className="panel backup-main-panel"><Tabs activeKey={tab} onChange={setTab} items={[
        { key: 'records', label: '备份记录', children: <>
          <div className="backup-section-heading"><div><h2>备份记录</h2><p>本地与云端状态分别记录，上传完成后仍需回读校验。</p></div><Space wrap><Button icon={<CloudUploadOutlined />} disabled={updating} onClick={() => setCloudOpen(true)}>云端备份目录</Button><Button icon={<UploadOutlined />} disabled={updating} onClick={() => openRestore()}>导入并预检</Button></Space></div>
          <Form form={filterForm} layout="vertical" className="backup-filters" onFinish={applyFilters}>
            <Form.Item name="state" label="记录状态"><Select allowClear placeholder="全部状态" options={['queued','snapshotting','uploading','verifying_remote','succeeded','failed','interrupted','skipped','unavailable'].map(value=>({value,label:label(value)}))}/></Form.Item>
            <Form.Item name="trigger" label="触发来源"><Select allowClear placeholder="全部来源" options={[{value:'manual',label:'手动'},{value:'scheduled',label:'定时'},{value:'imported',label:'云端导入'}]}/></Form.Item>
            <Form.Item name="protection" label="保护类型"><Select allowClear placeholder="全部备份" options={[{value:'ordinary',label:'普通备份'},{value:'monthly',label:'月度归档'},{value:'protected',label:'手动保护'}]}/></Form.Item>
            <Form.Item name="from" label="开始日期 · 北京时间"><Input type="date"/></Form.Item><Form.Item name="to" label="结束日期 · 北京时间"><Input type="date"/></Form.Item>
            <div className="backup-filter-actions"><Button htmlType="submit" type="primary" aria-label="筛选">筛选</Button><Button aria-label="重置" onClick={()=>{filterForm.resetFields();setFilters({});setPage(1);}}>重置</Button></div>
          </Form>
          {records.error && <Alert type="error" showIcon message="备份记录读取失败" description={records.error.message} />}
          <Table rowKey="id" dataSource={rows} loading={records.isLoading} pagination={false} scroll={{ x: 1180 }} locale={{ emptyText: <Empty description={hasFilters?'没有符合筛选条件的备份':'尚无备份，设置恢复密钥后可创建第一份备份'} /> }} columns={[
            { title: '开始时间 / 来源', key: 'started', width: 185, render: (_, row: BackupRun) => <div><strong>{time(row.started_at)}</strong><div className="backup-row-note">{row.trigger === 'scheduled' ? '定时' : row.trigger === 'imported' ? '云端导入' : row.trigger === 'pre_restore' ? '恢复前安全快照' : '手动'} · <code>{row.id.slice(0, 8)}</code></div></div> },
            { title: '文件 / 版本', key: 'file', width: 290, render: (_, row: BackupRun) => <div className="backup-file-cell"><strong>{row.file_name || '正在准备'}</strong><div className="backup-row-note">v{row.version} · Schema {row.database_schema} · {backupBytes(row.size_bytes)}</div></div> },
            { title: '执行状态', key: 'state', width: 165, render: (_, row: BackupRun) => <><State state={row.state} />{row.transferred_bytes > 0 && row.size_bytes > 0 && activeStates.has(row.state) && <Progress size="small" percent={Math.min(100, Math.round(row.transferred_bytes / row.size_bytes * 100))} />}{row.message && row.state !== 'succeeded' && <div className={row.error_code ? 'backup-row-error' : 'backup-row-note'}>{row.message}</div>}</> },
            { title: '副本状态', key: 'copies', width: 160, render: (_, row: BackupRun) => <div className="backup-copy-states"><span>本地 <State state={row.local_state} /></span><span>云端 <State state={row.remote_state} /></span></div> },
            { title: '保护 / 到期', key: 'retention', width: 190, render: (_, row: BackupRun) => <>{row.monthly_archive && <Tag color="gold">月度归档</Tag>}{row.protected && <Tag icon={<LockOutlined />}>已保护</Tag>}<div className="backup-row-note">{row.protected || row.monthly_archive && row.archive_retain_count === 0 ? '永久保留' : row.expires_at ? time(row.expires_at) : '按保留策略清理'}</div>{row.archive_dates?.length ? <div className="backup-row-note">{row.archive_dates.join('、')}</div> : null}</> },
            { title: '操作', key: 'actions', width: 245, render: (_, row: BackupRun) => <Space size={6} wrap>
              <Button size="small" onClick={() => setDetail(row)}>详情</Button>
              <Button size="small" disabled={updating || row.local_state !== 'verified' && row.remote_state !== 'verified'} onClick={() => requirePassword('下载加密备份', '备份包含整站账号与私有数据，请验证管理员密码后下载。', async () => { const anchor = document.createElement('a'); anchor.href = backupAPI.downloadURL(row.id); anchor.download = row.file_name; anchor.click(); })}>下载</Button>
              <Button size="small" disabled={updating || row.local_state !== 'verified' && row.remote_state !== 'verified'} onClick={() => openRestore(row)}>恢复预检</Button>
              {row.storage_id && row.remote_state !== 'verified' && !activeStates.has(row.state) && <Button size="small" disabled={updating} onClick={() => void perform('retry', async () => { await backupAPI.retry(row.id); void message.info('正在重试上传原备份文件'); })}>重试上传</Button>}
              <Button size="small" disabled={updating || activeStates.has(row.state)} onClick={() => row.protected || row.monthly_archive ? requirePassword('解除备份保护', '解除后，该备份可能被保留策略清理。', async () => { await backupAPI.protect(row.id, false); }) : void perform('protect', async () => { await backupAPI.protect(row.id, true); })}>{row.protected || row.monthly_archive ? '解除保护' : '保护'}</Button>
              {row.local_state === 'missing' && !row.storage_id && <Button size="small" disabled={updating} onClick={() => { setMissingRecord(row); setMissingConfirmation(''); }}>确认副本遗失</Button>}
              <Popconfirm title={row.cloud_imported ? '删除本机导入记录及本地副本？' : '删除这份备份及其已登记副本？'} description={row.cloud_imported ? '云端来源对象将保留。已保护记录需要先解除保护。' : '此操作不能撤销。已保护备份需要先解除保护。'} disabled={updating || activeStates.has(row.state) || row.protected || row.monthly_archive} onConfirm={() => requirePassword('删除备份', row.cloud_imported ? '验证管理员密码后，仅删除本机记录及本地副本，保留云端来源。' : '验证管理员密码后，删除这份备份及其已登记副本。', async () => { await backupAPI.remove(row.id); })} okText="删除" cancelText="取消"><Button size="small" danger disabled={updating || activeStates.has(row.state) || row.protected || row.monthly_archive}>删除</Button></Popconfirm>
            </Space> },
          ]} />
          <div className="backup-pagination"><span>第 {page} 页 · 每页 20 条</span><Space><Button disabled={page === 1} onClick={() => setPage(page - 1)}>上一页</Button><Button disabled={rows.length < 20} onClick={() => setPage(page + 1)}>下一页</Button></Space></div>
        </> },
        { key: 'schedule', label: '定时与保留', children: <div className="backup-settings-grid"><Form form={scheduleForm} layout="vertical" initialValues={schedule} onFinish={values => void perform('schedule', () => saveSchedule(values))}>
          <div className="backup-section-heading"><div><h2>定时备份</h2><p>保存策略后从下一个计划时间执行；立即备份请使用顶部按钮。</p></div><Form.Item name="enabled" valuePropName="checked" noStyle><Switch aria-label="启用定时备份" disabled={!schedule.recipient_verified || schedule.reconciliation_required} /></Form.Item></div>
          <div className="backup-form-grid"><Form.Item name="mode" label="保存方式"><Select options={[{ value: 'local', label: '仅本地加密存储' }, { value: 'local_s3', label: '本地保存并上传 S3' }]} /></Form.Item><Form.Item name="timezone" label="计划时区" rules={[{ required: true }]}><Select options={[{ value: 'Asia/Shanghai', label: 'Asia/Shanghai · 北京时间' }, { value: 'UTC', label: 'UTC' }]} /></Form.Item></div>
          <Form.Item name="cron" label="Cron 表达式" rules={[{ required: true, message: '请输入五段 Cron 表达式' }]} extra="分钟 小时 日期 月份 星期。北京时间每天 02:00：0 2 * * *。"><Input /></Form.Item>
          <div className="backup-preset-actions"><Button size="small" onClick={() => scheduleForm.setFieldValue('cron', '0 2 * * *')}>每天 02:00</Button><Button size="small" onClick={() => scheduleForm.setFieldValue('cron', '0 2 * * 0')}>周日 02:00</Button><Button size="small" loading={busy === 'preview'} onClick={() => void perform('preview', async () => { const values = await scheduleForm.validateFields(['cron', 'timezone']); const result = await backupAPI.preview({ ...schedule, ...scheduleForm.getFieldsValue(true), ...values }); setNextRuns(result.next_runs); })}>预览下三次执行</Button></div>
          {(nextRuns || data.next_runs)?.length ? <div className="backup-next-runs" aria-label="未来三次执行时间">{(nextRuns || data.next_runs)!.map(value => <span key={value}>{time(value)} <small>北京时间</small></span>)}</div> : null}
          <h3 className="backup-section-divider">普通备份保留</h3><div className="backup-form-grid"><Form.Item name="retain_days" label="保留天数" extra="0 = 不按天数清理" rules={[{ required: true }]}><InputNumber min={0} max={3650} /></Form.Item><Form.Item name="retain_count" label="最大保留份数" extra="0 = 不按份数清理" rules={[{ required: true }]}><InputNumber min={0} max={10000} /></Form.Item></div>
          <div className="backup-section-heading backup-section-divider"><h3>月度归档</h3><Form.Item name="monthly_enabled" valuePropName="checked" noStyle><Switch aria-label="启用月度归档" /></Form.Item></div>
          <Form.Item name="monthly_days" label="归档日期" extra="不存在的日期按当月最后一天处理；当天失败可顺延至本月下一份成功定时备份。"><Select mode="multiple" placeholder="选择日期" options={Array.from({ length: 31 }, (_, i) => ({ value: i + 1, label: `${i + 1} 日` }))} /></Form.Item><Form.Item name="monthly_month_end" valuePropName="checked"><Checkbox>同时归档月末备份</Checkbox></Form.Item>
          <Form.Item name="monthly_retain_count" label="归档最大保留份数" extra="0 = 永久保留；已有永久归档不会因修改策略自动解除保护。"><InputNumber min={0} max={10000} /></Form.Item>
          <div className="backup-form-actions"><Button type="primary" htmlType="submit" loading={busy === 'schedule'} disabled={updating}>保存策略</Button></div>
        </Form><aside className="backup-settings-aside"><SafetyOutlined /><h3>保留策略摘要</h3><p>{draft?.enabled ? '定时备份已选择开启，保存后生效。' : '定时备份关闭，可随时手动创建。'}</p><p>{(draft?.retain_days ?? schedule.retain_days) === 0 ? '不按天数清理' : `保留 ${draft?.retain_days ?? schedule.retain_days} 天内备份`}；{(draft?.retain_count ?? schedule.retain_count) === 0 ? '不限制普通份数' : `最多 ${draft?.retain_count ?? schedule.retain_count} 份普通备份`}。</p><p>达到任一条件时清理旧普通备份，至少保留最后一个已验证恢复点。</p><p>月度归档和手动保护独立计算，不占普通份数。</p><span>预计触发时间不承诺实际开始时间。</span></aside></div> },
        { key: 'storage', label: '存储与恢复密钥', children: <div className="backup-storage-stack">
          <section><div className="backup-section-heading"><div><h2><DatabaseOutlined /> 本地存储</h2><p>快照暂存与加密文件位于持久目录，不依赖容器 /tmp。</p></div><Tag>加密归档</Tag></div><code className="backup-path">{data.backup_dir || '由服务端指定的备份目录'}</code></section>
          <section><div className="backup-section-heading"><div><h2><CloudUploadOutlined /> S3 兼容存储</h2><p>支持 R2 等兼容服务。保存凭据后仅显示掩码。</p></div>{data.storage && <Tag color={storageHealth?.status==='ok'?'success':storageHealth?.status==='failed'?'error':'default'}>{storageHealth?.status==='ok'?'连接已验证':storageHealth?.status==='failed'?'最近检查失败':'已配置 · 尚未验证'}</Tag>}</div>{storageHealth?.message&&<Alert type={storageHealth.status==='failed'?'warning':'info'} showIcon message={storageHealth.message} description={storageHealth.checked_at?`最近检查：${time(storageHealth.checked_at)}`:undefined}/>}<Form form={storageForm} layout="vertical" initialValues={{ region: 'auto', prefix: 'backups/', force_path_style: false }} onFinish={values => void perform('storage', () => saveStorage(values))}>
            <div className="backup-form-grid"><Form.Item name="endpoint" label="端点地址" rules={[{ required: true, message: '请输入 S3 端点地址' }, { type: 'url', message: '请输入完整 URL' }]}><Input placeholder="https://<account-id>.r2.cloudflarestorage.com" autoComplete="off" /></Form.Item><Form.Item name="region" label="区域" rules={[{ required: true }]}><Input placeholder="R2 使用 auto" /></Form.Item><Form.Item name="bucket" label="存储桶" rules={[{ required: true }]}><Input placeholder="relaylab-backups" /></Form.Item><Form.Item name="prefix" label="对象 Key 前缀"><Input placeholder="backups/" /></Form.Item><Form.Item name="access_key_id" label="Access Key ID" extra={data.storage?.access_key_mask ? `当前 ${data.storage.access_key_mask}；留空保留。` : '首次配置需填写。'}><Input.Password autoComplete="new-password" placeholder={data.storage ? '留空保留当前 Access Key' : '输入 Access Key ID'} /></Form.Item><Form.Item name="secret_access_key" label="Secret Access Key" extra={data.storage?.secret_configured ? '已加密保存；留空保留。' : '首次配置需填写。'}><Input.Password autoComplete="new-password" placeholder="留空保留，填写则替换" /></Form.Item></div>
            <Form.Item name="force_path_style" valuePropName="checked"><Checkbox>使用路径风格寻址</Checkbox></Form.Item><p className="backup-help">更换端点身份时需要重新输入凭据。连接测试会创建、读取并删除一个专用测试对象。</p><div className="backup-form-actions"><Button disabled={updating} loading={busy === 'test-storage'} onClick={() => void perform('test-storage', async () => { const values = await storageForm.validateFields(); await backupAPI.testStorage(values); void message.success('对象写入、读取和删除验证已通过'); })}>测试连接</Button><Button type="primary" htmlType="submit" disabled={updating} loading={busy === 'storage'}>保存存储配置</Button></div>
          </Form></section>
          <section><div className="backup-section-heading"><div><h2><LockOutlined /> 独立恢复密钥</h2><p>服务端只保留公钥，恢复私钥由您离线保管。丢失私钥将无法解密对应备份。</p></div><Tag color={schedule.recipient_verified ? 'success' : 'warning'}>{schedule.recipient_verified ? '已验证' : '待设置 / 校验'}</Tag></div>
            <Form form={recipientForm} layout="vertical" onFinish={values => void perform('recipient', async () => { await backupAPI.setRecipient(values.recipient); recipientForm.setFieldValue('identity', ''); void message.success('恢复公钥已保存，请提供私钥完成匹配校验'); })}><Form.Item name="recipient" label="恢复公钥" rules={[{ required: true, message: '请输入 age 恢复公钥' }]} extra={schedule.recipient_id ? `公钥标识：${schedule.recipient_id}` : '以 age1 开头的公钥。'}><Input autoComplete="off" placeholder="age1…" /></Form.Item><div className="backup-form-actions"><Button type="primary" htmlType="submit" disabled={updating}>保存公钥</Button><Button disabled={updating} onClick={() => requirePassword('生成新的恢复密钥', '新私钥仅在本次弹窗显示。更换公钥后，旧备份仍需要原来的私钥。', async () => { setKeyPair(await backupAPI.generateKey()); setExported(false); })}>生成并导出新密钥</Button></div></Form>
            <Form layout="vertical" onFinish={values => void perform('verify-key', async () => { try { await backupAPI.verifyIdentity(values.identity); void message.success('恢复密钥匹配验证通过'); } finally { identityForm.resetFields(); } })} form={identityForm}><Form.Item name="identity" label="校验恢复私钥" rules={[{ required: true, message: '请输入恢复私钥进行匹配校验' }]} extra="仅在本次请求中使用，不持久保存。"><Input.Password autoComplete="new-password" placeholder="AGE-SECRET-KEY-…" /></Form.Item><Button htmlType="submit" loading={busy === 'verify-key'} disabled={updating || !schedule.recipient}>验证私钥匹配</Button></Form>
          </section>
        </div> },
      ]} /></section>
    </>}</QueryState>
    <CloudBackupBrowser open={cloudOpen} storageConfigured={!!data?.storage} disabled={updating || restorePending} onClose={() => setCloudOpen(false)} onConfigure={() => setTab('storage')} onImported={refresh} />
    <Modal title={sensitive?.title || '验证管理员密码'} open={!!sensitive} onCancel={() => { if (!updating) { setSensitive(undefined); setPassword(''); setPasswordError(''); } }} onOk={() => void confirmSensitive()} okText="验证并继续" cancelText="取消" confirmLoading={busy === 'reauth'} okButtonProps={{ disabled: !password }} destroyOnClose afterClose={() => { setPassword(''); setPasswordError(''); }}><p>{sensitive?.description}</p>{passwordError && <Alert type="error" showIcon message={passwordError} />}<label htmlFor="backup-admin-password">管理员密码</label><Input.Password id="backup-admin-password" value={password} onChange={event => setPassword(event.target.value)} onPressEnter={() => void confirmSensitive()} autoComplete="current-password" /></Modal>
    <Modal title="确认本地副本遗失" open={!!missingRecord} onCancel={() => { setMissingRecord(undefined); setMissingConfirmation(''); }} okText="验证密码并确认" cancelText="取消" okButtonProps={{ disabled: missingConfirmation !== '确认副本遗失' }} onOk={() => { if (!missingRecord) return; const selected = missingRecord; setMissingRecord(undefined); setMissingConfirmation(''); requirePassword('确认副本遗失', '只更新这份纯本地备份的遗失状态，不重建文件，不删除任何云端对象。确认后记录仍保留。', async () => { await backupAPI.acknowledgeMissing(selected.id); }); }} destroyOnClose><Alert showIcon type="warning" message="这份备份的本地文件已不存在" description="此操作不会重建丢失备份，不会删除任何云端对象。记录将标为不可用并保留；之后可重新核对索引。" /><p className="backup-file-cell">{missingRecord?.file_name}</p><label htmlFor="backup-missing-confirmation">输入“确认副本遗失”以继续</label><Input id="backup-missing-confirmation" value={missingConfirmation} onChange={event => setMissingConfirmation(event.target.value)} /></Modal>
    <Modal title="导出恢复私钥" open={!!keyPair} width={620} onCancel={() => { setKeyPair(undefined); setExported(false); }} footer={<Space wrap><Button onClick={() => { setKeyPair(undefined); setExported(false); }}>取消</Button><Button icon={<DownloadOutlined />} onClick={() => { if (keyPair) downloadText(keyPair.identity, 'relaylab-backup-recovery.key'); }}>下载私钥</Button><Button type="primary" disabled={!exported || updating} loading={busy === 'save-key'} onClick={() => void perform('save-key', async () => { if (!keyPair) return; await backupAPI.setRecipient(keyPair.recipient); await backupAPI.verifyIdentity(keyPair.identity); recipientForm.setFieldValue('recipient', keyPair.recipient); setKeyPair(undefined); setExported(false); void message.success('恢复密钥已设置并验证'); })}>已保管，启用此公钥</Button></Space>} destroyOnClose>
      <Alert showIcon type="warning" message="私钥仅在本次显示" description="请下载到独立、安全的位置。私钥不随备份保存；原服务器丢失后仍需要它才能恢复。" /><Form layout="vertical"><Form.Item label="恢复公钥" htmlFor="backup-generated-recipient"><Input id="backup-generated-recipient" value={keyPair?.recipient || ''} readOnly /></Form.Item><Form.Item label="恢复私钥" htmlFor="backup-generated-identity"><Input.Password id="backup-generated-identity" value={keyPair?.identity || ''} readOnly autoComplete="off" /></Form.Item></Form><Checkbox checked={exported} onChange={event => setExported(event.target.checked)}>我已将私钥下载并独立保管</Checkbox>
    </Modal>
    <Modal title="恢复预检与执行" open={restoreOpen} width={780} onCancel={closeRestore} footer={null} destroyOnClose><div className="backup-restore"><Steps size="small" current={preflight ? 1 : 0} items={[{ title: '选择并验证备份' }, { title: '核对影响' }, { title: '恢复与检查' }]} /><Alert showIcon type="warning" message="恢复会将整站数据回退到所选快照" description="先完成预检，原生恢复执行前会创建当前数据的安全快照。Docker 使用新空数据卷恢复。" />
      {!preflight ? <><Form layout="vertical"><Form.Item label="待恢复备份">{restoreRecord ? <div className="backup-selection"><strong>{restoreRecord.file_name}</strong><span>{time(restoreRecord.started_at)} · {backupBytes(restoreRecord.size_bytes)}</span><Button size="small" onClick={() => { setRestoreRecord(undefined); setRestoreFile(undefined); }}>改为导入文件</Button></div> : <input aria-label="选择备份文件" type="file" accept=".zip,.age" onChange={event => { const file = event.target.files?.[0]; if (file && file.size > 3 * 1024 ** 3) { setRestoreError('备份文件超过 3 GiB 接收上限'); setRestoreFile(undefined); event.target.value = ''; return; } setRestoreFile(file); setRestoreError(''); }} />}</Form.Item><Form.Item label="恢复私钥" htmlFor="backup-restore-identity" extra="加密包需要匹配私钥；旧版未加密 ZIP 可留空。私钥仅在此次预检中使用。"><Input.Password id="backup-restore-identity" value={identity} onChange={event => setIdentity(event.target.value)} autoComplete="new-password" placeholder="AGE-SECRET-KEY-…" /></Form.Item></Form>{restoreError && <Alert type="error" showIcon message="预检未通过" description={restoreError} />}<div className="backup-form-actions"><Button onClick={closeRestore}>取消</Button><Button type="primary" disabled={(!restoreRecord && !restoreFile) || updating} loading={busy === 'preflight'} onClick={() => requirePassword('验证并预检备份', '预检需要临时解密整站数据，请验证管理员密码。', runPreflight)}>开始预检</Button></div></> : <>
        <Alert type="success" showIcon message="预检通过，当前数据尚未更改" /><Descriptions column={1} size="small" bordered><Descriptions.Item label="快照版本">{preflight.manifest.app_version || preflight.manifest.version || '旧版备份'}</Descriptions.Item><Descriptions.Item label="数据库版本">Schema {preflight.manifest.database_schema ?? '旧格式，服务端已校验'}</Descriptions.Item><Descriptions.Item label="预检有效期">{time(preflight.expires_at)}</Descriptions.Item><Descriptions.Item label="部署方式">{preflight.deployment_kind === 'docker' ? 'Docker / 宝塔' : '原生部署'}</Descriptions.Item></Descriptions>
        <p>恢复后当前登录会失效；旧任务队列不会重放，备份清理暂停，待索引核对后再启用。</p>{!preflight.native_restore_supported && <Alert type="info" showIcon message={preflight.deployment_kind === 'docker' ? 'Docker 使用新数据卷恢复' : '当前启动器不支持在线恢复'} description="请按照下方离线步骤操作；原有数据保留用于返回。" />}{preflight.docker_instructions?.length ? <pre className="backup-instructions">{preflight.docker_instructions.join('\n')}</pre> : null}
        <div className="backup-form-actions"><Button onClick={() => { setPreflight(undefined); setIdentity(''); }}>重新选择</Button><Button type="primary" danger disabled={!preflight.native_restore_supported || updating || restorePending || new Date(preflight.expires_at).getTime() <= now} onClick={() => requirePassword('确认恢复整站数据', '将停止网站与 Worker，恢复到已预检的快照。请确认已理解数据回退影响。', async () => { await submitRestore(preflight.id); })}>验证密码并恢复</Button></div>
      </>}
    </div></Modal>
    <Modal title="保留策略清理影响" open={!!retentionReview} onCancel={()=>setRetentionReview(undefined)} okText="确认影响并保存策略" cancelText="保留原策略" confirmLoading={busy==='schedule'} onOk={()=>retentionReview&&void perform('schedule',()=>commitSchedule(retentionReview.values))} width={740} destroyOnClose>
      {retentionReview&&<><Alert showIcon type="warning" message={`新策略将使 ${retentionReview.preview.count} 份旧备份进入清理候选`} description="保存策略不会立即删除文件；后续成功备份或清理周期将按新策略执行。请确认这些副本不再需要。永久保护及最后恢复点不受普通清理影响。"/><p>本地副本 {retentionReview.preview.local_copies} 份，云端副本 {retentionReview.preview.remote_copies} 份，候选总大小 {backupBytes(retentionReview.preview.total_bytes)}。</p><Table rowKey="id" size="small" pagination={{pageSize:5}} dataSource={retentionReview.preview.items} columns={[{title:'候选备份',dataIndex:'file_name',render:value=><span className="backup-file-cell">{value}</span>},{title:'开始时间',dataIndex:'started_at',render:value=>time(value)},{title:'大小',dataIndex:'size_bytes',render:value=>backupBytes(value)}]}/></>}
    </Modal>
    <Modal title="备份详情" open={!!detail} onCancel={() => setDetail(undefined)} footer={<Button onClick={() => setDetail(undefined)}>关闭</Button>} width={720} destroyOnClose>
      {recordDetail.isError&&<Alert type="warning" showIcon message="详情暂时无法刷新" description={recordDetail.error.message}/>}
      {currentDetail&&<Descriptions column={1} bordered size="small"><Descriptions.Item label="文件">{currentDetail.file_name || '—'}</Descriptions.Item><Descriptions.Item label="当前阶段"><State state={currentDetail.state} /></Descriptions.Item><Descriptions.Item label="开始 / 完成">{time(currentDetail.started_at)} / {time(currentDetail.finished_at)}</Descriptions.Item><Descriptions.Item label="执行耗时">{Number.isFinite(Date.parse(currentDetail.started_at))?`${Math.max(0,(Date.parse(currentDetail.finished_at||new Date(now).toISOString())-Date.parse(currentDetail.started_at))/1000).toFixed(1)} 秒`:'—'}</Descriptions.Item><Descriptions.Item label="传输字节">{backupBytes(currentDetail.transferred_bytes)} / {backupBytes(currentDetail.size_bytes)}</Descriptions.Item><Descriptions.Item label="程序 / 数据库">v{currentDetail.version} / Schema {currentDetail.database_schema}</Descriptions.Item><Descriptions.Item label="本地 / 云端"><State state={currentDetail.local_state} /><State state={currentDetail.remote_state} /></Descriptions.Item><Descriptions.Item label="SHA-256"><code className="backup-hash">{currentDetail.sha256 || '尚未生成'}</code></Descriptions.Item><Descriptions.Item label="恢复公钥标识">{currentDetail.recipient_id || '—'}</Descriptions.Item><Descriptions.Item label="说明">{currentDetail.message || '—'}</Descriptions.Item></Descriptions>}
    </Modal>
  </div>;
}

function Summary({ title, value, note }: { title: string; value: string; note: string }) { return <div className="backup-summary-card"><span>{title}</span><strong>{value}</strong><small>{note}</small></div>; }
