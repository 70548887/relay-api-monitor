import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { UpdateSnapshot } from '../types';

export function useAdminVersion() {
  return useQuery({
    queryKey: ['admin-updates'],
    queryFn: ({ signal }) => api<UpdateSnapshot>('/admin/updates/status', { signal }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    retry: false,
  });
}

function versionLabel(version: string) {
  return version === 'dev' ? 'dev' : `v${version.replace(/^v/, '')}`;
}

export function AdminVersion({ snapshot, failed }: { snapshot?: UpdateSnapshot; failed: boolean }) {
  const current = snapshot?.current?.version?.trim();
  const latest = snapshot?.latest;
  const newer = latest?.available === true && !!latest.latest_version?.trim();
  const operationFailed = snapshot?.operation?.state === 'failed' || (!snapshot?.operation && snapshot?.state === 'failed');
  const operationFailure = snapshot?.operation?.kind === 'check' ? '版本检查失败' : '更新操作失败';
  const failureLabel = failed ? '状态读取失败' : operationFailed ? operationFailure : '';
  const status = failed ? '版本状态读取失败，显示上次获取的信息，点击查看详情' : operationFailed ? `${operationFailure}，点击查看详情` : '查看版本与更新';

  return <Link to="/admin/updates" className={`admin-version-notice${newer ? ' has-update' : ''}`} title={status}
    aria-label={current ? `当前版本 ${versionLabel(current)}，${newer ? `发现新版本 ${versionLabel(latest!.latest_version)}，` : ''}${status}` : failed ? '版本信息获取失败，查看在线升级' : '正在获取版本信息，查看在线升级'}>
    <span className="admin-current-version">{current ? versionLabel(current) : failed ? '版本信息暂不可用' : '获取版本中…'}</span>
    {newer && <span className="admin-new-version"><span className="admin-version-dot" aria-hidden="true"/>新版本 {versionLabel(latest!.latest_version)}</span>}
    {failureLabel && current && <span className="admin-version-error">{failureLabel}</span>}
  </Link>;
}
