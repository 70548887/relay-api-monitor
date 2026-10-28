import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Alert, Button, Empty, Input, Modal, Space, Switch, Table, Tag, Tooltip } from 'antd';
import { ClockCircleOutlined, DownOutlined, HistoryOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { api } from '../api';
import type { ModelView, ProbeResult } from '../types';
import type { MonitorData } from '../types';
import { duration, nameOf, time } from '../lib';
import { PageHeading, QueryState, StateTag } from '../components/Common';
import '../monitor.css';

type Counts={ok:number;slow:number;failed:number;unknown:number;disabled:number;stale:number};
function countModels(models:ModelView[]):Counts{
 const counts:Counts={ok:0,slow:0,failed:0,unknown:0,disabled:0,stale:0};
 for(const model of models){counts[model.status]++;if(model.stale&&model.last_probe)counts.stale++}
 return counts;
}
function abnormalModel(model:ModelView){return model.status==='failed'||model.status==='slow'||(model.stale&&!!model.last_probe)}
function describeCounts(counts:Counts){return ([['ok','正常'],['slow','偏慢'],['failed','失败'],['unknown','待观测'],['disabled','已停用']] as const).filter(([key])=>counts[key]>0).map(([key,label])=>`${counts[key]} ${label}`).join(' · ')||'暂无公开模型'}
function protocolLabel(protocol?:string){return ({chat:'Chat Completions',responses:'Responses',anthropic:'Anthropic'} as Record<string,string>)[protocol||'']||protocol||'继承分组'}
function relativeTime(value:string,now:number){const seconds=Math.max(0,Math.floor((now-Date.parse(value))/1000));if(!Number.isFinite(seconds))return '时间未提供';if(seconds<60)return '不到 1 分钟前';if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;if(seconds<86400)return `${Math.floor(seconds/3600)} 小时前`;return `${Math.floor(seconds/86400)} 天前`}
function probeReason(probe?:ProbeResult|null){
 if(!probe)return '尚未产生检测样本';
 if(probe.status!=='failed')return probe.returned_model||probe.finish_reason||'已完成检测';
 if(probe.http_status===429)return '上游限流 · HTTP 429';
 if(probe.http_status===401||probe.http_status===403)return `鉴权失败 · HTTP ${probe.http_status}`;
 if(probe.error_code?.includes('timeout'))return '上游请求超时';
 if(probe.http_status>=500)return `上游服务错误 · HTTP ${probe.http_status}`;
 if(probe.error_code==='truncated')return '上游输出不完整';
 return probe.error_message||'上游检测失败';
}

export default function Monitor(){
 const [automatic,setAutomatic]=useState(true);
 const [search,setSearch]=useState('');
 const [abnormal,setAbnormal]=useState(false);
 const [platformId,setPlatformId]=useState<string>();
 const [collapsed,setCollapsed]=useState<Set<string>>(()=>new Set());
 const [historyId,setHistoryId]=useState<string>();
 const [now,setNow]=useState(Date.now());
 const query=useQuery({queryKey:['monitor'],queryFn:({signal})=>api<MonitorData>('/public/monitor',{signal}),refetchInterval:automatic?30000:false});
 const allModels=query.data?.models||[];
 const platforms=query.data?.platforms||[];
 const channels=query.data?.channels||[];
 const counts=countModels(allModels);
 const normalizedSearch=search.trim().toLowerCase();
 const filtering=!!normalizedSearch||abnormal;
 const matchedModels=useMemo(()=>allModels.filter(model=>(!normalizedSearch||`${nameOf(model)} ${model.upstream_id} ${model.platform_name} ${model.channel_name}`.toLowerCase().includes(normalizedSearch))&&(!abnormal||abnormalModel(model))),[allModels,normalizedSearch,abnormal]);
 const eligiblePlatforms=filtering?platforms.filter(platform=>matchedModels.some(model=>model.platform_id===platform.id)):platforms;
 const currentPlatform=eligiblePlatforms.find(platform=>platform.id===platformId)||eligiblePlatforms[0];
 const visibleModels=matchedModels.filter(model=>model.platform_id===currentPlatform?.id);
 const visibleChannels=channels.filter(channel=>channel.platform_id===currentPlatform?.id&&(!filtering||visibleModels.some(model=>model.channel_id===channel.id)));
 const platformModels=allModels.filter(model=>model.platform_id===currentPlatform?.id);
 const platformCounts=countModels(platformModels);
 const allCollapsed=visibleChannels.length>0&&visibleChannels.every(channel=>collapsed.has(channel.id));
 const history=allModels.find(model=>model.id===historyId);
 const historyQuery=useQuery({queryKey:['history',historyId],queryFn:({signal})=>api<ProbeResult[]>(`/public/models/${encodeURIComponent(historyId!)}/history`,{signal}),enabled:!!history});
 useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[]);
 useEffect(()=>{if(query.data&&historyId&&!query.data.models.some(model=>model.id===historyId))setHistoryId(undefined)},[query.data,historyId]);
 const toggleChannel=(id:string)=>setCollapsed(previous=>{const next=new Set(previous);if(next.has(id))next.delete(id);else next.add(id);return next});
 const toggleAll=()=>setCollapsed(previous=>{const next=new Set(previous);for(const channel of visibleChannels){if(allCollapsed)next.delete(channel.id);else next.add(channel.id)}return next});
 const clearFilters=()=>{setSearch('');setAbnormal(false)};
 const openHistory=(model:ModelView)=>setHistoryId(model.id);
 const columns=[
  {title:'模型',key:'model',width:192,render:(_:unknown,model:ModelView)=><ModelName model={model}/>},
  {title:'当前状态',key:'status',width:112,render:(_:unknown,model:ModelView)=><ModelStatus model={model}/>},
  {title:<Tooltip title="每格代表北京时间的一个小时，展示该小时最严重状态；当前小时可能尚未结束。无样本不填充旧结果。">近 24 小时</Tooltip>,key:'timeline',width:247,render:(_:unknown,model:ModelView)=><ModelTimeline model={model}/>},
  {title:'近 7 天成功率',key:'rate',width:126,render:(_:unknown,model:ModelView)=><SuccessRate model={model}/>},
  {title:<Tooltip title="从发出请求到收到首段有效正文，不包含心跳、思考内容和工具调用。">首段正文</Tooltip>,key:'first',width:99,render:(_:unknown,model:ModelView)=><span className={`mono ${model.status==='slow'?'warning-text':''}`}>{duration(model.last_probe?.first_text_ms)}</span>},
  {title:'总耗时',key:'total',width:94,render:(_:unknown,model:ModelView)=><span className="mono">{duration(model.last_probe?.duration_ms)}</span>},
  {title:'最近检测',key:'last',width:170,render:(_:unknown,model:ModelView)=><LastProbe model={model} now={now}/>},
  {title:'',key:'action',width:64,render:(_:unknown,model:ModelView)=><Button size="small" type="text" aria-label={`查看 ${nameOf(model)} 检测历史`} icon={<HistoryOutlined aria-hidden="true"/>} onClick={()=>openHistory(model)}>历史</Button>},
 ];
 const totals=query.data?[
  {label:'观测范围',value:platforms.length,caption:`平台 · ${channels.length} 个公开分组`,tone:'range'},
  {label:'公开模型',value:allModels.length,caption:'同一模型按分组分别统计',tone:'neutral'},
  {label:'正常',value:counts.ok,caption:'已收到有效完整响应',tone:'ok'},
  {label:'偏慢',value:counts.slow,caption:'首段正文超过设定阈值',tone:'slow'},
  {label:'失败',value:counts.failed,caption:'查看最近一次失败原因',tone:'failed'},
  {label:'待观测 / 停用',value:counts.unknown+counts.disabled,caption:`${counts.unknown} 待观测 · ${counts.disabled} 已停用`,tone:'idle'},
 ]:null;
 const needsAttention=counts.failed+counts.slow+counts.stale>0;

 return <div className="monitor-page">
  <div className="monitor-heading"><PageHeading eyebrow="OBSERVABILITY / 实时观测" title="中转 API 监控" description="一屏查看平台、分组与模型状态，让每一次真实检测都有迹可循。" extra={<div className="monitor-live-state"><span><i className={automatic&&!query.error?'live':'paused'}/>{query.error?'最近刷新失败':automatic?'页面每 30 秒刷新':'自动刷新已暂停'}</span><small>状态快照 {time(query.data?.updated_at)}</small></div>}/></div>
  <section className="monitor-summary" aria-label="监控总览">{(totals||['观测范围','公开模型','正常','偏慢','失败','待观测 / 停用'].map(label=>({label,value:'—',caption:'正在读取监控数据',tone:label==='观测范围'?'range':'neutral'}))).map(card=><div className={`monitor-summary-card ${card.tone}`} key={card.label}><span className="label">{card.label}</span><strong className="value">{card.value}</strong><span className="caption">{card.caption}</span></div>)}</section>
  {query.error&&query.data&&<Alert className="monitor-attention" type="warning" showIcon message="监控刷新失败，暂时保留上次快照" description={`${query.error.message} · 上次更新 ${time(query.data.updated_at)}`} action={<Button size="small" onClick={()=>void query.refetch()}>重试</Button>}/>}
  {needsAttention&&<div className="monitor-attention"><div><strong>需要关注</strong><span>{counts.failed} 个失败 · {counts.slow} 个偏慢</span><span>另有 {counts.stale} 个模型数据过期</span><small>保留上次结果，请结合最近检测时间判断。</small></div><Button type="link" onClick={()=>setAbnormal(value=>!value)}>{abnormal?'显示全部模型':'查看异常模型'}</Button></div>}
  <section className="monitor-workspace panel" aria-label="平台运行状态">
   <div className="monitor-toolbar"><h2>平台运行状态 <Tag>{platforms.length} 个平台</Tag></h2><div className="monitor-toolbar-controls"><Input value={search} allowClear onChange={event=>setSearch(event.target.value)} prefix={<SearchOutlined aria-hidden="true"/>} placeholder="搜索平台 / 分组 / 模型" aria-label="搜索平台、分组或模型"/><Space><Switch size="small" checked={abnormal} onChange={setAbnormal} aria-label="仅看异常"/><span>仅看异常</span></Space><Button onClick={toggleAll} disabled={!visibleChannels.length}>{allCollapsed?'展开全部':'折叠全部'}</Button><Space><Switch size="small" checked={automatic} onChange={setAutomatic} aria-label="自动刷新"/><span>自动刷新</span></Space><Button aria-label="刷新监控" icon={<ReloadOutlined aria-hidden="true" spin={query.isFetching}/>} onClick={()=>void query.refetch()}>刷新</Button></div></div>
   <QueryState loading={query.isLoading} error={query.data?null:query.error}>
    {platforms.length?<div className="monitor-layout">
     <div className="monitor-platforms" role="group" aria-label="平台导航"><div className="monitor-platforms-label"><span>平台</span><span>分组</span></div>{platforms.map(platform=>{const models=allModels.filter(model=>model.platform_id===platform.id);const hasMatch=matchedModels.some(model=>model.platform_id===platform.id);return <button type="button" className={`monitor-platform-button ${platform.id===currentPlatform?.id?'active':''}`} key={platform.id} aria-label={platform.name} aria-pressed={platform.id===currentPlatform?.id} disabled={filtering&&!hasMatch} onClick={()=>setPlatformId(platform.id)}><span className="platform-title"><strong>{platform.name}</strong><span className="platform-group-count" title="公开分组数量">{channels.filter(channel=>channel.platform_id===platform.id).length}</span></span><span className="platform-status-summary">{describeCounts(countModels(models))}</span>{filtering&&<small>{matchedModels.filter(model=>model.platform_id===platform.id).length} 个匹配模型</small>}</button>})}<p className="monitor-platforms-note">状态按各模型分别统计<br/>部分模型失败会明确标注</p></div>
     <div className="monitor-results">
      {currentPlatform?<><div className="monitor-results-heading"><div><h2>{currentPlatform.name}</h2><p>{channels.filter(channel=>channel.platform_id===currentPlatform.id).length} 个分组 / {platformModels.length} 个模型{filtering?` · ${visibleModels.length} 个匹配`:''}</p></div><StatusCounts counts={platformCounts}/></div>
       {visibleChannels.length?<div className="monitor-channel-list">{visibleChannels.map(channel=>{const models=visibleModels.filter(model=>model.channel_id===channel.id);const open=!collapsed.has(channel.id);return <section className="monitor-channel" key={channel.id} aria-label={`分组 ${channel.name}`}><button type="button" className="monitor-channel-toggle" aria-expanded={open} aria-controls={`monitor-channel-${channel.id}`} onClick={()=>toggleChannel(channel.id)}><span className="monitor-channel-title"><DownOutlined aria-hidden="true" rotate={open?0:-90}/><strong>{channel.name}</strong><small>{models.length} 个模型</small></span><span className="monitor-channel-counts">{describeCounts(countModels(models))}</span></button>{open&&<div id={`monitor-channel-${channel.id}`}>
        {models.length?<><div className="monitor-model-table"><Table<ModelView> rowKey="id" columns={columns} dataSource={models} pagination={false} size="small" scroll={{x:1204}} tableLayout="fixed"/></div><div className="monitor-mobile-models">{models.map(model=><article className="monitor-model-card" key={model.id}><div className="monitor-mobile-heading"><ModelName model={model}/><ModelStatus model={model}/></div><ModelTimeline model={model}/><div className="monitor-mobile-metrics"><div><span>近 7 天成功率</span><SuccessRate model={model}/></div><div><span>首段正文</span><strong>{duration(model.last_probe?.first_text_ms)}</strong></div><div><span>总耗时</span><strong>{duration(model.last_probe?.duration_ms)}</strong></div></div><div className="monitor-mobile-bottom"><LastProbe model={model} now={now}/><Button size="small" type="text" aria-label={`查看 ${nameOf(model)} 检测历史`} icon={<HistoryOutlined aria-hidden="true"/>} onClick={()=>openHistory(model)}>历史</Button></div></article>)}</div></>:<div className="monitor-channel-empty"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="这个分组暂无公开模型"/></div>}
       </div>}</section>})}</div>:<Empty description="当前平台暂无公开分组"/>}
      </>:<div className="monitor-no-matches"><Empty description="没有符合条件的模型"/><Button onClick={clearFilters}>清除筛选</Button></div>}
      <div className="monitor-legend"><span><i className="ok"/>正常</span><span><i className="slow"/>偏慢</span><span><i className="failed"/>失败</span><span><i className="unknown"/>暂无数据</span><small>每格 1 小时 · 该小时最严重状态</small><span className="monitor-visible-count">当前展示 {visibleModels.length} / {allModels.length} 个模型</span></div>
     </div>
    </div>:<div className="monitor-no-matches"><Empty description="暂无公开监控平台"/><p>公开平台、分组和模型后，实际检测结果会显示在这里。</p></div>}
   </QueryState>
  </section>
  <div className="monitor-footer"><p>成功率仅统计已确定成功或失败的检测；无样本显示“—”。停用状态保留历史数据，数据过期是附加标记。</p><p>页面刷新读取已保存结果，检测频率按后台计划执行。时间统一为北京时间。</p></div>
  <Modal title={`检测历史 · ${history?nameOf(history):''}`} open={!!history} onCancel={()=>setHistoryId(undefined)} footer={null} width={1050} destroyOnClose>
   {history&&<><div className="monitor-history-current"><span>{history.platform_name} / {history.channel_name}</span><code>{history.upstream_id}</code><StateTag state={history.status}/>{history.stale&&<Tag icon={<ClockCircleOutlined/>}>{history.last_probe?'数据过期':'待首次检测'}</Tag>}</div><div className="monitor-history-summary"><div><span>近 7 天成功率</span><SuccessRate model={history}/></div><div><span>首段正文</span><strong>{duration(history.last_probe?.first_text_ms)}</strong></div><div><span>最近总耗时</span><strong>{duration(history.last_probe?.duration_ms)}</strong></div></div><p className="monitor-history-note">当前模型的实际检测记录 · 北京时间 · HTTP 状态本身不代表模型调用成功</p></>}
   <QueryState loading={historyQuery.isLoading} error={historyQuery.error} empty={!historyQuery.data?.length}><div className="monitor-history-list"><Table<ProbeResult> rowKey="id" size="small" dataSource={historyQuery.data} pagination={{pageSize:10,showSizeChanger:false}} scroll={{x:810}} columns={[{title:'检测时间',dataIndex:'created_at',width:170,render:time},{title:'状态',dataIndex:'status',width:85,render:(state:string)=><StateTag state={state}/>},{title:'HTTP',dataIndex:'http_status',width:65,render:(status:number)=>status||'—'},{title:'首段正文',dataIndex:'first_text_ms',width:100,render:duration},{title:'总耗时',dataIndex:'duration_ms',width:100,render:duration},{title:'返回模型 / 结果',key:'result',render:(_:unknown,probe:ProbeResult)=><span className={probe.status==='failed'?'error-text':''}>{probeReason(probe)}</span>}]}/></div></QueryState>
  </Modal>
 </div>;
}

