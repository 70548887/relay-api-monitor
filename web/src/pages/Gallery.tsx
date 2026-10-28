import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Alert, Button, Checkbox, Empty, Modal, Segmented, Select, Space, Switch, Tag, Tooltip } from 'antd';
import { ExperimentOutlined, HistoryOutlined, ReloadOutlined } from '@ant-design/icons';
import { api } from '../api';
import type { Artifact, IntelligenceData, IntelligenceGroup, PromptTemplate } from '../types';
import { CN_PROMPT, EN_PROMPT, duration, nameOf, stateColor, time } from '../lib';
import { attemptParameters, countdown, intervalLabel, reasoningLabel, requestContextLabel, sameQuestion, scheduleLabels, thinkingLabel, verificationLabel } from '../intelligence';
import { PageHeading, QueryState } from '../components/Common';
import { Preview } from '../components/Preview';
import { Thumbnail } from '../components/Thumbnail';
import '../intelligence.css';

export default function Gallery() {
 const [template,setTemplate]=useState('cn_html');
 const [platform,setPlatform]=useState<string>();
 const [channel,setChannel]=useState<string>();
 const [model,setModel]=useState<string>();
 const [automatic,setAutomatic]=useState(true);
 const [now,setNow]=useState(Date.now());
 const [chosen,setChosen]=useState<Record<string,string>>({});
 const [selected,setSelected]=useState<Artifact[]>([]);
 const [compare,setCompare]=useState(false);
 const [picker,setPicker]=useState(false);
 const templates=useQuery({queryKey:['templates'],queryFn:({signal})=>api<PromptTemplate[]>('/public/templates',{signal})});
 const catalog=useQuery({queryKey:['intelligence',template,undefined],queryFn:({signal})=>api<IntelligenceData>(`/public/intelligence?template_id=${encodeURIComponent(template)}`,{signal}),refetchInterval:automatic?5000:false});
 const filtered=useQuery({queryKey:['intelligence',template,model||'no-model-filter'],queryFn:({signal})=>api<IntelligenceData>(`/public/intelligence?template_id=${encodeURIComponent(template)}&model_id=${encodeURIComponent(model!)}`,{signal}),enabled:!!model,refetchInterval:model&&automatic?5000:false});
 const intelligence=model?filtered:catalog;
 const groups=catalog.data?.groups||[];
 const platforms=useMemo(()=>[...new Map(groups.map(group=>[group.platform_id,{id:group.platform_id,name:group.platform_name}])).values()],[groups]);
 const currentPlatform=platforms.some(item=>item.id===platform)?platform:platforms[0]?.id;
 const visibleGroups=groups.filter(group=>group.platform_id===currentPlatform);
 const navigationGroup=visibleGroups.find(item=>item.channel_id===channel)||visibleGroups[0];
 const group=model?filtered.data?.groups.find(item=>item.channel_id===navigationGroup?.channel_id):navigationGroup;
 const selectionKey=`${template}/${group?.channel_id||''}/${model||''}`;
 const history=group?.history||[];
 // Resolve selections against the current public list so hidden/deleted works cannot linger.
 const active=history.find(item=>item.id===chosen[selectionKey])||history[0];
 const historical=!!active&&active.id!==history[0]?.id;
 const detail=useQuery({queryKey:['artifact',active?.id],queryFn:({signal})=>api<Artifact>(`/public/artifacts/${encodeURIComponent(active!.id)}`,{signal}),enabled:!!active?.id,staleTime:Infinity});
 const current=active&&(detail.data?.id===active.id?detail.data:active);
 const first=useQuery({queryKey:['artifact',selected[0]?.id],queryFn:({signal})=>api<Artifact>(`/public/artifacts/${encodeURIComponent(selected[0].id)}`,{signal}),enabled:compare&&selected.length===2,staleTime:Infinity});
 const second=useQuery({queryKey:['artifact',selected[1]?.id],queryFn:({signal})=>api<Artifact>(`/public/artifacts/${encodeURIComponent(selected[1].id)}`,{signal}),enabled:compare&&selected.length===2,staleTime:Infinity});
 const choices=useMemo(()=>[...new Map([...groups.flatMap(item=>item.history),...(model?filtered.data?.groups.flatMap(item=>item.history)||[]:[])].map(artifact=>[artifact.id,artifact])).values()],[groups,model,filtered.data]);
 const promptTemplate=templates.data?.find(item=>item.id===template);
 const prompt=promptTemplate?.prompt||(template==='cn_html'?CN_PROMPT:EN_PROMPT);
 useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[]);
 useEffect(()=>{if(!catalog.data)return;const allowed=new Set(choices.map(item=>item.id));setSelected(previous=>previous.every(item=>allowed.has(item.id))?previous:previous.filter(item=>allowed.has(item.id)))},[catalog.data,choices]);
 useEffect(()=>{if(!intelligence.data||!group)return;setChosen(previous=>{if(!previous[selectionKey]||group.history.some(item=>item.id===previous[selectionKey]))return previous;const next={...previous};delete next[selectionKey];return next})},[intelligence.data,group,selectionKey]);
 useEffect(()=>{if(selected.length!==2)setCompare(false)},[selected.length]);
 const changeTemplate=(value:string)=>{setTemplate(value);setSelected([]);setCompare(false);setModel(undefined)};
 const toggleCompare=(artifact:Artifact,checked:boolean)=>setSelected(previous=>checked?[...previous.filter(item=>item.id!==artifact.id),artifact].slice(0,2):previous.filter(item=>item.id!==artifact.id));
 const latest=()=>setChosen(previous=>{const next={...previous};delete next[selectionKey];return next});
 const refresh=()=>void Promise.all([catalog.refetch(),model?filtered.refetch():Promise.resolve()]);
 return <div className="intelligence-page">
  <div className="intelligence-heading"><PageHeading eyebrow="MODEL INTELLIGENCE / 创作对照" title="同一道题，看看模型如何作答。" description="按平台与分组查看真实作品，切换历史，比较同一提示词下的模型表现。" extra={<Segmented aria-label="测试模板" options={[{label:'中文 · HTML 动画',value:'cn_html'},{label:'English · SVG',value:'en_svg'}]} value={template} onChange={value=>changeTemplate(String(value))}/>}/></div>
  <QueryState loading={catalog.isLoading} error={catalog.error}>
   {!!platforms.length&&<div className="intelligence-selectors panel">
    <div className="intelligence-platform-row"><span className="selector-label">平台</span><div className="tab-selector" role="group" aria-label="平台">{platforms.map(item=><button type="button" aria-pressed={currentPlatform===item.id} className={currentPlatform===item.id?'active':''} key={item.id} onClick={()=>{setPlatform(item.id);setChannel(undefined);setModel(undefined)}}>{item.name}</button>)}</div><div className="intelligence-refresh"><Switch size="small" checked={automatic} onChange={setAutomatic} aria-label="自动刷新"/><span>自动刷新 · 5 秒</span><Button size="small" icon={<ReloadOutlined aria-hidden="true" spin={catalog.isFetching||intelligence.isFetching}/>} onClick={refresh}>刷新</Button></div></div>
    <div className="group-selector-row"><span className="selector-label">分组</span><div className="tab-selector group-tabs" role="group" aria-label="分组">{visibleGroups.map(item=><button type="button" aria-pressed={navigationGroup?.channel_id===item.channel_id} className={navigationGroup?.channel_id===item.channel_id?'active':''} key={item.channel_id} onClick={()=>{setChannel(item.channel_id);setModel(undefined)}}>{item.channel_name}</button>)}</div><Select allowClear showSearch optionFilterProp="label" placeholder={navigationGroup?.requested_model?`代表：${navigationGroup.requested_model}`:'分组代表模型'} aria-label="筛选模型" value={model} onChange={setModel} options={(navigationGroup?.models||[]).map(item=>({label:nameOf(item),value:item.id}))}/></div>
   </div>}
   {model&&intelligence.error&&<Button onClick={()=>setModel(undefined)}>返回分组默认视图</Button>}
   <QueryState loading={intelligence.isLoading} error={intelligence.error}>
   {group?<section className="intelligence-workbench panel" aria-label="作品与测试状态">
    <aside className="intelligence-sidebar" aria-label="作品信息与计划">
     <div className="sidebar-group-heading"><span className="muted">{group.platform_name}</span><h2>{group.channel_name}</h2><Space wrap>{current&&<Tag color="success">{historical?'历史成功作品':'最新成功作品'}</Tag>}{current&&<Tag>{requestContextLabel(current.parameters)}</Tag>}</Space></div>
     {current?<SelectedArtifactInfo artifact={current} historical={historical}/>:<div className="selected-artifact-info"><h3>等待首个作品</h3><p className="muted">成功生成后，这里会显示所选作品的模型、参数和用量。</p></div>}
     <GroupStatus group={group} workerOnline={intelligence.data?.worker_online===true} now={now}/>
     <details className="sidebar-prompt" open><summary>本次提示词 <span>v{current?.template_version||promptTemplate?.version||1}</span></summary><p>{current?.prompt||prompt}</p></details>
    </aside>
    <div className="artwork-column">
     <section className="intelligence-work" aria-label="所选作品预览"><div className="work-heading"><div><h2>当前作品</h2><span className="muted">{current?`${nameOf(current)} · ${time(current.created_at)} · ${historical?'历史作品':'最近成功作品'}`:'作品生成状态与失败原因见本轮执行信息'}</span></div>{historical&&<Button size="small" icon={<HistoryOutlined aria-hidden="true"/>} onClick={latest}>回到最新</Button>}</div>
      {current?<><QueryState loading={detail.isLoading} error={detail.error}><Preview showExpandLabel key={current.id} document={current.preview_document} code={current.code} kind={current.kind} title={`${nameOf(current)} · 当前作品`}/></QueryState>{detail.error&&<Button size="small" onClick={()=>void detail.refetch()}>重新读取作品</Button>}<div className="artwork-preview-note"><span>完整画面等比显示 · 点击历史作品同步切换预览与左侧信息</span><span>隔离预览</span></div></>:<div className="intelligence-empty"><Empty description="这个分组还没有成功作品"/><p>{emptyMessage(group)}</p></div>}
     </section>
     <section className="intelligence-history" aria-label="历史作品"><div className="history-heading"><div><h2><HistoryOutlined aria-hidden="true"/> 历史作品 <span>{Math.min(history.length,8)}/8</span></h2><span className="muted">最近 8 次成功作品 · 勾选两份同题作品比较</span></div><Space wrap><span className="muted">已选 {selected.length}/2</span><Button size="small" onClick={()=>setPicker(true)}>跨组选择</Button><Button size="small" icon={<ExperimentOutlined aria-hidden="true"/>} disabled={selected.length!==2} onClick={()=>setCompare(true)}>并排比较</Button></Space></div>
      {history.length?<div className="history-grid">{history.slice(0,8).map((artifact,index)=>{const checked=selected.some(item=>item.id===artifact.id);const incompatible=selected.length===1&&!sameQuestion(selected[0],artifact);const isActive=current?.id===artifact.id;return <div className={`history-card artifact-card ${isActive?'active':''}`} key={artifact.id} data-artifact-id={artifact.id}>
       <button className="history-preview" type="button" onClick={()=>setChosen(previous=>({...previous,[selectionKey]:artifact.id}))} aria-pressed={isActive} aria-label={`预览 ${nameOf(artifact)} ${index+1}`}><Thumbnail artifact={artifact} title={`${nameOf(artifact)} · ${time(artifact.created_at)}`}/>{isActive&&<span className="history-current-badge">当前</span>}</button>
       <div className="history-card-content"><div className="history-card-top"><Tooltip title={incompatible?'提示词原文或模板版本不同，不能作为同题比较':''}><Checkbox aria-label={`选择作品 ${index+1}`} checked={checked} disabled={!checked&&(selected.length>=2||incompatible)} onChange={event=>toggleCompare(artifact,event.target.checked)}/></Tooltip></div><strong title={nameOf(artifact)}>{nameOf(artifact)}</strong><time dateTime={artifact.created_at} title={`北京时间 ${time(artifact.created_at)}`}>{historyTime(artifact.created_at)}</time><div className="history-card-stats"><span>{duration(artifact.duration_ms)}</span><span title={`推理强度 ${reasoningLabel(artifact.reasoning_effort)}`}>{reasoningLabel(artifact.reasoning_effort)}</span></div></div>
      </div>})}</div>:<div className="empty-panel"><Empty description="暂无历史作品，当前任务状态仍可在上方查看。"/></div>}
     </section>
     <p className="intelligence-snapshot muted">状态快照 {time(intelligence.data?.updated_at)}{intelligence.isFetching?' · 更新中':''} · 时间均为北京时间</p>
    </div>
   </section>:<div className="panel empty-panel"><Empty description="暂无公开创作分组"/><p className="muted">在后台公开分组并选择代表模型后，生成状态和作品会显示在这里。</p><Button icon={<ReloadOutlined/>} onClick={()=>void intelligence.refetch()}>刷新分组</Button></div>}
   </QueryState>
  </QueryState>
  <Modal title="选择同题作品" open={picker} onCancel={()=>setPicker(false)} footer={<Button type="primary" onClick={()=>setPicker(false)}>完成选择</Button>} destroyOnClose><Alert type="info" showIcon message="比较要求提示词原文、模板 ID 与版本完全一致。"/><Select className="compare-picker" mode="multiple" maxCount={2} value={selected.map(item=>item.id)} placeholder="选择当前或其他分组的两次作品" aria-label="跨组比较作品" optionFilterProp="label" onChange={ids=>setSelected(ids.map(id=>choices.find(item=>item.id===id)!).filter(Boolean))} options={choices.map(artifact=>({value:artifact.id,label:`${artifact.platform_name||''} / ${artifact.channel_name||artifact.channel_id} · ${nameOf(artifact)} · ${time(artifact.created_at)}`,disabled:!selected.some(item=>item.id===artifact.id)&&selected.length===1&&!sameQuestion(selected[0],artifact)}))}/>{selected.map(artifact=><p className="muted" key={artifact.id}>{nameOf(artifact)} · v{artifact.template_version} · {time(artifact.created_at)}</p>)}</Modal>
  <Modal title="同题作品比较" open={compare} onCancel={()=>setCompare(false)} footer={null} width="96vw" destroyOnClose><QueryState loading={first.isLoading||second.isLoading} error={first.error||second.error}>{first.data&&second.data&&(sameQuestion(first.data,second.data)?<><Alert type="success" message={`已核对提示词原文与模板 v${first.data.template_version} 完全一致`}/><div className="compare-grid">{[first.data,second.data].map(artifact=><div key={artifact.id}><h3>{artifact.channel_name} · {nameOf(artifact)}</h3><Preview document={artifact.preview_document} code={artifact.code} kind={artifact.kind} active={compare} title={`比较 · ${nameOf(artifact)}`}/><ArtifactMeta artifact={artifact}/></div>)}</div></>:<Alert type="warning" message="这两次作品的提示词原文或模板版本不同，无法进行同题比较。"/>)}</QueryState></Modal>
 </div>;
}

