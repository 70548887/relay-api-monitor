import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChannelEditor } from './ChannelEditor';
import type { Channel, Model, Platform } from '../types';

const platform={id:'platform-fixture',name:'Fixture platform',description:'',public:true,archived:false,sort:0,created_at:'2026-10-28T00:00:00Z'} as Platform;
const channel={
 id:'channel-fixture',platform_id:platform.id,name:'Fixture group',base_url:'https://fixture.example/v1',
 key_mask:'••••1234',protocol:'responses',discovery_protocol:'openai',models_path:'/saved-models',generate_path:'',
 anthropic_version:'2023-06-01',chat_token_field:'max_completion_tokens',reasoning_effort:'high',thinking_mode:'',thinking_budget:0,
 intelligence_model_id:'model-fixture',model_controls:{probe_enabled:true,artifact_chinese:true,artifact_english:true},
 group_provider:'none',group_path:'',group_field:'',group_verification_status:'unconfigured',
 enabled:true,public:true,archived:false,sort:0,revision:3,created_at:'2026-10-28T00:00:00Z',
} as Channel;
const models=[{id:'model-fixture',channel_id:channel.id,upstream_id:'fixture-model',display_name:'Fixture model',public:true,probe_enabled:true,artifact_chinese:true,artifact_english:false,archived:false,missing:false} as Model];
const clients:QueryClient[]=[];
let originalScrollIntoView:PropertyDescriptor|undefined;

function setup(current:Channel=channel,catalogue:Model[]=models){
 const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity},mutations:{retry:false}}});clients.push(client);
 const onClose=vi.fn();
 const fetcher=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(url);
  if(path.endsWith('/sync-models'))return new Response(JSON.stringify({count:catalogue.length,models:catalogue}));
  if(init?.method==='PUT'||init?.method==='POST')return new Response(JSON.stringify({...current,...JSON.parse(String(init.body||'{}'))}));
  if(path.endsWith('/channels'))return new Response(JSON.stringify([current]));
  if(path.endsWith('/models'))return new Response(JSON.stringify(catalogue));
  if(path.endsWith('/platforms'))return new Response(JSON.stringify([platform]));
  if(path.endsWith('/intelligence-status'))return new Response(JSON.stringify({groups:[],worker_online:false}));
  return new Response(JSON.stringify([]));
 });
 vi.stubGlobal('fetch',fetcher);
 const element=(open=true,record=current,items=catalogue)=><QueryClientProvider client={client}><App><ChannelEditor open={open} channel={record} platforms={[platform]} models={items} onClose={onClose}/></App></QueryClientProvider>;
 const view=render(element());
 return {client,onClose,fetcher,rerender:(open:boolean,record=current,items=catalogue)=>view.rerender(element(open,record,items))};
}

beforeEach(()=>{
 const getStyle=window.getComputedStyle.bind(window);
 vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));
 vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));
 vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}});
 originalScrollIntoView=Object.getOwnPropertyDescriptor(HTMLElement.prototype,'scrollIntoView');
 Object.defineProperty(HTMLElement.prototype,'scrollIntoView',{configurable:true,value:vi.fn()});
 vi.spyOn(window,'scrollTo').mockImplementation(()=>{});
});
afterEach(()=>{
 cleanup();clients.splice(0).forEach(client=>client.clear());vi.restoreAllMocks();vi.unstubAllGlobals();
 if(originalScrollIntoView)Object.defineProperty(HTMLElement.prototype,'scrollIntoView',originalScrollIntoView);
 else delete (HTMLElement.prototype as unknown as Record<string,unknown>).scrollIntoView;
});

