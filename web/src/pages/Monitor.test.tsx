import {act,cleanup,fireEvent,render,screen,waitFor,within} from '@testing-library/react';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest';
import Monitor from './Monitor';
import type {ModelView,MonitorData,Platform,ProbeResult} from '../types';

const {request}=vi.hoisted(()=>({request:vi.fn()}));
vi.mock('../api',()=>({api:request}));
const clients:QueryClient[]=[];
const baseTime='2026-10-28T08:40:00Z';
function model(id:string,platformId:string,overrides:Partial<ModelView>={}):ModelView{
 return {
  id,channel_id:`group-${platformId}`,platform_id:platformId,
  platform_name:platformId==='alpha'?'Alpha':'Beta',channel_name:platformId==='alpha'?'alpha-group':'beta-group',
  display_name:'shared-model',upstream_id:'shared-model',status:'ok',stale:false,running:false,
  probe_enabled:true,protocol:'responses',success_rate:100,samples:20,
  last_probe:{id:`probe-${id}`,model_id:id,status:'ok',http_status:200,first_text_ms:1200,duration_ms:3400,created_at:baseTime,returned_model:'shared-model'} as ProbeResult,
  timeline:Array.from({length:24},(_,i)=>({at:new Date(Date.parse(baseTime)-(23-i)*3600000).toISOString(),status:i<3?'unknown':'ok',count:i<3?0:3})),
  ...overrides,
 } as ModelView;
}
function fixture():MonitorData{
 const models=[
  model('alpha-ok','alpha'),
  model('alpha-slow','alpha',{display_name:'slow-model',upstream_id:'slow-model',status:'slow'}),
  model('alpha-unknown','alpha',{display_name:'unobserved-model',upstream_id:'unobserved-model',status:'unknown',stale:true,success_rate:null,samples:0,last_probe:null,timeline:[]}),
  model('alpha-disabled','alpha',{display_name:'disabled-model',upstream_id:'disabled-model',status:'disabled',probe_enabled:false,success_rate:null,samples:0,last_probe:null,timeline:[]}),
  model('beta-failed','beta',{status:'failed',success_rate:70,samples:10,last_probe:{id:'failed-probe',model_id:'beta-failed',status:'failed',http_status:429,first_text_ms:null,duration_ms:2000,created_at:baseTime,error_code:'rate_limited',error_message:'上游检测失败'} as ProbeResult}),
  model('beta-stale','beta',{display_name:'stale-model',upstream_id:'stale-model',stale:true}),
 ];
 return {platforms:[{id:'alpha',name:'Alpha',public:true},{id:'beta',name:'Beta',public:true}] as Platform[],channels:[{id:'group-alpha',platform_id:'alpha',name:'alpha-group',sort:0},{id:'group-beta',platform_id:'beta',name:'beta-group',sort:0}],models,summary:{platforms:2,models:6,normal:2,slow:1,failed:1,stale:2},updated_at:baseTime};
}
function setup(data:MonitorData|null=fixture()){
 const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});clients.push(client);
 if(data)client.setQueryData<MonitorData>(['monitor'],data);
 render(<QueryClientProvider client={client}><Monitor/></QueryClientProvider>);
 return client;
}
function navigation(){return screen.getByRole('group',{name:'平台导航'})}
function results(){return document.querySelector('.monitor-results') as HTMLElement}
beforeEach(()=>{
 const getStyle=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));
 vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));
 vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}});
 request.mockReset();request.mockRejectedValue(new Error('Unexpected fixture request'));
});
afterEach(()=>{cleanup();clients.splice(0).forEach(client=>client.clear());vi.restoreAllMocks();vi.unstubAllGlobals()});

