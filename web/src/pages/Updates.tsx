import { useEffect, useMemo, useRef } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Descriptions, Popconfirm, Progress, Space, Table, Tag } from 'antd';
import { CheckCircleOutlined, CloudDownloadOutlined, DownloadOutlined, ReloadOutlined, RollbackOutlined } from '@ant-design/icons';
import { api, post } from '../api';
import type { UpdateAsset, UpdateHistory, UpdateOperation, UpdateSnapshot } from '../types';
import { PageHeading, QueryState, StateTag } from '../components/Common';
import { time } from '../lib';

const activeStates = new Set(['checking', 'downloading', 'draining', 'stopping', 'stopped', 'backing_up', 'backed_up', 'replacing', 'starting', 'validating', 'rolling_back', 'recovering']);
const normalizeVersion = (value?: string) => (value || '').replace(/^v/, '');
function bytes(value?: number) {
  if (value === undefined || value < 0) return '—';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}
function operationMessage(operation?: UpdateOperation | null) {
  if (!operation) return '尚未执行升级操作。';
  if (operation.message) return operation.message;
  const labels: Record<string, string> = { idle:'等待操作', checking:'正在检查 GitHub Release', available:'发现可用版本', downloading:'正在下载并校验', ready:'文件已校验，等待安装', draining:'正在排空请求', stopping:'正在停止服务', stopped:'服务已停止，准备备份', backing_up:'正在创建升级前备份', backed_up:'备份已完成，准备替换', replacing:'正在替换程序', starting:'正在启动新版本', validating:'正在验证网站与 Worker', rolling_back:'正在回滚程序和数据', recovering:'正在恢复上次升级事务', succeeded:'升级完成', failed:'升级失败', rolled_back:'已回滚', pending_restart:'已准备，等待手动重启' };
  return labels[operation.state] || operation.state || '等待操作';
}