function emptyMessage(group:IntelligenceGroup){const state=group.latest_attempt?.state;return group.latest_attempt?.error_message||(['running','dispatching','claimed'].includes(state||'')?'正在接收生成内容，完成后会显示作品。':state==='queued'?'任务已排队，等待 Worker 执行。':group.schedule_state==='no_public_plan'?'暂无公开测试计划与作品。':!group.automatic_enabled?'自动已关闭，可在后台运行一次任务。':'等待首次自动创作。')}

function SelectedArtifactInfo({artifact,historical}:{artifact:Artifact;historical:boolean}){
 const context=requestContextLabel(artifact.parameters);
 return <section className="selected-artifact-info" aria-label="所选作品信息"><div className="sidebar-section-title"><h3>所选作品</h3><span className="muted">{historical?'历史记录':'最新成功'}</span></div>
  <div className="artifact-sidebar-metrics"><Metric wide label="请求 / 返回模型标识" value={artifact.requested_model||'—'} detail={artifact.returned_model||'未提供'}/><Metric label="调用协议" value={protocolName(artifact.protocol)}/><Metric label="推理强度" value={reasoningLabel(artifact.reasoning_effort)}/><Metric wide label="开始 / 完成时间 · 北京时间" value={time(artifact.started_at)} detail={time(artifact.created_at)}/><Metric label="生成耗时" value={duration(artifact.duration_ms)}/><Metric label="总 Tokens" value={formatUsage(artifact.usage?.total_tokens)}/></div>
  <div className="independent-request-note"><strong>{context}</strong><span>{context.startsWith('独立请求')?'仅发送本次提示词，不携带历史对话。':'这份历史记录未保存上下文策略。'}</span></div>
  <details className="artifact-extra-details"><summary>更多生成参数与用量</summary><div className="artifact-sidebar-metrics"><Metric label="首段正文" value={duration(artifact.first_text_ms)}/><Metric label="结束原因" value={artifact.finish_reason||'未提供'}/><Metric label="输入 Tokens" value={formatUsage(artifact.usage?.input_tokens)}/><Metric label="输出 Tokens" value={formatUsage(artifact.usage?.output_tokens)}/><Metric label="缓存命中输入" value={formatUsage(artifact.usage?.cached_input_tokens)}/><Metric label="缓存写入输入" value={formatUsage(artifact.usage?.cache_creation_input_tokens)}/><Metric wide label="Thinking" value={thinkingLabel(artifact.thinking_mode,artifact.thinking_budget)}/></div><p className="muted">缓存用量来自上游返回，与历史会话是否上传无关。</p></details>
 </section>;
}