describe('tabbed channel editor',()=>{
 it('keeps edited values when switching tabs and never echoes a replacement Key in the summary',async()=>{
  setup();
  fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Renamed fixture'}});
  fireEvent.change(screen.getByLabelText('API Key'),{target:{value:'fixture-transient-key-not-real'}});
  fireEvent.click(screen.getByRole('tab',{name:'高级设置'}));
  fireEvent.change(screen.getByLabelText('模型列表路径'),{target:{value:'/custom-catalog'}});
  expect(screen.queryByLabelText('Chat Token 字段')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Anthropic Version')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('tab',{name:'推理参数'}));
  fireEvent.click(screen.getByRole('tab',{name:'基础连接'}));
  expect(screen.getByLabelText('分组名称')).toHaveValue('Renamed fixture');
  expect(screen.getByLabelText('API Key')).toHaveValue('fixture-transient-key-not-real');
  const summary=screen.getByRole('region',{name:'分组摘要'});
  expect(summary).not.toHaveTextContent('fixture-transient-key-not-real');
  fireEvent.click(screen.getByRole('tab',{name:'高级设置'}));
  expect(screen.getByLabelText('模型列表路径')).toHaveValue('/custom-catalog');
 });

 it('saves all tab values with a blank replacement Key and without resetting model exceptions',async()=>{
  const {fetcher,onClose}=setup();
  fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Saved fixture'}});
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(String(call[0])).toBe('/api/v1/admin/channels/channel-fixture');
  const body=JSON.parse(String(call[1]?.body));
  expect(body).toMatchObject({name:'Saved fixture',api_key:'',models_path:'/saved-models',intelligence_model_id:'model-fixture',selected_model_only:true,model_controls:channel.model_controls});
  expect(body).not.toHaveProperty('apply_model_controls');
  await waitFor(()=>expect(onClose).toHaveBeenCalled());
 });

 it('requires the separate explicit action to apply group switches to every model',async()=>{
  const {fetcher}=setup();
  fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
  expect(screen.getByLabelText('全部模型健康检测')).toBeChecked();
  fireEvent.click(screen.getByRole('button',{name:'保存并应用到全部模型'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({apply_model_controls:true,model_controls:channel.model_controls});
 });

 it('uses discovery protocol as well as generation protocol to reveal the relevant compatibility fields',async()=>{
  setup({...channel,protocol:'chat'});
  fireEvent.click(screen.getByRole('tab',{name:'高级设置'}));
  expect(screen.getByRole('tab',{name:'高级设置'})).toHaveAttribute('aria-selected','true');
  expect(screen.getByLabelText('Chat Token 字段')).toBeInTheDocument();
  expect(screen.queryByLabelText('Anthropic Version')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('tab',{name:'基础连接'}));
  fireEvent.mouseDown(screen.getByLabelText('模型发现协议'));
  const option=screen.getAllByText('Anthropic',{exact:true}).find(element=>element.closest('.ant-select-item-option-content'));
  expect(option).toBeTruthy();fireEvent.click(option!);
  fireEvent.click(screen.getByRole('tab',{name:'高级设置'}));
  expect(screen.getByRole('tab',{name:'高级设置'})).toHaveAttribute('aria-selected','true');
  expect(screen.getByLabelText('Anthropic Version')).toBeInTheDocument();
  expect(screen.getByLabelText('Chat Token 字段')).toBeInTheDocument();
 });

 it('refreshes persisted group and model state when saving succeeds but catalogue synchronization fails',async()=>{
  const {client,fetcher,onClose}=setup();
  const invalidate=vi.spyOn(client,'invalidateQueries');
  fetcher.mockImplementationOnce(async()=>new Response(JSON.stringify({...channel,name:'Saved before sync'})));
  fetcher.mockImplementationOnce(async()=>new Response(JSON.stringify({error:{code:'fixture_sync_error',message:'fixture catalogue unavailable'}}),{status:502}));
  fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Saved before sync'}});
  fireEvent.click(screen.getByRole('button',{name:'保存并获取模型'}));
  expect(await screen.findByText(/fixture catalogue unavailable/)).toBeInTheDocument();
  expect(fetcher.mock.calls).toHaveLength(2);
  expect(fetcher.mock.calls[0][1]?.method).toBe('PUT');
  expect(String(fetcher.mock.calls[1][0])).toBe('/api/v1/admin/channels/channel-fixture/sync-models');
  await waitFor(()=>{
   for(const key of ['admin-channels','admin-models','admin-intelligence-status']){
    expect(invalidate).toHaveBeenCalledWith({queryKey:[key]});
   }
  });
  expect(screen.getByLabelText('分组名称')).toHaveValue('Saved before sync');
  expect(onClose).not.toHaveBeenCalled();
 });

 it('opens the tab containing a validation error before any save request is sent',async()=>{
  const {fetcher}=setup({...channel,protocol:'anthropic',thinking_mode:'enabled',thinking_budget:512});
  expect(screen.getByRole('tab',{name:'基础连接'})).toHaveAttribute('aria-selected','true');
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(screen.getByRole('tab',{name:'推理参数'})).toHaveAttribute('aria-selected','true'));
  expect(await screen.findByText('预算至少为 1024，且小于最大输出 Tokens')).toBeVisible();
  expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT'||call[1]?.method==='POST')).toBe(false);
 },25000);

 it('clears transient Key and failed operation state before opening another group',async()=>{
  const {fetcher,rerender}=setup();
  fetcher.mockImplementationOnce(async()=>new Response(JSON.stringify({error:{code:'fixture_error',message:'fixture save failure'}}),{status:400}));
  fireEvent.change(screen.getByLabelText('API Key'),{target:{value:'first-group-transient-key'}});
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  expect(await screen.findByText('fixture save failure')).toBeInTheDocument();
  rerender(false);await waitFor(()=>expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  rerender(true,{...channel,id:'another-channel',name:'Second fixture',key_mask:'••••5678'});
  expect(await screen.findByLabelText('分组名称')).toHaveValue('Second fixture');
  expect(screen.getByLabelText('API Key')).toHaveValue('');
  expect(screen.queryByText('fixture save failure')).not.toBeInTheDocument();
  expect(within(screen.getByRole('region',{name:'分组摘要'})).queryByText('first-group-transient-key')).not.toBeInTheDocument();
 },25000);
 it('selects exact gpt-6.1-sol rather than the first discovered model and saves its identity',async()=>{
  const sol={...models[0],id:'sol-model',upstream_id:'gpt-6.1-sol',display_name:'Sol default'};
  const {fetcher}=setup({...channel,intelligence_model_id:''},[models[0],sol]);
  fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
  expect(screen.getByText('Sol default',{exact:true,selector:'.ant-select-selection-item'})).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({intelligence_model_id:'sol-model'});
 });
 it('allows saving and synchronizing 25 non-default models while the target remains unselected',async()=>{
  const catalogue=Array.from({length:25},(_,index)=>({...models[0],id:'other-'+index,upstream_id:'other-model-'+index,display_name:'Other model '+index}));
  const {fetcher}=setup({...channel,intelligence_model_id:''},catalogue);
  fireEvent.click(screen.getByRole('button',{name:'保存并获取模型'}));
  expect(await screen.findByText('本次已获取 25 个模型',{exact:true,selector:'.ant-tabs-tabpane-active .channel-editor-sync-success'})).toBeInTheDocument();
  const requests=fetcher.mock.calls;
  expect(requests.filter(call=>call[1]?.method==='PUT')).toHaveLength(1);
  expect(JSON.parse(String(requests.find(call=>call[1]?.method==='PUT')?.[1]?.body)).intelligence_model_id).toBe('');
  expect(requests.filter(call=>String(call[0]).endsWith('/sync-models'))).toHaveLength(1);
  expect(requests.some(call=>String(call[0]).includes('/admin/jobs'))).toBe(false);
 });
 it('preserves an unavailable configured target on a rename even when gpt-6.1-sol is available',async()=>{
  const sol={...models[0],id:'sol-model',upstream_id:'gpt-6.1-sol',display_name:'Sol default'};
  const missing={...models[0],missing:true};
  const {fetcher}=setup({...channel,intelligence_schedule:{enabled:true,model_scope:'selected',model_id:'model-fixture',template_ids:['cn_html'],interval_seconds:1800}},[missing,sol]);
  fireEvent.change(screen.getByLabelText('分组名称'),{target:{value:'Only renamed'}});
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({name:'Only renamed',intelligence_model_id:'model-fixture',intelligence_schedule:{model_id:'model-fixture'}});
 });
 it('keeps an explicit clear through a catalogue refresh and sends single-model intent for the default reset',async()=>{
  const sol={...models[0],id:'sol-model',upstream_id:'gpt-6.1-sol',display_name:'Sol default'};
  const current={...channel,intelligence_model_id:'',intelligence_schedule:{enabled:true,model_scope:'selected',model_id:'model-fixture',template_ids:['cn_html'],interval_seconds:1800}} as Channel;
  const catalogue=[models[0],sol];
  const {fetcher,rerender}=setup(current,catalogue);
  fireEvent.click(screen.getByRole('tab',{name:'模型与开关'}));
  const select=screen.getByLabelText('定时测试模型').closest('.ant-select')!;
  fireEvent.mouseDown(select.querySelector('.ant-select-clear')!);
  expect(select.querySelector('.ant-select-selection-item')).toBeNull();
  rerender(true,current,[...catalogue]);
  expect(select.querySelector('.ant-select-selection-item')).toBeNull();
  expect(screen.getByRole('region',{name:'分组摘要'})).toHaveTextContent('Sol default');
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({intelligence_model_id:'',selected_model_only:true});
 },25000);
 it('identifies a legacy all-model plan and requests narrowing even when its representative is unchanged',async()=>{
  const {fetcher}=setup({...channel,intelligence_schedule:{enabled:true,model_scope:'all',model_id:'',template_ids:['cn_html'],interval_seconds:1800}});
  expect(screen.getByRole('region',{name:'分组摘要'})).toHaveTextContent('当前仍为全部模型计划');
  fireEvent.click(screen.getByRole('button',{name:'保存更改'}));
  await waitFor(()=>expect(fetcher.mock.calls.some(call=>call[1]?.method==='PUT')).toBe(true));
  const call=fetcher.mock.calls.find(item=>item[1]?.method==='PUT')!;
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({intelligence_model_id:'model-fixture',selected_model_only:true});
 });
});
