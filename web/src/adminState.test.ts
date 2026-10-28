import { describe, expect, it } from 'vitest';
import { activeRecords, availableModels, channelModelControls, channelUpdate, effectiveArtifactFlags, modelDailyCalls, modelFormValues, modelUpdate, newModelDefaults, platformUpdate } from './adminState';
import type { Channel, Model, Platform, Settings } from './types';

describe('administrator editing and task selection',()=>{
 it('creates models with public and all task controls enabled by default',()=>{
  expect(newModelDefaults()).toMatchObject({timeout_seconds:600,reasoning_effort:'',thinking_mode:'',thinking_budget:0});
  const defaults=newModelDefaults({probe_interval_seconds:120,artifact_interval_seconds:900,artifact_timeout_seconds:240} as Settings);
  expect(defaults).toMatchObject({probe_enabled:true,artifact_chinese:true,artifact_english:true,probe_interval_seconds:120,artifact_interval_seconds:900,timeout_seconds:240});
 });
 it('preserves unedited metadata and keeps an empty replacement key empty',()=>{
  const platform={id:'p',name:'Old',archived:false,created_at:'saved',public:false,sort:9} as Platform;
  expect(platformUpdate(platform,{name:'New'})).toMatchObject({...platform,name:'New'});
  const channel={id:'c',name:'Old',key_mask:'sk-****',revision:7,models_path:'/custom-models',enabled:false,archived:false} as Channel;
  expect(channelUpdate(channel,{name:'New',api_key:''})).toMatchObject({id:'c',revision:7,models_path:'/custom-models',enabled:false,name:'New',api_key:''});
  expect(channelUpdate(channel,{name:'New'})).not.toHaveProperty('key_mask');
 });
 it('inherits an off parent when creating models and never estimates calls through that master',()=>{
  const channel={id:'c',enabled:true,public:false,model_controls:{probe_enabled:false,artifact_chinese:false,artifact_english:true}} as Channel;
  expect(newModelDefaults(undefined,channel)).toMatchObject({public:false,probe_enabled:false,artifact_chinese:false,artifact_english:true});
  expect(modelDailyCalls({probe_enabled:true,artifact_chinese:true,artifact_english:false},channel)).toEqual({probe:0,artifacts:0});
 });
 it('adopts the actual legacy plan masters without turning a disabled plan on during an ordinary edit',()=>{
  const disabled={id:'legacy',public:true,intelligence_schedule:{enabled:false,model_id:'m',template_ids:['cn_html','en_svg'],interval_seconds:1800}} as Channel;
  expect(channelModelControls(disabled)).toEqual({probe_enabled:true,artifact_chinese:false,artifact_english:false});
  expect(newModelDefaults(undefined,disabled)).toMatchObject({public:true,probe_enabled:true,artifact_chinese:false,artifact_english:false});
  const chinese={...disabled,intelligence_schedule:{...disabled.intelligence_schedule!,enabled:true,template_ids:['cn_html']}};
  expect(channelModelControls(chinese)).toEqual({probe_enabled:true,artifact_chinese:true,artifact_english:false});
  expect(channelModelControls({id:'without-plan'} as Channel)).toEqual({probe_enabled:true,artifact_chinese:true,artifact_english:true});
 });
 it('displays a legacy plan effectively without converting an ordinary save into per-model opt-outs',()=>{
  const channel={id:'c',enabled:true,intelligence_schedule:{enabled:true,model_id:'m',template_ids:['cn_html','en_svg'],interval_seconds:1800}} as Channel;
  const model={id:'m',artifact_chinese:false,artifact_english:false,public:true,probe_enabled:false} as Model;
  expect(modelFormValues(model,channel)).toMatchObject({artifact_chinese:true,artifact_english:true});
  expect(modelUpdate(model,{display_name:'Updated',artifact_chinese:true,artifact_english:true},channel)).toMatchObject({artifact_chinese:false,artifact_english:false,model_control_changes:[]});
  expect(modelUpdate(model,{artifact_chinese:false,artifact_english:true},channel)).toMatchObject({artifact_chinese:false,artifact_english:false,model_control_changes:['artifact_chinese']});
 });
 it('combines all-model scope, group masters and explicit opt-outs when counting calls',()=>{
  const channel={id:'c',enabled:true,model_controls:{probe_enabled:true,artifact_chinese:true,artifact_english:true},intelligence_schedule:{enabled:true,model_scope:'all',model_id:'representative',template_ids:['cn_html','en_svg'],interval_seconds:3600}} as Channel;
  const model={id:'other-model',artifact_chinese:true,artifact_english:true,artifact_overrides:{english:false},probe_enabled:false} as Model;
  expect(effectiveArtifactFlags(model,channel)).toEqual({artifact_chinese:true,artifact_english:false});
  expect(modelDailyCalls({...model,...effectiveArtifactFlags(model,channel)},channel)).toEqual({probe:0,artifacts:24});
 });
 it('excludes archived records and prevents runs on archived, disabled, or missing targets',()=>{
  const channels=[{id:'active',enabled:true,archived:false},{id:'off',enabled:false,archived:false},{id:'archive',enabled:true,archived:true}] as Channel[];
  const models=[{id:'ok',channel_id:'active',archived:false,missing:false},{id:'archived',channel_id:'active',archived:true},{id:'missing',channel_id:'active',archived:false,missing:true},{id:'off',channel_id:'off',archived:false},{id:'old',channel_id:'archive',archived:false}] as Model[];
  expect(activeRecords(channels).map(c=>c.id)).toEqual(['active','off']);
  expect(availableModels(models,channels).map(m=>m.id)).toEqual(['ok']);
 });
 it('counts both health checks and artwork only for the single selected model',()=>{
  const channel={id:'c',enabled:true,model_controls:{probe_enabled:true,artifact_chinese:true,artifact_english:true},intelligence_schedule:{enabled:true,model_scope:'selected',model_id:'sol',template_ids:['cn_html','en_svg'],interval_seconds:3600}} as Channel;
  const flags={probe_enabled:true,probe_interval_seconds:300,artifact_chinese:true,artifact_english:true};
  expect(modelDailyCalls({id:'other',...flags},channel)).toEqual({probe:0,artifacts:0});
  expect(modelDailyCalls({id:'sol',...flags},channel)).toEqual({probe:288,artifacts:48});
  expect(effectiveArtifactFlags({id:'other',...flags} as Model,channel)).toEqual({artifact_chinese:false,artifact_english:false});
 });
});
