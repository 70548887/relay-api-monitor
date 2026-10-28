import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {MemoryRouter} from 'react-router-dom';
import { ModelsPage, PlatformsPage, SettingsPage } from './Admin';
import type { Model } from '../types';

beforeEach(()=>{const getStyle=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.restoreAllMocks();vi.unstubAllGlobals()});
describe('model configuration form',()=>{
 it('uses the backend limits on the settings page instead of offering rejected values',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});
  client.setQueryData(['admin-settings'],{probe_concurrency:4,artifact_concurrency:2,channel_concurrency:2,probe_timeout_seconds:60,artifact_timeout_seconds:600,retention_days:30,visitor_ip_concurrency:2});
  render(<QueryClientProvider client={client}><App><SettingsPage/></App></QueryClientProvider>);
  expect(screen.getByLabelText('健康检测并发')).toHaveAttribute('aria-valuemax','32');
  expect(screen.getByLabelText('作品生成并发')).toHaveAttribute('aria-valuemax','16');
  expect(screen.getByLabelText('单分组并发')).toHaveAttribute('aria-valuemax','16');
  expect(screen.getByLabelText('检测超时（秒）')).toHaveAttribute('aria-valuemin','5');
  expect(screen.getByLabelText('创作超时（秒）')).toHaveAttribute('aria-valuemin','5');
  expect(screen.getByLabelText('检测数据保留（天）')).toHaveAttribute('aria-valuemax','365');
  expect(screen.getByLabelText('每 IP 并发')).toHaveAttribute('aria-valuemax','8');client.clear();
 });
 it('allows per-model artwork opt-out while keeping the group interval authoritative',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[{id:'m',channel_id:'c',upstream_id:'fixture',display_name:'Fixture',archived:false,artifact_chinese:true,artifact_english:true,artifact_interval_seconds:1800}]);client.setQueryData(['admin-channels'],[{id:'c',name:'Group',enabled:true,archived:false,protocol:'chat',intelligence_schedule:{enabled:true,model_id:'m',template_ids:['cn_html','en_svg'],interval_seconds:3600}}]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600});
  render(<QueryClientProvider client={client}><App><MemoryRouter><ModelsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:/^编辑$/}));
  expect(screen.getByText('分组定时创作：已开启')).toBeInTheDocument();expect(screen.getByLabelText('中文 HTML 创作')).not.toBeDisabled();expect(screen.getByLabelText('英文 SVG 创作')).not.toBeDisabled();expect(screen.getByLabelText('创作间隔（秒）')).toBeDisabled();expect(screen.getByLabelText('周期健康检测')).not.toBeDisabled();expect(screen.getByText('保存后预计每日自动请求约 48 次')).toBeInTheDocument();client.clear();
 });
 it('distinguishes inherited effort from upstream default and constrains thinking by protocol',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[{id:'c',name:'Anthropic fixture',protocol:'anthropic',enabled:true,archived:false}]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600});render(<QueryClientProvider client={client}><App><MemoryRouter><ModelsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));fireEvent.mouseDown(screen.getByLabelText('所属分组'));fireEvent.click(screen.getByText('Anthropic fixture',{exact:true}));expect(screen.getByLabelText('Thinking 模式')).toBeInTheDocument();fireEvent.mouseDown(screen.getByLabelText('推理强度'));fireEvent.click(screen.getByText('上游默认（覆盖分组）',{exact:true}));expect(screen.getAllByText('上游默认（覆盖分组）').length).toBeGreaterThan(0);fireEvent.mouseDown(screen.getByLabelText('生成协议'));fireEvent.click(screen.getByText('OpenAI Responses',{exact:true}));expect(screen.queryByLabelText('Thinking 模式')).not.toBeInTheDocument();expect(screen.getByLabelText('创作请求超时（秒）')).toHaveValue('600');expect(screen.getByLabelText('周期健康检测')).toBeChecked();client.clear();
 });
 it('defaults new models to enabled public monitoring and both artwork templates',async()=>{
  const fetcher=vi.fn().mockRejectedValue(new Error('network should not be used'));vi.stubGlobal('fetch',fetcher);const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[]);client.setQueryData(['admin-settings'],{probe_interval_seconds:300,artifact_interval_seconds:1800,artifact_timeout_seconds:180});
  render(<QueryClientProvider client={client}><App><MemoryRouter><ModelsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));
  expect(await screen.findByText('保存后预计每日自动请求约 384 次')).toBeInTheDocument();expect(screen.getByLabelText('周期健康检测')).toBeChecked();expect(screen.getByLabelText('中文 HTML 创作')).toBeChecked();expect(screen.getByLabelText('英文 SVG 创作')).toBeChecked();expect(fetcher).not.toHaveBeenCalled();client.clear();
 });
});

