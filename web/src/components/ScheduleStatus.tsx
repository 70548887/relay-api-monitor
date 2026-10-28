import '../schedule-status.css';
import {useEffect,useState} from 'react';
import {useQuery} from '@tanstack/react-query';
import {Tag} from 'antd';
import {api} from '../api';
import {time} from '../lib';
import {countdown,intervalLabel,scheduleLabels} from '../intelligence';
import type {IntelligenceGroupStatus,IntelligenceStatusResponse} from '../types';
export function useIntelligenceStatus(){return useQuery({queryKey:['admin-intelligence-status'],queryFn:({signal})=>api<IntelligenceStatusResponse>('/admin/intelligence-status',{signal}),refetchInterval:5000})}
export function ScheduleStatus({group,template,model}:{group?:IntelligenceGroupStatus;template?:string;model?:string}){
 const [now,setNow]=useState(Date.now());useEffect(()=>{const t=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(t)},[]);
 if(!group)return <span className="muted">计划状态读取中</span>;
 const plans=group.plans.filter(p=>(!template||p.template_id===template)&&(!model||p.model_id===model));
 return <div className="schedule-status" aria-label="定时计划状态">{plans.length?plans.map(p=><div className="schedule-plan" key={p.model_id+p.template_id}>
 <div><Tag>{p.template_id==='en_svg'?'英文 SVG':'中文 HTML'}</Tag><Tag color={p.enabled?'processing':'default'}>{scheduleLabels[p.state]||p.state}</Tag></div>
 <div>{p.requested_model} · {intervalLabel(p.interval_seconds)}</div>
 <div>下次预计触发：{p.next_run_at?time(p.next_run_at):'未安排'}{p.next_run_at&&Date.parse(p.next_run_at)>now?`（${countdown(p.next_run_at,now)}后）`:''}</div>
 <div className="muted">{p.blocking_reason}</div>
 {p.visibility_reasons.length>0&&<div className="schedule-warning">前台不可见：{p.visibility_reasons.join('、')} · <a href={'/admin/models?model='+encodeURIComponent(p.model_id)}>检查公开设置</a></div>}
 </div>):<span className="muted">暂无定时作品计划</span>}<small className="muted">预计触发时间；实际开始受排队影响 · 北京时间</small></div>
}
