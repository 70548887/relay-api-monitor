import type { Channel, Model, Platform, Settings } from './types';

export function activeRecords<T extends {archived:boolean}>(items:readonly T[]|undefined){return (items||[]).filter(item=>!item.archived)}
export function channelModelControls(channel?:Channel){
 if(channel?.model_controls)return channel.model_controls;
 const plan=channel?.intelligence_schedule;
 if(plan)return {probe_enabled:true,artifact_chinese:plan.enabled&&plan.template_ids.includes('cn_html'),artifact_english:plan.enabled&&plan.template_ids.includes('en_svg')};
 return {probe_enabled:true,artifact_chinese:true,artifact_english:true};
}
export function newModelDefaults(settings?:Settings,channel?:Channel){return {public:channel?.public??true,source:'manual',protocol:'',reasoning_effort:'',thinking_mode:'',thinking_budget:0,max_tokens:8192,...channelModelControls(channel),probe_interval_seconds:settings?.probe_interval_seconds||300,artifact_interval_seconds:settings?.artifact_interval_seconds||1800,timeout_seconds:settings?.artifact_timeout_seconds||600,sort:0}}
export function effectiveArtifactFlags(model:Model,channel?:Channel){
 const controls=channel?.model_controls,plan=channel?.intelligence_schedule;
 const selected=!plan||plan.model_scope==='all'||plan.model_id===model.id;
 const allowed=(template:string,raw:boolean,master:boolean|undefined,override:boolean|null|undefined)=>{
  if(master===false||override===false)return false;
  if(plan)return plan.enabled&&selected&&plan.template_ids.includes(template)&&(!controls||raw);
  return raw;
 };
 return {artifact_chinese:allowed('cn_html',model.artifact_chinese,controls?.artifact_chinese,model.artifact_overrides?.chinese),artifact_english:allowed('en_svg',model.artifact_english,controls?.artifact_english,model.artifact_overrides?.english)};
}
export function modelFormValues(model:Model,channel?:Channel){return {...model,...effectiveArtifactFlags(model,channel)}}
export function modelUpdate(existing:Model,values:Partial<Model>,channel?:Channel){
 const result={...existing,...values};const initial=effectiveArtifactFlags(existing,channel);const changes:string[]=[];
 for(const flag of ['public','probe_enabled'] as const)if(values[flag]!==undefined&&values[flag]!==existing[flag])changes.push(flag);
 for(const flag of ['artifact_chinese','artifact_english'] as const){
  if(values[flag]===undefined||values[flag]===initial[flag]){result[flag]=existing[flag];continue}
  changes.push(flag);
 }
 return {...result,model_control_changes:changes};
}
export function modelDailyCalls(values:Partial<Model>,channel?:Channel,settings?:Settings){
 if(values.archived||values.missing||channel&&(!channel.enabled||channel.archived))return {probe:0,artifacts:0};
 const controls=channelModelControls(channel),plan=channel?.intelligence_schedule;
 const selected=!plan||plan.model_scope==='all'||plan.model_id===values.id;
 const probe=selected&&values.probe_enabled&&controls.probe_enabled?86400/Math.max(30,Number(values.probe_interval_seconds)||settings?.probe_interval_seconds||300):0;
 const templates=Number(!!values.artifact_chinese&&controls.artifact_chinese&&(!plan||plan.template_ids.includes('cn_html')))+Number(!!values.artifact_english&&controls.artifact_english&&(!plan||plan.template_ids.includes('en_svg')));
 const artifacts=(!plan||plan.enabled)&&selected?templates*86400/Math.max(60,plan?.interval_seconds||Number(values.artifact_interval_seconds)||settings?.artifact_interval_seconds||1800):0;
 return {probe,artifacts};
}
export function platformUpdate(existing:Platform,values:Partial<Platform>){return {...existing,...values}}
export function channelUpdate(existing:Channel,values:Partial<Channel>&{api_key?:string}){const {key_mask:_,...preserved}=existing;return {...preserved,...values}}
export function availableModels(models:readonly Model[]|undefined,channels:readonly Channel[]|undefined){const enabledChannels=new Set(activeRecords(channels).filter(channel=>channel.enabled).map(channel=>channel.id));return activeRecords(models).filter(model=>!model.missing&&enabledChannels.has(model.channel_id))}