it('saves an edited group before fetching only its model catalogue',async()=>{
 const platform={id:'platform-new',name:'Fixture Platform',archived:false,public:true};
 const channel={id:'channel-new',platform_id:platform.id,name:'Fixture Group',base_url:'https://fixture.example/v1',protocol:'chat',discovery_protocol:'openai',enabled:true,public:true,archived:false,revision:1,key_mask:'••••1234'};
 const model={id:'model-new',channel_id:channel.id,upstream_id:'model-from-fixture',display_name:'model-from-fixture',archived:false,missing:false,public:false};
 const fetcher=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(url),method=init?.method||'GET';
  if(path==='/api/v1/admin/platforms')return new Response(JSON.stringify([platform]));
  if(path==='/api/v1/admin/channels'&&method==='GET')return new Response(JSON.stringify([channel]));
  if(path==='/api/v1/admin/channels/channel-new'&&method==='PUT')return new Response(JSON.stringify(channel));
  if(path==='/api/v1/admin/models'&&method==='GET')return new Response(JSON.stringify([]));
  if(path==='/api/v1/admin/models'&&method==='POST')return new Response(JSON.stringify([model]));
  if(path==='/api/v1/admin/intelligence-status')return new Response(JSON.stringify({groups:[],worker_online:true,maintenance:false,updated_at:new Date().toISOString()}));
  if(path==='/api/v1/admin/templates')return new Response(JSON.stringify([{id:'cn_html',name:'中文 HTML 动画',prompt:'fixture prompt'}]));
  if(path==='/api/v1/admin/channels/channel-new/sync-models')return new Response(JSON.stringify({count:1,models:[model]}));
  throw new Error(`unexpected request ${method} ${path}`);
 });
 vi.stubGlobal('fetch',fetcher);
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={['/admin/platforms?channel=channel-new']}><PlatformsPage/></MemoryRouter></App></QueryClientProvider>);
 fireEvent.click(await screen.findByRole('tab',{name:/分组/}));
 fireEvent.click(await screen.findByRole('button',{name:'编辑'}));
 fireEvent.click(await screen.findByRole('button',{name:'保存并获取模型'}));
 await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/channels/channel-new/sync-models',expect.objectContaining({method:'POST'})));
 expect(fetcher.mock.calls.filter(call=>String(call[0]).includes('/sync-models'))).toHaveLength(1);
 expect(client.getQueryData<Model[]>(['admin-models'])).toEqual([model]);
 fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
 fireEvent.mouseDown(screen.getByLabelText('定时测试模型'));expect(await screen.findByRole('option',{name:'model-from-fixture'})).toBeInTheDocument();
 client.clear();
},25000);