function ModelName({model}:{model:ModelView}){return <div className="monitor-model-name"><strong title={nameOf(model)}>{nameOf(model)}</strong>{model.display_name&&model.display_name!==model.upstream_id&&<code>{model.upstream_id}</code>}<span>{protocolLabel(model.protocol)} · {model.status==='disabled'||!model.probe_enabled?'周期检测未启用':'周期检测已开启'}</span></div>}
function ModelStatus({model}:{model:ModelView}){return <div className="monitor-model-status"><StateTag state={model.status}/>{model.running&&<Tag color="processing">检测中</Tag>}{model.stale&&<Tag icon={<ClockCircleOutlined aria-hidden="true"/>}>{model.last_probe?'数据过期':'待首次检测'}</Tag>}</div>}
function SuccessRate({model}:{model:ModelView}){return <div className="monitor-rate"><strong className={model.success_rate!=null&&model.success_rate<95?'warning-text':''}>{model.success_rate==null||!model.samples?'—':`${model.success_rate.toFixed(1)}%`}</strong><small>{model.samples} 个样本</small></div>}
function LastProbe({model,now}:{model:ModelView;now:number}){return <div className="monitor-last-probe"><span>{model.last_probe?time(model.last_probe.created_at):'尚未检测'}</span><small className={`monitor-row-reason ${model.status==='failed'?'error-text':model.stale&&model.last_probe?'warning-text':''}`}>{model.last_probe?(model.status==='failed'?probeReason(model.last_probe):`${model.stale?'数据过期 · ':''}${relativeTime(model.last_probe.created_at,now)}`):model.status==='disabled'?'周期检测未启用':'等待首次检测'}</small></div>}
function ModelTimeline({model}:{model:ModelView}){return <div className="monitor-model-timeline"><div className="timeline" aria-label={`${nameOf(model)} 最近24小时状态`}>{Array.from({length:24},(_,index)=>{const bucket=model.timeline[index];const hasSamples=!!bucket?.count;return <Tooltip key={index} title={bucket?`${time(bucket.at)} · ${hasSamples?`${({ok:'正常',slow:'偏慢',failed:'失败',unknown:'未知'} as Record<string,string>)[bucket.status]||'未知'} · ${bucket.count} 个样本`:'暂无样本'} · 北京时间`: '暂无样本'}><span className={`timeline-block ${hasSamples?bucket.status:'unknown'}`}/></Tooltip>})}</div><div className="monitor-timeline-axis"><span>24 小时前</span><span>现在</span></div></div>}
function StatusCounts({counts}:{counts:Counts}){return <Space wrap className="monitor-status-counts">{counts.ok>0&&<Tag color="success">{counts.ok} 正常</Tag>}{counts.slow>0&&<Tag color="warning">{counts.slow} 偏慢</Tag>}{counts.failed>0&&<Tag color="error">{counts.failed} 失败</Tag>}{counts.unknown>0&&<Tag>{counts.unknown} 待观测</Tag>}{counts.disabled>0&&<Tag>{counts.disabled} 已停用</Tag>}{counts.stale>0&&<Tag>{counts.stale} 数据过期</Tag>}</Space>}