export function GroupStatus({group,workerOnline,now}:{group:IntelligenceGroup;workerOnline:boolean;now:number}){
 const attempt=group.latest_attempt;const parameters=attemptParameters(attempt);const state=attempt?.state||'unknown';const running=['claimed','dispatching','running'].includes(state);const matchingArtifact=group.history.find(artifact=>artifact.job_id===attempt?.id);
 const started=attempt?.started_at,finished=attempt?.finished_at;const elapsed=attempt?.duration_ms??(started&&(finished||running)?Math.max(0,(finished?Date.parse(finished):now)-Date.parse(started)):undefined);const usage=attempt?.usage||matchingArtifact?.usage;
 const requested=attempt?.requested_model||String(parameters.requested_model||parameters.upstream_id||parameters.model||group.requested_model||'—');
 const effort=attempt?.reasoning_effort??(Object.prototype.hasOwnProperty.call(parameters,'reasoning_effort')?String(parameters.reasoning_effort||''):group.reasoning_effort);const thinking=attempt?.thinking_mode??(Object.prototype.hasOwnProperty.call(parameters,'thinking_mode')?String(parameters.thinking_mode||''):group.thinking_mode);
 const showProgress=!!attempt&&['queued','claimed','dispatching','running','failed','uncertain'].includes(state);
 const planState=group.schedule_state||(!group.automatic_enabled?'disabled':!workerOnline?'worker_offline':'waiting');const planLabel=scheduleLabels[planState]||planState;const future=!!group.next_run_at&&Date.parse(group.next_run_at)>now;
 const attemptLabel=({queued:'排队中',claimed:'准备生成',dispatching:'生成中',uncertain:'结果未知',skipped:'已跳过',running:'生成中',succeeded:'生成成功',completed:'生成成功',failed:'生成失败',canceled:'已取消',cancelled:'已取消',unknown:'暂无公开执行记录'} as Record<string,string>)[state]||state;
 return <section className="group-status" aria-label="当前分组状态"><div className="group-status-heading"><h3>当前定时计划</h3><Tag color={group.automatic_enabled?'processing':'default'}>{group.schedule_state==='no_public_plan'?'暂无公开测试计划':group.automatic_enabled?'已开启':'自动已关闭'}</Tag></div>
  <div className="next-generation"><span>下次预计触发 · 北京时间</span><strong>{time(group.next_run_at)}</strong><small>{future?`${countdown(group.next_run_at,now)}后 · ${planLabel}`:planLabel}</small></div>
  <div className="schedule-runtime"><span>{group.interval_seconds?`周期 ${intervalLabel(group.interval_seconds)}`:'未设置周期'}</span><Tag color={workerOnline?'success':'warning'}>Worker {workerOnline?'在线':'离线'}</Tag></div><p className="muted schedule-explanation">{group.schedule_message||(future?'等待下次预计触发时间':planLabel)} · 预计触发后可能排队。</p>
  <div className="sidebar-section-title current-attempt-heading"><h3>本轮执行</h3><Tag color={stateColor[state]||'default'}>{attemptLabel}</Tag>{attempt?.cancel_requested&&<Tag>取消请求中</Tag>}</div>
  {attempt&&<>{showProgress&&<div className="group-metrics"><Metric wide label="请求模型" value={requested}/><Metric label="已接收字符" value={(attempt.progress_chars||0).toLocaleString('zh-CN')}/><Metric label="本次耗时" value={duration(elapsed)}/></div>}<details className="current-attempt-details"><summary>本轮任务详情</summary><div className="group-metrics">{!showProgress&&<><Metric wide label="请求模型" value={requested}/><Metric label="已接收字符" value={(attempt.progress_chars||0).toLocaleString('zh-CN')}/><Metric label="本次耗时" value={duration(elapsed)}/></>}<Metric wide label="返回模型" value={attempt.returned_model||matchingArtifact?.returned_model||'—'}/><Metric label="推理强度" value={reasoningLabel(effort)}/><Metric label="Thinking" value={thinkingLabel(thinking,Number(attempt.thinking_budget??parameters.thinking_budget??group.thinking_budget))}/><Metric wide label="上下文" value={requestContextLabel(attempt.context_policy)}/><Metric wide label="发起时间" value={time(started||attempt.created_at)}/><Metric wide label="完成时间" value={time(finished)}/><Metric label="输入 Tokens" value={usage?.input_tokens??'未提供'}/><Metric label="输出 Tokens" value={usage?.output_tokens??'未提供'}/><Metric label="缓存命中输入" value={usage?.cached_input_tokens??'未提供'}/><Metric label="缓存写入输入" value={usage?.cache_creation_input_tokens??'未提供'}/></div>{attempt.progress_at&&<p className="muted progress-time">最近接收 {time(attempt.progress_at)}</p>}</details></>}
  {attempt?.error_message&&<Alert showIcon type={state==='failed'?'error':'warning'} message={attempt.error_message}/>} {!workerOnline&&(state==='queued'||running)&&<Alert showIcon type="warning" message="Worker 当前离线，任务进度将在服务恢复后更新。"/>}
  <div className="binding-information"><Tooltip title={group.group_verification?.message||'仅验证令牌绑定组；不证明每个请求的实际路由。'}><Tag>{verificationLabel(group.group_verification)}</Tag></Tooltip><p className="muted">仅分组信息查询，不影响生成与定时执行。</p></div>
 </section>;
}
function historyTime(value:string){return new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false})}
function protocolName(protocol?:string){return ({chat:'Chat Completions',responses:'Responses',anthropic:'Anthropic'} as Record<string,string>)[protocol||'']||protocol||'未记录'}
function formatUsage(value?:number|null){return value==null?'未提供':value.toLocaleString('zh-CN')}
function Metric({label,value,detail,wide=false}:{label:string;value:string|number;detail?:string;wide?:boolean}){return <div className={wide?'sidebar-wide':undefined}><span>{label}</span><strong>{value}</strong>{detail&&<small>{detail}</small>}</div>}
export function ArtifactMeta({artifact}:{artifact:Artifact}){return <div className="artifact-detail-meta"><div className="artifact-detail-grid"><Metric label="请求模型" value={artifact.requested_model||'—'}/><Metric label="返回模型" value={artifact.returned_model||'—'}/><Metric label="推理强度" value={reasoningLabel(artifact.reasoning_effort)}/><Metric label="Thinking" value={thinkingLabel(artifact.thinking_mode,artifact.thinking_budget)}/><Metric label="首段正文" value={duration(artifact.first_text_ms)}/><Metric label="总耗时" value={duration(artifact.duration_ms)}/><Metric label="输入 Tokens" value={artifact.usage?.input_tokens??'未提供'}/><Metric label="输出 Tokens" value={artifact.usage?.output_tokens??'未提供'}/><Metric label="上下文" value={requestContextLabel(artifact.parameters)}/><Metric label="缓存命中输入" value={artifact.usage?.cached_input_tokens??'未提供'}/><Metric label="缓存写入输入" value={artifact.usage?.cache_creation_input_tokens??'未提供'}/><Metric label="发起时间" value={time(artifact.started_at)}/><Metric label="完成时间" value={time(artifact.created_at)}/></div><p className="muted">{protocolName(artifact.protocol)} · 模板 v{artifact.template_version} · 结束原因 {artifact.finish_reason||'—'} · {verificationLabel({status:artifact.group_verification_status,group:artifact.verified_group})}</p><p className="muted">上下文独立与上游缓存是两回事；缓存用量仅按上游返回记录，未提供时无法确认。</p><details><summary>原文提示词</summary><p>{artifact.prompt}</p></details></div>}
