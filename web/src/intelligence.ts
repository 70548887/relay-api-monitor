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
 if(v?.status==='failed'||v?.status==='error')return '分组验证失败';
 return '绑定组未验证';
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