export function UpdatesPage() {
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const baseline = useRef<string>();
  const snapshot = useQuery({ queryKey:['admin-updates'], queryFn:()=>api<UpdateSnapshot>('/admin/updates/status'), refetchInterval:2000, refetchIntervalInBackground:true, retry:false });
  const history = useQuery({ queryKey:['admin-updates-history'], queryFn:()=>api<{history:UpdateHistory[]}>('/admin/updates/history'), retry:false });
  const data = snapshot.data;
  const historyRows = data?.history ?? history.data?.history ?? [];
  const operation = data?.operation;
  const state = operation?.state || data?.state || 'idle';
  const current = data?.current;
  const info = data?.latest;
  const release = data?.release;
  const latestVersion = info?.latest_version || release?.tag_name || '';
  const latestBody = info?.notes || release?.body || '';
  const latestURL = info?.release_url || release?.url;
  const enabled = data?.enabled === true;
  const busy = activeStates.has(state);
  const managed = data?.launcher_managed === true && (data?.launcher_protocol || 0) >= 2;
  const compatibleUpdate = info?.compatible === true && info.available === true;
  const canDownload = enabled && compatibleUpdate && !busy && state !== 'ready' && state !== 'pending_restart';
  const canApply = enabled && compatibleUpdate && state === 'ready' && managed;
  const autoRestart = data?.auto_restart !== false;
  const installLabel = autoRestart ? '安装升级' : '准备升级并等待重启';
  const assets = useMemo<UpdateAsset[]>(()=>release?.assets || [], [release?.assets]);
  const action = useMutation({
    mutationFn:async(input:{path:string;accepted:string})=>({snapshot:await post<UpdateSnapshot>(input.path), accepted:input.accepted}),
    onSuccess:({snapshot:next,accepted})=>{
      if (next?.current) queryClient.setQueryData(['admin-updates'],next);
      void message.info(accepted);
      void queryClient.invalidateQueries({queryKey:['admin-updates']});
      void queryClient.invalidateQueries({queryKey:['admin-updates-history']});
    },
    onError:(error:Error)=>{ void message.error(error.message); void snapshot.refetch(); },
  });
  const run=(path:string,accepted:string)=>action.mutate({path,accepted});
  useEffect(()=>{
    if (!current) return;
    const identity = `${current.version}/${current.commit || ''}`;
    if (baseline.current === undefined) { baseline.current=identity; return; }
    const versionMatches = normalizeVersion(current.version) === normalizeVersion(operation?.version);
    const commitMatches = state === 'rolled_back' || (!!info?.commit && current.commit === info.commit);
    const verified = (state === 'succeeded' || state === 'rolled_back') && versionMatches && commitMatches && data?.maintenance?.worker_online === true;
    if (verified && baseline.current !== identity) {
      baseline.current=identity;
      const key=`relaylab-update-refresh:${operation?.id || identity}`;
      if (sessionStorage.getItem(key) !== 'done') {
        sessionStorage.setItem(key,'done');
        void queryClient.invalidateQueries();
        window.location.reload();
      }
    }
  },[current,operation,info?.commit,state,data?.maintenance?.worker_online,queryClient]);
  const confirmedSuccess = state === 'succeeded' && normalizeVersion(current?.version) === normalizeVersion(operation?.version) && !!info?.commit && current?.commit === info.commit && data?.maintenance?.worker_online === true;
  const statusMessage = state === 'succeeded' && !confirmedSuccess ? '安装结果已返回，正在确认目标版本与 Worker' : operationMessage(operation || {state});

  return <>
    <PageHeading eyebrow="SYSTEM / 在线升级" title="让服务保持最新。" description="从 GitHub Release 检查、校验并安装 Relay Lab 新版本，升级过程会保留本地数据。" extra={<Space wrap>
      <Button icon={<ReloadOutlined aria-hidden="true" spin={snapshot.isFetching}/>} onClick={()=>{void snapshot.refetch();void history.refetch();}}>刷新</Button>
      <Button type="primary" icon={<CloudDownloadOutlined aria-hidden="true"/>} disabled={!enabled || busy || action.isPending} onClick={()=>run('/admin/updates/check','检查请求已受理，请等待结果')}>检查更新</Button>
    </Space>}/>
    <QueryState loading={snapshot.isLoading} error={!data ? snapshot.error : null}>{data && <>
      {snapshot.isError && <Alert showIcon type="warning" message="与服务暂时断开，正在重新连接" description="安装和重启可能使连接短暂中断；页面会持续轮询，恢复连接后再确认结果。" className="updates-alert"/>}
      {!enabled && <Alert showIcon type="warning" message="在线升级已禁用" description="请在服务端配置 update.enabled=true 后重新启动程序。" className="updates-alert"/>}
      {!managed && <Alert showIcon type="warning" message="需要协议 2 启动器才能安装或回滚" description="升级到 v1.2.3 需先手动部署一次完整发布包，替换服务端、启动器及脚本；完成迁移后可使用在线升级。" className="updates-alert"/>}
      {state==='pending_restart' && <Alert showIcon type="info" message="升级包已准备，等待手动重启" description="自动重启已关闭。当前服务仍在运行，尚未完成新版本安装与健康验证。" className="updates-alert"/>}
      <div className="admin-stats update-stats">
        <div className="summary-card green"><span>当前版本</span><strong className="summary-value update-version">{current?.version || 'dev'}</strong><small>{current?.commit ? `Commit ${current.commit.slice(0,8)}` : '本地构建'}</small></div>
        <div className="summary-card"><span>最新版本</span><strong className="summary-value update-version">{latestVersion || '—'}</strong><small>{info?.published_at ? time(info.published_at) : '尚未检查'}</small></div>
        <div className="summary-card"><span>运行平台</span><strong className="summary-value update-platform">{current?.os || '—'} / {current?.arch || '—'}</strong><small>{current?.build_time ? `构建于 ${time(current.build_time)}` : '—'}</small></div>
        <div className="summary-card"><span>升级状态</span><strong className="worker-state update-maintenance">{statusMessage}</strong><small>{data.checked_at ? `检查于 ${time(data.checked_at)}` : '尚未检查'}</small></div>
      </div>
      <div className="updates-grid"><div className="panel update-release-panel">
        <div className="section-toolbar"><div><h2>最新 Release</h2><span className="muted">{data.repository}</span></div>{latestURL && <Button size="small" href={latestURL} target="_blank" rel="noreferrer">查看 GitHub</Button>}</div>
        {info ? <>
          <Descriptions size="small" column={1} bordered>
            <Descriptions.Item label="版本"><Tag color="cyan">{latestVersion}</Tag>{release?.name && <span className="muted">{release.name}</span>}</Descriptions.Item>
            <Descriptions.Item label="发布时间">{info.published_at ? time(info.published_at) : '—'}</Descriptions.Item>
            <Descriptions.Item label="兼容性">{info.compatible ? <Tag color="success">兼容当前平台</Tag> : <Tag color="error">当前平台不兼容</Tag>}{!info.available && <Tag>当前已是最新版本</Tag>}</Descriptions.Item>
          </Descriptions>
          {latestBody && <pre className="update-release-notes">{latestBody}</pre>}
          <div className="update-actions"><Button icon={<DownloadOutlined aria-hidden="true"/>} disabled={!canDownload || action.isPending} onClick={()=>run('/admin/updates/download','下载请求已受理，正在下载并校验')}>下载并校验</Button>
            <Popconfirm title={autoRestart?'安装该版本并重启服务？':'准备升级并等待手动重启？'} description={autoRestart?'安装期间访客请求与后台任务会进入维护排空。':'自动重启已关闭；准备完成后仍需手动重启并验证。'} okText={installLabel} cancelText="取消" disabled={!canApply || action.isPending} onConfirm={()=>run('/admin/updates/apply','安装请求已受理，请等待最终验证结果')}><Button type="primary" icon={<CheckCircleOutlined aria-hidden="true"/>} disabled={!canApply || action.isPending}>{installLabel}</Button></Popconfirm>
          </div>
        </> : <div className="update-empty">点击“检查更新”读取 GitHub Release。</div>}
      </div><div className="panel update-operation-panel">
        <div className="section-toolbar"><div><h2>升级状态</h2><span className="muted">{statusMessage}</span></div><StateTag state={state==='succeeded'&&!confirmedSuccess?'validating':state}/></div>
        {operation ? <>
          <Progress percent={Math.max(0,Math.min(100,operation.progress || 0))} status={state==='failed'?'exception':confirmedSuccess?'success':busy||state==='succeeded'?'active':'normal'}/>
          <div className="update-operation-meta"><span>操作 ID <code>{operation.id || '—'}</code></span><span>目标版本 <strong>{operation.version || latestVersion || '—'}</strong></span></div>
          {(operation.error_code || state==='failed' || state==='rolled_back') && <Alert showIcon type={state==='rolled_back'?'warning':'error'} message={operation.error_code || (state==='rolled_back'?'升级已回滚':'升级失败')} description={operation.message}/>}
          {(operation.total_bytes !== undefined || operation.bytes_downloaded !== undefined) && <div className="update-maintenance-detail"><span>已下载：{bytes(operation.bytes_downloaded)} / {bytes(operation.total_bytes)}</span>{operation.bytes_per_second !== undefined && <span>下载速度：{bytes(operation.bytes_per_second)}/s</span>}{operation.eta_seconds !== undefined && <span>预计剩余：{Math.max(0,Math.ceil(operation.eta_seconds))} 秒</span>}</div>}
          <div className="update-maintenance-detail"><span>文件：{info?.asset_name || '—'}</span><span>文件大小：{bytes(info?.asset_size)}</span><span>SHA-256：{info?.sha256 || '未提供'}</span></div>
        </> : <div className="update-empty">暂无升级操作。</div>}
        {data.maintenance && <div className="update-maintenance-detail"><span>维护排空：{data.maintenance.active?'进行中':'未开启'}</span><span>在途请求：{data.maintenance.draining_requests ?? 0}</span><span>运行任务：{data.maintenance.running_jobs ?? 0}</span><span>Worker：{data.maintenance.worker_online?'在线':'离线'}</span></div>}
      </div></div>
      <div className="panel update-assets-panel"><div className="section-toolbar"><div><h2>可用安装包</h2><span className="muted">当前 Release 的安装包与校验状态。</span></div></div><div style={{overflowX:'auto'}}><Table rowKey="name" size="small" dataSource={assets} pagination={false} locale={{emptyText:'当前 Release 没有可用安装包'}} columns={[{title:'文件',dataIndex:'name',render:(name:string,asset:UpdateAsset)=><div><strong style={{overflowWrap:'anywhere'}}>{name}</strong>{asset.os&&<div className="muted">{asset.os} / {asset.arch}</div>}</div>},{title:'大小',dataIndex:'size',render:bytes},{title:'SHA-256',dataIndex:'sha256',render:(value:string,asset:UpdateAsset)=><code className="update-sha">{value || (asset.verified?'已验证':'Manifest 未提供')}</code>},{title:'状态',render:(_:unknown,asset:UpdateAsset)=>asset.verified?<Tag color="success">已校验</Tag>:<Tag>待校验</Tag>}]}/></div></div>
      <div className="panel update-history-panel"><div className="section-toolbar"><div><h2>升级历史</h2><span className="muted">保留最近的检查、安装和回滚操作。</span></div><Popconfirm title="回滚到上一版本？" description="仅在旧版本与当前数据库兼容时执行。" okText="回滚" cancelText="取消" disabled={!enabled||!managed||busy||action.isPending} onConfirm={()=>run('/admin/updates/rollback','回滚请求已受理，请等待验证结果')}><Button danger icon={<RollbackOutlined aria-hidden="true"/>} disabled={!enabled||!managed||busy||action.isPending}>回滚</Button></Popconfirm></div><QueryState loading={data?.history === undefined && history.isLoading} error={data?.history === undefined ? history.error : null} empty={!historyRows.length}><div style={{overflowX:'auto'}}><Table rowKey={(row:UpdateHistory)=>row.id} size="small" dataSource={historyRows} pagination={{pageSize:8}} columns={[{title:'时间',dataIndex:'created_at',render:time},{title:'目标版本',dataIndex:'version',render:(value:string)=><Tag>{value || '—'}</Tag>},{title:'状态',dataIndex:'state',render:(value:string)=><StateTag state={value}/>},{title:'说明',dataIndex:'message',render:(value:string)=>value || '—'}]}/></div></QueryState></div>
    </>}</QueryState>
  </>;
}
