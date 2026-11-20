import { useQuery } from '@tanstack/react-query';
import { Segmented } from 'antd';
import { useSearchParams } from 'react-router-dom';
import { api } from '../api';
import { PageHeading } from '../components/Common';
import { FingerprintManual } from '../components/FingerprintManual';
import { FingerprintResults } from '../components/FingerprintResults';
import { bankDate, SUMMARY_ROUNDS } from '../fingerprint';
import type { FingerprintOverview } from '../types';
import '../monitor.css';
import '../fingerprint.css';

type Tab = 'results' | 'self';

export default function Fingerprint() {
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get('tab') === 'self' ? 'self' : 'results';
  const show = (next: Tab) => setParams(next === 'self' ? { tab: 'self' } : {}, { replace: true });
  const overview = useQuery({
    queryKey: ['fingerprint-overview'],
    queryFn: ({ signal }) => api<FingerprintOverview>('/public/fingerprint', { signal }),
    // The server downloads the bank on first use; keep asking until it is there.
    refetchInterval: query => query.state.data?.bank.ready ? false : 5000,
  });
  const bank = overview.data?.bank;

  return <div className="monitor-page fingerprint-page">
    <div className="monitor-heading">
      <PageHeading eyebrow="FINGERPRINT / 指纹匹对" title="指纹匹对"
        description="让模型按题目写出约 300 个整数，把数字分布和参考库里的已知模型比对，看声明的模型和实际返回的是否对得上。"
        extra={<div className="monitor-live-state">{bank?.ready
          ? <><span><i className="live"/>参考库 {bankDate(bank.built_at)} 版</span><small>收录 {bank.models} 个模型 · 来自 lm-detector</small></>
          : <><span><i className="paused"/>{overview.isPending ? '正在读取参考库' : '参考库暂不可用'}</span><small>{overview.isPending ? '请稍候' : '页面会自动重试'}</small></>}</div>}/>
    </div>
    <div className="fp-tabs">
      <Segmented<Tab> value={tab} onChange={show} options={[{ label: '平台检测结果', value: 'results' }, { label: '自助检测', value: 'self' }]}/>
      <small>{tab === 'self' ? '自助检测的结果只给你自己看，不会出现在平台检测结果里' : `结论取每个模型最近 ${SUMMARY_ROUNDS} 轮的多数结果 · 检测由后台发起`}</small>
    </div>
    {tab === 'self'
      ? <FingerprintManual models={overview.data?.models || []} ready={!!bank?.ready}/>
      : <FingerprintResults onSelf={() => show('self')}/>}
  </div>;
}
