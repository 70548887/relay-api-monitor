import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Segmented } from 'antd';
import { useSearchParams } from 'react-router-dom';
import { api } from '../api';
import { PageHeading } from '../components/Common';
import { FingerprintApi } from '../components/FingerprintApi';
import { FingerprintManual } from '../components/FingerprintManual';
import { FingerprintResults } from '../components/FingerprintResults';
import { bankDate, SUMMARY_ROUNDS } from '../fingerprint';
import type { FingerprintModel, FingerprintOverview } from '../types';
import '../monitor.css';
import '../fingerprint.css';

type Tab = 'results' | 'self';
/** The two ways to check a model oneself: paste its answers, or let the server ask it through a saved connection. */
type Way = 'manual' | 'api';

export default function Fingerprint() {
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get('tab') === 'self' ? 'self' : 'results';
  const way: Way = params.get('mode') === 'api' ? 'api' : 'manual';
  const show = (next: Tab, by: Way = 'manual') => setParams(next === 'results' ? {} : by === 'api' ? { tab: 'self', mode: 'api' } : { tab: 'self' }, { replace: true });
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
      <Segmented<Tab> value={tab} onChange={next => show(next)} options={[{ label: '平台检测结果', value: 'results' }, { label: '自助检测', value: 'self' }]}/>
      <small>{tab === 'self' ? '自助检测的结果只给你自己看，不会出现在平台检测结果里' : `结论取每个模型最近 ${SUMMARY_ROUNDS} 轮的多数结果 · 检测由后台发起`}</small>
    </div>
    {tab === 'self'
      ? <SelfCheck way={way} onWay={next => show('self', next)} models={overview.data?.models || []} ready={!!bank?.ready}/>
      : <FingerprintResults onSelf={() => show('self')}/>}
  </div>;
}

/** A way that was opened stays mounted while the other one shows, so switching loses neither pasted answers nor a result. */
function SelfCheck({ way, onWay, models, ready }: { way: Way; onWay: (way: Way) => void; models: FingerprintModel[]; ready: boolean }) {
  const [opened, setOpened] = useState<Way[]>([way]);
  if (!opened.includes(way)) setOpened([...opened, way]);
  const head = <>
    <h2>检测设置</h2>
    <p className="muted">选一种方式，把要检测的模型的回答交给本站比对。</p>
    <div className="fp-field">
      <span className="fp-field-label">检测方式</span>
      <Segmented<Way> block aria-label="检测方式" value={way} onChange={onWay} options={[{ label: '手动粘贴', value: 'manual' }, { label: '通过 API', value: 'api' }]}/>
    </div>
  </>;
  return <>
    {(way === 'manual' || opened.includes('manual')) && <FingerprintManual models={models} ready={ready} head={head} hidden={way !== 'manual'}/>}
    {(way === 'api' || opened.includes('api')) && <FingerprintApi models={models} ready={ready} head={head} hidden={way !== 'api'}/>}
  </>;
}
