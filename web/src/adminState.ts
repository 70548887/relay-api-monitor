import type { Channel, Model, Platform, Settings } from './types';

export function activeRecords<T extends {archived:boolean}>(items:readonly T[]|undefined){return (items||[]).filter(item=>!item.archived)}
export function newModelDefaults(settings?:Settings){return {public:true,source:'manual',protocol:'',reasoning_effort:'',thinking_mode:'',thinking_budget:0,max_tokens:8192,probe_enabled:false,artifact_chinese:false,artifact_english:false,probe_interval_seconds:settings?.probe_interval_seconds||300,artifact_interval_seconds:settings?.artifact_interval_seconds||1800,timeout_seconds:settings?.artifact_timeout_seconds||600,sort:0}}
export function platformUpdate(existing:Platform,values:Partial<Platform>){return {...existing,...values}}
export function channelUpdate(existing:Channel,values:Partial<Channel>&{api_key?:string}){const {key_mask:_,...preserved}=existing;return {...preserved,...values}}
export function availableModels(models:readonly Model[]|undefined,channels:readonly Channel[]|undefined){const enabledChannels=new Set(activeRecords(channels).filter(channel=>channel.enabled).map(channel=>channel.id));return activeRecords(models).filter(model=>!model.missing&&enabledChannels.has(model.channel_id))}
