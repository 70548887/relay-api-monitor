import { describe, expect, it } from 'vitest';
import { activeRecords, availableModels, channelUpdate, newModelDefaults, platformUpdate } from './adminState';
import type { Channel, Model, Platform, Settings } from './types';

describe('administrator editing and task selection',()=>{
 it('creates models with every automatic paid task disabled',()=>{
  expect(newModelDefaults()).toMatchObject({timeout_seconds:600,reasoning_effort:'',thinking_mode:'',thinking_budget:0});
  const defaults=newModelDefaults({probe_interval_seconds:120,artifact_interval_seconds:900,artifact_timeout_seconds:240} as Settings);
  expect(defaults).toMatchObject({probe_enabled:false,artifact_chinese:false,artifact_english:false,probe_interval_seconds:120,artifact_interval_seconds:900,timeout_seconds:240});
 });
 it('preserves unedited metadata and keeps an empty replacement key empty',()=>{
  const platform={id:'p',name:'Old',archived:false,created_at:'saved',public:false,sort:9} as Platform;
  expect(platformUpdate(platform,{name:'New'})).toMatchObject({...platform,name:'New'});
  const channel={id:'c',name:'Old',key_mask:'sk-****',revision:7,models_path:'/custom-models',enabled:false,archived:false} as Channel;
  expect(channelUpdate(channel,{name:'New',api_key:''})).toMatchObject({id:'c',revision:7,models_path:'/custom-models',enabled:false,name:'New',api_key:''});
  expect(channelUpdate(channel,{name:'New'})).not.toHaveProperty('key_mask');
 });
 it('excludes archived records and prevents runs on archived, disabled, or missing targets',()=>{
  const channels=[{id:'active',enabled:true,archived:false},{id:'off',enabled:false,archived:false},{id:'archive',enabled:true,archived:true}] as Channel[];
  const models=[{id:'ok',channel_id:'active',archived:false,missing:false},{id:'archived',channel_id:'active',archived:true},{id:'missing',channel_id:'active',archived:false,missing:true},{id:'off',channel_id:'off',archived:false},{id:'old',channel_id:'archive',archived:false}] as Model[];
  expect(activeRecords(channels).map(c=>c.id)).toEqual(['active','off']);
  expect(availableModels(models,channels).map(m=>m.id)).toEqual(['ok']);
 });
});
