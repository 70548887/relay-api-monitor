import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChannelScheduleControl, RunChannelTestButton, groupScheduleEnabled } from './ChannelTesting';
import type { Channel, Model } from '../types';

const channel={id:'c',name:'Group A',enabled:true,archived:false,intelligence_model_id:'m1'} as Channel;
const models=[{id:'m1',channel_id:'c',upstream_id:'first',display_name:'First model',archived:false,missing:false},{id:'m2',channel_id:'c',upstream_id:'second',display_name:'Second model',archived:false,missing:false},{id:'foreign',channel_id:'other',display_name:'Foreign model',archived:false,missing:false}] as Model[];
const templates=[{id:'cn_html',name:'中文动画',prompt:'中文原文'},{id:'en_svg',name:'英文 SVG',prompt:'English original'}];
const clients:QueryClient[]=[];
function show(element:React.ReactNode,fail=false){
 const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});clients.push(client);client.setQueryData(['admin-templates'],templates);
 const fetcher=vi.fn(async(_url:RequestInfo|URL,init?:RequestInit)=>new Response(JSON.stringify(fail?{error:{code:'conflict',message:'已有测试任务正在执行'}}:init?.method?{id:'task-12345678'}:templates),{status:fail?409:200,headers:{'Content-Type':'application/json'}}));vi.stubGlobal('fetch',fetcher);
 render(<QueryClientProvider client={client}><App>{element}</App></QueryClientProvider>);return fetcher;
}
beforeEach(()=>{const getStyle=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();clients.splice(0).forEach(c=>c.clear());vi.restoreAllMocks();vi.unstubAllGlobals()});

describe('group testing controls',()=>{
 it('submits only the selected model and template as a one-off artifact job',async()=>{
  const fetcher=show(<RunChannelTestButton channel={channel} models={models}/>);
  fireEvent.click(screen.getByRole('button',{name:'执行测试'}));
  fireEvent.mouseDown(screen.getByLabelText('测试模型'));expect(screen.queryByText('Foreign model')).not.toBeInTheDocument();fireEvent.click(screen.getByText('Second model',{exact:true}));
  fireEvent.mouseDown(screen.getByLabelText('测试模板'));fireEvent.click(screen.getByText('英文 SVG',{exact:true}));
  expect(screen.getByText('English original')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button',{name:'提交测试任务'}));
  await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/jobs',expect.objectContaining({method:'POST',body:JSON.stringify({model_id:'m2',template_id:'en_svg',kind:'artifact'})})));
  expect(await screen.findByText('查看任务进度')).toBeInTheDocument();
 });
 it('keeps a failed submission visible for correction',async()=>{
  show(<RunChannelTestButton channel={channel} models={models}/>,true);fireEvent.click(screen.getByRole('button',{name:'执行测试'}));fireEvent.click(screen.getByRole('button',{name:'提交测试任务'}));
  expect(await screen.findByText('已有测试任务正在执行')).toBeInTheDocument();await waitFor(()=>expect(screen.getByRole('dialog')).toBeVisible());
 });
 it('saves a group schedule only after model, templates and interval are confirmed',async()=>{
  const fetcher=show(<ChannelScheduleControl channel={channel} models={models}/>);
  fireEvent.click(screen.getByRole('switch',{name:'Group A 分组定时测试'}));expect(fetcher).not.toHaveBeenCalled();
  fireEvent.mouseDown(screen.getByLabelText('定时测试模型'));fireEvent.click(screen.getByText('Second model',{exact:true}));
  fireEvent.mouseDown(screen.getByLabelText('定时测试模板'));fireEvent.click(screen.getByText('英文 SVG',{exact:true}));
  fireEvent.change(screen.getByLabelText('测试间隔（秒）'),{target:{value:'3600'}});
  fireEvent.click(screen.getByRole('button',{name:'保存定时设置'}));
  await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/channels/c/intelligence-schedule',expect.objectContaining({method:'PUT',body:JSON.stringify({enabled:true,model_id:'m2',template_ids:['cn_html','en_svg'],interval_seconds:3600})})));
 });
 it('disables a saved plan without changing model or template settings',async()=>{
  const configured={...channel,intelligence_schedule:{enabled:true,model_id:'m1',template_ids:['cn_html'],interval_seconds:1800}};
  const fetcher=show(<ChannelScheduleControl channel={configured} models={models}/>);fireEvent.click(screen.getByRole('switch',{name:'Group A 分组定时测试'}));
  await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/channels/c/intelligence-schedule',expect.objectContaining({method:'PUT',body:'{"enabled":false}'})));expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
 });
 it('respects an explicit off switch over legacy per-model flags',()=>{
  const legacy=[{...models[0],artifact_chinese:true}];expect(groupScheduleEnabled(channel,legacy)).toBe(true);expect(groupScheduleEnabled({...channel,intelligence_schedule:{enabled:false,model_id:'m1',template_ids:['cn_html'],interval_seconds:1800}},legacy)).toBe(false);
 });
});