describe('monitor platform workbench',()=>{
 it('searches globally across platforms and preserves the complete platform navigation',()=>{
  setup();fireEvent.click(within(navigation()).getByRole('button',{name:'Beta'}));
  expect(within(results()).getAllByText('beta-group').length).toBeGreaterThan(0);
  fireEvent.change(screen.getByRole('textbox',{name:'搜索平台、分组或模型'}),{target:{value:'slow-model'}});
  expect(within(results()).getAllByText('slow-model').length).toBeGreaterThan(0);
  expect(within(results()).queryByText('beta-group')).not.toBeInTheDocument();
  expect(within(navigation()).getByRole('button',{name:'Beta'})).toBeInTheDocument();
  expect(within(navigation()).getByRole('button',{name:'Alpha'})).toBeInTheDocument();
 });

 it('filters failures, slow results and stale data without treating disabled models as failed',()=>{
  setup();fireEvent.click(screen.getByRole('switch',{name:'仅看异常'}));
  expect(within(results()).getAllByText('slow-model').length).toBeGreaterThan(0);
  expect(within(results()).queryByText('unobserved-model')).not.toBeInTheDocument();
  expect(within(results()).queryByText('disabled-model')).not.toBeInTheDocument();
  fireEvent.click(within(navigation()).getByRole('button',{name:'Beta'}));
  expect(within(results()).getAllByText('shared-model').length).toBeGreaterThan(0);
  expect(within(results()).getAllByText('stale-model').length).toBeGreaterThan(0);
  expect(within(results()).getAllByText(/数据过期/).length).toBeGreaterThan(0);
 });

 it('distinguishes unobserved and disabled models and never invents a success rate for no samples',()=>{
  setup();
  const scope=within(results());
  expect(scope.getAllByText('unobserved-model').length).toBeGreaterThan(0);
  expect(scope.getAllByText('disabled-model').length).toBeGreaterThan(0);
  expect(scope.getAllByText(/待观测/).length).toBeGreaterThan(0);
  expect(scope.getAllByText(/停用/).length).toBeGreaterThan(0);
  const unknownRow=Array.from(results().querySelectorAll('tr')).find(row=>row.textContent?.includes('unobserved-model'));
  expect(unknownRow).toBeTruthy();expect(unknownRow!.textContent).toContain('—');expect(unknownRow!.textContent).not.toContain('100.0%');
  expect(screen.getByRole('switch',{name:'自动刷新'})).toBeChecked();
 });

 it('loads history for the selected cross-platform model identity and includes its platform context',async()=>{
  request.mockImplementation((path:string)=>path==='/public/models/beta-failed/history'?Promise.resolve([{id:'history-beta',model_id:'beta-failed',status:'failed',http_status:429,first_text_ms:null,duration_ms:2000,created_at:baseTime,error_message:'上游检测失败'}]):Promise.reject(new Error('Unexpected fixture request')));
  setup();fireEvent.click(within(navigation()).getByRole('button',{name:'Beta'}));
  fireEvent.click(screen.getAllByRole('button',{name:'查看 shared-model 检测历史'})[0]);
  const dialog=await screen.findByRole('dialog');
  await waitFor(()=>expect(request.mock.calls.some(call=>call[0]==='/public/models/beta-failed/history')).toBe(true));
  expect(within(dialog).getAllByText(/Beta/).length).toBeGreaterThan(0);
  expect(within(dialog).getAllByText(/beta-group/).length).toBeGreaterThan(0);
  expect(request.mock.calls.some(call=>call[0]==='/public/models/alpha-ok/history')).toBe(false);
 });

 it('keeps an explicitly chosen platform across background data refreshes',async()=>{
  const data=fixture(),client=setup(data);
  fireEvent.click(within(navigation()).getByRole('button',{name:'Beta'}));
  await act(async()=>{client.setQueryData<MonitorData>(['monitor'],{...data,platforms:[...data.platforms].reverse(),updated_at:'2026-10-28T08:40:30Z'});});
  await waitFor(()=>expect(within(results()).getAllByText('beta-group').length).toBeGreaterThan(0));
  expect(within(results()).queryByText('alpha-group')).not.toBeInTheDocument();
 });

 it('shows empty and failed reads without presenting missing data as a healthy result',async()=>{
  const empty=fixture();empty.platforms=[];empty.channels=[];empty.models=[];empty.summary={platforms:0,models:0,normal:0,slow:0,failed:0,stale:0};
  const client=setup(empty);expect(screen.getAllByText(/暂无公开|还没有公开/).length).toBeGreaterThan(0);
  cleanup();client.clear();request.mockRejectedValue(new Error('monitor fixture unavailable'));
  setup(null);
  expect(await screen.findByText('monitor fixture unavailable')).toBeInTheDocument();
 });
});