it('creates a new group first, then fetches and selects only its models',async()=>{
 const platform={id:'platform-new',name:'Fixture Platform',archived:false,public:true};
 const channel={id:'channel-new',platform_id:platform.id,name:'Fixture Group',base_url:'https://fixture.example/v1',protocol:'chat',discovery_protocol:'openai',enabled:true,public:true,archived:false,revision:1,key_mask:'••••1234'};
 const model={id:'model-new',channel_id:channel.id,upstream_id:'model-from-fixture',display_name:'model-from-fixture',archived:false,missing:false,public:false};
 let saved=false;
 const fetcher=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(url),method=init?.method||'GET';
  if(path==='/api/v1/admin/platforms')return new Response(JSON.stringify([platform]));
  if(path==='/api/v1/admin/channels'&&method==='GET')return new Response(JSON.stringify(saved?[channel]:[]));
  if(path==='/api/v1/admin/channels'&&method==='POST'){saved=true;return new Response(JSON.stringify(channel))}
  if(path==='/api/v1/admin/models'&&method==='GET')return new Response(JSON.stringify([]));
  if(path==='/api/v1/admin/models'&&method==='POST')return new Response(JSON.stringify([model]));
  if(path==='/api/v1/admin/intelligence-status')return new Response(JSON.stringify({groups:[],worker_online:true,maintenance:false,updated_at:new Date().toISOString()}));
  if(path==='/api/v1/admin/channels/channel-new/sync-models')return new Response(JSON.stringify({count:1,models:[model]}));
  throw new Error('unexpected request '+method+' '+path);
 });
 vi.stubGlobal('fetch',fetcher);const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 render(<QueryClientProvider client={client}><App><MemoryRouter initialEntries={['/admin/platforms']}><PlatformsPage/></MemoryRouter></App></QueryClientProvider>);
 fireEvent.click(await screen.findByRole('tab',{name:/分组/}));fireEvent.click(screen.getByRole('button',{name:'新增分组'}));
 fireEvent.mouseDown(screen.getByLabelText('所属平台'));fireEvent.click((await screen.findAllByText('Fixture Platform',{exact:true})).at(-1)!);
 fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Fixture Group'}});
 fireEvent.change(screen.getByLabelText('接口地址'),{target:{value:'https://fixture.example/v1'}});
 fireEvent.change(screen.getByLabelText('API Key'),{target:{value:'fixture-secret-key'}});
 fireEvent.click(screen.getByRole('button',{name:'保存并获取模型'}));
 await waitFor(()=>expect(fetcher.mock.calls.some(call=>String(call[0])==='/api/v1/admin/channels/channel-new/sync-models')).toBe(true));
 const calls=fetcher.mock.calls.map(call=>String(call[0]));
 expect(calls.indexOf('/api/v1/admin/channels')).toBeLessThan(calls.indexOf('/api/v1/admin/channels/channel-new/sync-models'));
 expect(calls.filter(path=>path.includes('/sync-models'))).toEqual(['/api/v1/admin/channels/channel-new/sync-models']);
 expect(client.getQueryData<Model[]>(['admin-models'])).toEqual([model]);
 fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
 fireEvent.mouseDown(screen.getByLabelText('定时测试模型'));
 expect(await screen.findByRole('option',{name:'model-from-fixture'})).toBeInTheDocument();client.clear();
});

it('keeps a group-owned model public switch editable and saves only explicit visibility',async()=>{
 const model={id:'plus-model',channel_id:'plus-channel',upstream_id:'gpt-6.1-sol',display_name:'gpt-6.1-sol',source:'synced',public:false,archived:false,missing:false,protocol:'responses',artifact_chinese:false,artifact_english:false,probe_enabled:false,max_tokens:8192,artifact_interval_seconds:1800,timeout_seconds:600,sort:0};
 const channel={id:'plus-channel',name:'plus线路',enabled:true,archived:false,public:true,protocol:'responses',intelligence_schedule:{enabled:true,model_id:'plus-model',template_ids:['cn_html'],interval_seconds:1800}};
 const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[model]);client.setQueryData(['admin-channels'],[channel]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600,probe_interval_seconds:300,artifact_interval_seconds:1800});
 const fetcher=vi.fn(async(_url:RequestInfo|URL,init?:RequestInit)=>new Response(JSON.stringify(init?.method==='PUT'?{...model,...JSON.parse(String(init.body))}:[]),{headers:{'Content-Type':'application/json'}}));vi.stubGlobal('fetch',fetcher);
 render(<QueryClientProvider client={client}><App><MemoryRouter><ModelsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:/^编辑$/}));
 const visible=screen.getByLabelText('公开展示');expect(visible).not.toBeDisabled();expect(visible).not.toBeChecked();fireEvent.click(visible);expect(visible).toBeChecked();
 const confirm=screen.getAllByRole('button').find(button=>button.classList.contains('ant-btn-primary')&&button.closest('.ant-modal-footer'));expect(confirm).toBeTruthy();fireEvent.click(confirm!);
 await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/models/plus-model',expect.objectContaining({method:'PUT'})));
 const body=JSON.parse(String(fetcher.mock.calls.find(call=>call[1]?.method==='PUT')?.[1]?.body));expect(body.public).toBe(true);expect(body.artifact_chinese).toBe(false);expect(body.model_control_changes).toEqual(['public']);client.clear();
});

