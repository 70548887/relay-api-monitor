import '../backups.css';
import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Spin, Tag } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { NavLink, useParams } from 'react-router-dom';
import { backupAPI } from '../backupApi';
import { useSession } from '../api';
import { forgetRestore, useRestoreTracking } from '../restoreTracking';
import { time } from '../lib';

const terminal = new Set(['succeeded', 'failed', 'rolled_back']);
const phases: Record<string,string> = { accepted:'已受理', draining:'正在排空请求', stopping:'停止服务', backing_up:'保存当前安全快照', replacing:'替换恢复数据', starting:'启动并验证', committed:'数据已验证，等待解除维护', succeeded:'恢复完成', failed:'恢复未执行', rolled_back:'已恢复操作前数据', rolling_back:'恢复操作前数据', unknown:'提交结果待核对', recovery_required:'需要人工恢复' };

export function RestoreProgressPage() {
  const { id = '' } = useParams();
  const session = useSession();
  const tracking = useRestoreTracking(id);
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  useEffect(()=>{if(tracking&&tracking.expiresAt<=now)forgetRestore(id);},[id,now,tracking]);
  const token = tracking && tracking.expiresAt > now ? tracking.token : undefined;
  const canRead = /^[a-f0-9]{32}$/.test(id) && (!!token || !!session);
  const query = useQuery({ queryKey: ['restore-progress', id], queryFn: () => backupAPI.operation(id, token), enabled: canRead, retry:false, refetchInterval: value => value.state.data && terminal.has(value.state.data.state) ? false : 2000, refetchIntervalInBackground:true });
  const operation = query.data;
  const ended = !!operation && terminal.has(operation.state);
  return <main className="restore-progress-page">
    <NavLink className="restore-progress-brand" to="/">Relay Lab.</NavLink>
    <section className="panel backup-operation" aria-live="polite">
      <div className="section-toolbar"><h1>整站恢复进度</h1>{operation && <Tag color={operation.state === 'succeeded' ? 'success' : operation.state === 'failed' || operation.state === 'recovery_required' ? 'error' : 'processing'}>{phases[operation.state] || operation.state}</Tag>}</div>
      <p>此页面只查询当前恢复操作。网站重启或管理员会话失效后，仍可在本标签页等待最终结果。</p>
      <code>{id}</code>
      {!canRead && <Alert showIcon type="info" message="需要重新登录后核对结果" description="临时查询凭证只保存在本标签页内存中，刷新、关闭或凭证过期后不再保留。请重新登录后台查看最近恢复状态。" />}
      {canRead && !operation && <div className="restore-progress-wait"><Spin /><span>{query.isError ? '服务暂时无法连接，恢复结果尚未确认；请勿重复提交。' : '正在读取恢复状态…'}</span></div>}
      {canRead && query.isError && operation && <Alert showIcon type="warning" message="正在等待服务重新连接" description="下方为上次已确认状态，请等待最终结果，不要再次提交恢复。" />}
      {operation && <><p>{operation.message}</p><Descriptions column={1} size="small"><Descriptions.Item label="开始时间">{time(operation.started_at)}</Descriptions.Item><Descriptions.Item label="最近更新">{time(operation.updated_at)}</Descriptions.Item></Descriptions>
        {operation.state === 'succeeded' && <Alert showIcon type="success" message="恢复已完成" description="请使用恢复后快照中的管理员账号重新登录，并核对备份索引后再启用备份计划。" />}
        {operation.state === 'rolled_back' && <Alert showIcon type="warning" message="目标恢复未完成，已恢复操作前数据" description="请使用操作前的管理员账号登录，查看失败原因后重新预检。" />}
        {(operation.state === 'unknown' || operation.state === 'recovery_required') && <Alert showIcon type="error" message="恢复结果需要人工核对" description="请检查启动器恢复日志与当前数据状态。此页面不会再次发出恢复请求。" />}
        {operation.state === 'failed' && <Alert showIcon type="error" message="恢复未完成" description={operation.message} />}
      </>}
      <div className="backup-form-actions"><Button onClick={() => void query.refetch()} disabled={!canRead} loading={query.isFetching}>刷新状态</Button><NavLink to="/admin/login?next=%2Fadmin%2Fbackups">{ended || !canRead ? '重新登录并核对备份' : '返回登录入口'}</NavLink>{ended && token && <Button type="text" onClick={() => forgetRestore(id)}>清除本页临时查询凭证</Button>}</div>
    </section>
  </main>;
}
