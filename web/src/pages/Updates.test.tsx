import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpdatesPage } from './Updates';
import type { UpdateSnapshot } from '../types';

function fixture(): UpdateSnapshot {
  return {enabled:true,repository:'70548887/relay-api-monitor',launcher_managed:true,launcher_protocol:2,auto_restart:true,
    current:{version:'1.2.1',commit:'a'.repeat(40),os:'windows',arch:'amd64',build_time:'2026-10-26T00:00:00Z'},
    latest:{current_version:'1.2.1',latest_version:'v1.2.2',commit:'b'.repeat(40),compatible:true,available:true,notes:'修复升级流程',asset_name:'relaylab-windows-amd64.exe',asset_size:2048,sha256:'c'.repeat(64)},
    release:{tag_name:'v1.2.2',name:'Release v1.2.2 - 修复在线升级',assets:[{name:'relaylab-windows-amd64.exe',size:2048,sha256:'c'.repeat(64),verified:true}]},
    operation:{id:'op-1',state:'available',version:'v1.2.2',progress:0},state:'available',
    maintenance:{active:false,draining_requests:0,running_jobs:0,worker_online:true}};
}
const clients: QueryClient[]=[];
function show(data:UpdateSnapshot, accepted?:UpdateSnapshot) {
  const fetcher=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>new Response(JSON.stringify(String(url).endsWith('/history')?{history:[]}:init?.method==='POST'?(accepted||data):data),{status:init?.method==='POST'?202:200,headers:{'Content-Type':'application/json'}}));
  vi.stubGlobal('fetch',fetcher);
  const client=new QueryClient({defaultOptions:{queries:{retry:false}}});clients.push(client);
  render(<QueryClientProvider client={client}><App><UpdatesPage/></App></QueryClientProvider>);
  return fetcher;
}
beforeEach(()=>{
  const getStyle=window.getComputedStyle.bind(window);
  vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));
  vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));
  vi.stubGlobal('ResizeObserver',class {observe(){}unobserve(){}disconnect(){}});
  sessionStorage.clear();
});
afterEach(()=>{cleanup();clients.splice(0).forEach(client=>client.clear());vi.restoreAllMocks();vi.unstubAllGlobals();});

describe('online updates page',()=>{
  it('uses the snapshot contract and treats download acceptance as progress',async()=>{
    const data=fixture();const accepted=fixture();accepted.operation={id:'download-2',state:'downloading',version:'v1.2.2',progress:20};
    const fetcher=show(data,accepted);
    await screen.findByText('70548887/relay-api-monitor');
    expect(screen.getByRole('button',{name:'下载并校验'})).toBeEnabled();
    expect(screen.getByRole('button',{name:'安装升级'})).toBeDisabled();
    fireEvent.click(screen.getByRole('button',{name:'下载并校验'}));
    await waitFor(()=>expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/updates/download',expect.objectContaining({method:'POST'})));
    expect(screen.queryByText('升级完成')).not.toBeInTheDocument();
  });
  it.each(['incompatible','current','old-launcher','not-ready'])('gates unsafe action for %s',async(reason)=>{
    const data=fixture();data.operation!.state='ready';data.state='ready';
    if(reason==='incompatible')data.latest!.compatible=false;
    if(reason==='current')data.latest!.available=false;
    if(reason==='old-launcher')data.launcher_protocol=1;
    if(reason==='not-ready')data.operation!.state='available';
    show(data);await screen.findByText('70548887/relay-api-monitor');
    expect(screen.getByRole('button',{name:'安装升级'})).toBeDisabled();
    if(reason==='incompatible'||reason==='current')expect(screen.getByRole('button',{name:'下载并校验'})).toBeDisabled();
  });
  it('enables installation only for ready and managed protocol 2',async()=>{
    const data=fixture();data.operation!.state='ready';data.state='ready';show(data);
    await screen.findByText('70548887/relay-api-monitor');expect(screen.getByRole('button',{name:'安装升级'})).toBeEnabled();
  });
  it('shows persisted final errors and maintenance/download telemetry',async()=>{
    const data=fixture();data.operation={id:'failed-1',state:'failed',error_code:'health_check_failed',message:'目标 Worker 未启动',bytes_downloaded:1024,total_bytes:2048,bytes_per_second:512,eta_seconds:2};
    data.maintenance={active:true,draining_requests:3,running_jobs:2,worker_online:false};show(data);
    expect(await screen.findByText('health_check_failed')).toBeInTheDocument();
    expect(screen.getByText('下载速度：512 B/s')).toBeInTheDocument();expect(screen.getByText('预计剩余：2 秒')).toBeInTheDocument();
    expect(screen.getByText('在途请求：3')).toBeInTheDocument();expect(screen.getByText('运行任务：2')).toBeInTheDocument();
  });
  it('does not label pending restart as an installed update',async()=>{
    const data=fixture();data.auto_restart=false;data.operation!.state='pending_restart';show(data);
    await screen.findByText('升级包已准备，等待手动重启');expect(screen.getByRole('button',{name:'准备升级并等待重启'})).toBeDisabled();expect(screen.queryByText('升级完成')).not.toBeInTheDocument();
  });
  it('waits for target commit and worker even if the operation says succeeded',async()=>{
    const data=fixture();data.operation!.state='succeeded';data.operation!.progress=100;data.current.version='1.2.2';data.maintenance!.worker_online=false;show(data);
    await screen.findByText('70548887/relay-api-monitor');expect(screen.getAllByText('安装结果已返回，正在确认目标版本与 Worker').length).toBeGreaterThan(0);expect(screen.queryByText('升级完成')).not.toBeInTheDocument();
    expect(document.querySelector('.update-operation-panel .ant-progress-status-success')).toBeNull();
  });
});