it('saves a legacy group-owned template opt-out without changing the other inherited template',async()=>{
 const model={id:'legacy-model',channel_id:'legacy-channel',upstream_id:'fixture',display_name:'Legacy fixture',public:true,archived:false,missing:false,protocol:'responses',artifact_chinese:false,artifact_english:false,probe_enabled:false,max_tokens:8192,artifact_interval_seconds:1800,timeout_seconds:600,sort:0};
 const channel={id:'legacy-channel',name:'Legacy group',enabled:true,archived:false,public:true,protocol:'responses',intelligence_schedule:{enabled:true,model_id:'legacy-model',template_ids:['cn_html','en_svg'],interval_seconds:1800}};
 const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[model]);client.setQueryData(['admin-channels'],[channel]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600,probe_interval_seconds:300,artifact_interval_seconds:1800});
 const fetcher=vi.fn(async(_url:RequestInfo|URL,init?:RequestInit)=>new Response(JSON.stringify(init?.method==='PUT'?model:[]),{headers:{'Content-Type':'application/json'}}));vi.stubGlobal('fetch',fetcher);
 render(<QueryClientProvider client={client}><App><MemoryRouter><ModelsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:/^编辑$/}));
 expect(screen.getByLabelText('中文 HTML 创作')).toBeChecked();expect(screen.getByLabelText('英文 SVG 创作')).toBeChecked();fireEvent.click(screen.getByLabelText('中文 HTML 创作'));
 const confirm=screen.getAllByRole('button').find(button=>button.classList.contains('ant-btn-primary')&&button.closest('.ant-modal-footer'));fireEvent.click(confirm!);
 await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
 const body=JSON.parse(String(fetcher.mock.calls.find(call=>call[1]?.method==='PUT')?.[1]?.body));expect(body.model_control_changes).toEqual(['artifact_chinese']);expect(body.artifact_chinese).toBe(false);expect(body.artifact_english).toBe(false);expect(body.artifact_overrides).toBeUndefined();client.clear();
});

it.each([false,true])('saves group controls with explicit apply-all = %s',async(applyAll)=>{
 const platform={id:'p',name:'Platform',public:true,archived:false};
 const channel={id:'c',platform_id:'p',name:'Group',base_url:'https://fixture.example/v1',protocol:'chat',discovery_protocol:'openai',enabled:true,public:true,archived:false,revision:1,key_mask:'••••1234',intelligence_model_id:'m',model_controls:{probe_enabled:true,artifact_chinese:true,artifact_english:true},group_provider:'none',max_tokens:8192};
 const model={id:'m',channel_id:'c',upstream_id:'fixture',public:false,probe_enabled:false,artifact_chinese:false,artifact_english:false,archived:false};
 const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});client.setQueryData(['admin-platforms'],[platform]);client.setQueryData(['admin-channels'],[channel]);client.setQueryData(['admin-models'],[model]);client.setQueryData(['admin-intelligence-status'],{groups:[],worker_online:true});
 const fetcher=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{const path=String(url);return new Response(JSON.stringify(init?.method==='PUT'?channel:path.endsWith('/platforms')?[platform]:path.endsWith('/channels')?[channel]:path.endsWith('/models')?[model]:{groups:[],worker_online:true}),{headers:{'Content-Type':'application/json'}})});vi.stubGlobal('fetch',fetcher);
 render(<QueryClientProvider client={client}><App><MemoryRouter><PlatformsPage/></MemoryRouter></App></QueryClientProvider>);fireEvent.click(screen.getByRole('tab',{name:/分组/}));fireEvent.click(screen.getByRole('button',{name:'编辑'}));
 fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Renamed'}});
 fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
 expect(screen.getByLabelText('全部模型健康检测')).toBeChecked();
 if(applyAll)fireEvent.click(screen.getByRole('button',{name:'保存并应用到全部模型'}));
 else{const confirm=screen.getAllByRole('button').find(button=>button.classList.contains('ant-btn-primary')&&button.closest('.ant-modal-footer'));fireEvent.click(confirm!)}
 await waitFor(()=>expect(fetcher.mock.calls.filter(call=>call[1]?.method==='PUT')).toHaveLength(1));
 const body=JSON.parse(String(fetcher.mock.calls.find(call=>call[1]?.method==='PUT')?.[1]?.body));
 expect(body.model_controls).toEqual(channel.model_controls);
 if(applyAll)expect(body.apply_model_controls).toBe(true);else expect(body).not.toHaveProperty('apply_model_controls');
 client.clear();
},25000);
