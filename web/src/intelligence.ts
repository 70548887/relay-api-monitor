import type { Artifact, GroupVerification, PublicAttempt } from './types';

export function sameQuestion(first?:Artifact,second?:Artifact){
 return !!first&&!!second&&!!first.prompt&&first.template_id===second.template_id&&first.template_version===second.template_version&&first.prompt===second.prompt;
}
export function reasoningLabel(effort?:string){return !effort||effort==='default'?'上游默认':effort}
export function requestContextLabel(parameters?:string|Record<string,unknown>){
 if(parameters==='independent')return '独立请求（无历史）';
 try{const value=typeof parameters==='string'?JSON.parse(parameters):parameters;return value?.context_policy==='independent'?'独立请求（无历史）':'未记录'}catch{return '未记录'}
}
export function thinkingLabel(mode?:string,budget?:number){return !mode||mode==='default'?'上游默认':mode==='enabled'?`enabled · ${budget||0} tokens`:mode==='disabled'?'disabled · 关闭思考':mode==='between_tools'?'between_tools · 仅工具间思考':mode}
export function verificationLabel(v?:GroupVerification){
 if(v?.status==='verified'&&v.group&&v.group!=='auto')return `令牌绑定组：${v.group}`;
 if(v?.group==='auto')return '自动分组 · 未确认实际命中';
 if(v?.status==='unsupported')return '上游不支持分组验证';
 if(v?.status==='failed'||v?.status==='error')return '仅分组信息查询失败';
 return v?.status==='unconfigured'?'未配置，可选':'绑定组未验证';
}
export function attemptParameters(attempt?:PublicAttempt|null):Record<string,unknown>{
 const value=attempt?.snapshot;
 if(value&&typeof value==='object')return value;
 if(typeof value==='string'){try{return JSON.parse(value) as Record<string,unknown>}catch{return {}}}
 return {};
}
export function countdown(next?:string|null,now=Date.now()){
 if(!next||next.startsWith('0001-'))return '未安排';
 const seconds=Math.max(0,Math.ceil((Date.parse(next)-now)/1000));
 if(!Number.isFinite(seconds))return '未安排';
 if(!seconds)return '即将执行';
 if(seconds>=3600)return `${Math.floor(seconds/3600)} 小时 ${Math.floor(seconds%3600/60)} 分`;
 return seconds>=60?`${Math.floor(seconds/60)} 分 ${seconds%60} 秒`:`${seconds} 秒`;
}

export function intervalLabel(seconds:number) {
 if(seconds>=3600){const h=Math.floor(seconds/3600),m=Math.floor(seconds%3600/60);return `${seconds} 秒 · ${h} 小时${m?` ${m} 分`:''}`}
 if(seconds>=60)return `${seconds} 秒 · ${Math.floor(seconds/60)} 分${seconds%60?` ${seconds%60} 秒`:''}`;
 return `${seconds} 秒`;
}
export const scheduleLabels:Record<string,string>={no_public_plan:'暂无公开测试计划',disabled:'自动已关闭',unavailable:'计划不可执行',maintenance:'维护暂停',worker_offline:'Worker 离线',waiting:'等待执行',due:'已到期 · 等待派发',queued:'排队中',running:'执行中',cooldown:'等待旧请求截止'};

export function testErrorHint(code?:string){
 return ({upstream_unavailable:'上游服务暂时不可用，可稍后手动测试。',timeout:'请求超时；配置的总超时是上限，连接阶段也可能提前超时。',connection_timeout:'连接上游超时，尚未完成连接或 TLS 握手。',generation_timeout:'生成等待达到本次设置的总超时。',truncated:'上游达到输出 Token 上限，内容不完整；可检查输出和推理参数后手动测试。',response_too_large:'旧版接收限制触发；部分正文已保留，不代表完整生成成功。',response_stream_too_large:'流式传输累计超过 32 MiB，已停止接收。',response_event_too_large:'单个 SSE 事件或数据行超过 4 MiB。',response_text_too_large:'生成正文超过 2 MiB。',response_json_too_large:'非流式生成 JSON 超过 8 MiB。'} as Record<string,string>)[code||'']||'';
}
