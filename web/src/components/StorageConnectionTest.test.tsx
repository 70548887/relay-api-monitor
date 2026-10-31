import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { ConfigProvider, Form, Input } from 'antd';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { StorageConnectionTest, STORAGE_TEST_TIMEOUT_MS } from './StorageConnectionTest';
import { backupAPI } from '../backupApi';
import type { BackupStorage, BackupStorageInput } from '../backupApi';
import { setSession, useSession } from '../api';

const input: BackupStorageInput={endpoint:'https://storage.example',region:'auto',bucket:'backups',prefix:'backups',force_path_style:false,access_key_id:'',secret_access_key:''};
const storage={...input,id:'storage',revision:1,secret_configured:true};
const session={user:{id:'admin',username:'admin'},csrf_token:'csrf'} as Parameters<typeof setSession>[0];
function Session(){const current=useSession();return <output data-testid="session">{current?.user.id||'signed-out'}</output>;}
function Harness({onSettled=()=>{},invalid=false,storageConfig=storage}:{onSettled?:()=>Promise<void>|void;invalid?:boolean;storageConfig?:BackupStorage}){
  const [form]=Form.useForm<BackupStorageInput>();
  const [editVersion,setEditVersion]=useState(0);
  return <ConfigProvider theme={{token:{motion:false}}}><Session/><Form form={form} initialValues={{...input,bucket:invalid?'':input.bucket}} onValuesChange={()=>setEditVersion(version=>version+1)}>{Object.keys(input).filter(key=>key!=='bucket').map(key=><Form.Item hidden key={key} name={key}><Input/></Form.Item>)}<Form.Item name="bucket" label="存储桶" rules={[{required:true,message:'请填写存储桶'}]}><Input/></Form.Item><StorageConnectionTest form={form} storage={storageConfig} editVersion={editVersion} onRunningChange={()=>{}} onSettled={onSettled}/></Form></ConfigProvider>;
}
beforeEach(()=>{setSession(session);vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}));const getStyle=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));});
afterEach(()=>{cleanup();setSession(null);vi.restoreAllMocks();vi.unstubAllGlobals();vi.useRealTimers();});
const response=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
describe('storage connection feedback',()=>{
  it('keeps a local failed result and refreshes health after the request',async()=>{
    const settled=vi.fn();const fetcher=vi.fn().mockResolvedValue(response({error:{code:'permission_denied',message:'存储权限不足'},result:{status:'failed',stage:'writing',duration_ms:1240,http_status:403,request_id:'diagnostic-fixture',test_scope:'saved',storage_id:'storage',storage_revision:1,cleanup_error_code:'cleanup_denied',cleanup_message:'删除权限不足'}} ,502));vi.stubGlobal('fetch',fetcher);
    const view=render(<Harness onSettled={settled}/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));
    expect(await screen.findByText('连接测试失败')).toBeInTheDocument();expect(screen.getByText('存储权限不足')).toBeInTheDocument();expect(screen.getByText('上游 HTTP：403')).toBeInTheDocument();expect(screen.getByText('服务端耗时：1.2 秒')).toBeInTheDocument();expect(screen.getByText('diagnostic-fixture')).toBeInTheDocument();expect(screen.getByText(/测试对象清理未完成/)).toBeInTheDocument();await waitFor(()=>expect(settled).toHaveBeenCalledOnce());
    expect(JSON.parse(fetcher.mock.calls[0][1].body).secret_access_key).toBe('');expect(screen.getByText(/已保存配置/)).toBeInTheDocument();expect(screen.getByTestId('session')).toHaveTextContent('admin');
    view.rerender(<Harness onSettled={settled}/>);expect(screen.getByText('连接测试失败')).toBeInTheDocument();
    expect(screen.getByText('服务端阶段：写入测试对象')).toBeInTheDocument();
  });
  it('blocks duplicate submissions and aborts when unmounted',async()=>{
    let signal:AbortSignal|undefined;const fetcher=vi.fn((_url,init)=>{signal=init.signal;return new Promise(()=>{});});vi.stubGlobal('fetch',fetcher);
    const view=render(<Harness/>);const button=screen.getByRole('button',{name:'测试连接'});fireEvent.click(button);fireEvent.click(button);await waitFor(()=>expect(fetcher).toHaveBeenCalledOnce());expect(button).toBeDisabled();expect(screen.getByText('连接测试进行中')).toBeInTheDocument();view.unmount();expect(signal?.aborted).toBe(true);
  });
  it('shows and focuses invalid configuration without sending any request',async()=>{
    const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);render(<Harness invalid/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));expect(await screen.findByText('配置需要完善，尚未发送请求')).toBeInTheDocument();expect(screen.getByLabelText('存储桶')).toHaveFocus();expect(fetcher).not.toHaveBeenCalled();
  });
  it('reports client timeout as unknown and never automatically retries',async()=>{
    vi.useFakeTimers();const settled=vi.fn();const fetcher=vi.fn((_url,init)=>new Promise((_resolve,reject)=>init.signal.addEventListener('abort',()=>reject(new DOMException('aborted','AbortError')))));vi.stubGlobal('fetch',fetcher);render(<Harness onSettled={settled}/>);
    await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await vi.advanceTimersByTimeAsync(100);});expect(fetcher).toHaveBeenCalledOnce();await act(async()=>{await vi.advanceTimersByTimeAsync(STORAGE_TEST_TIMEOUT_MS);});expect(screen.getByText('连接测试结果待核对')).toBeInTheDocument();expect(screen.getByText(/服务端结果未知/)).toBeInTheDocument();expect(settled).toHaveBeenCalledOnce();expect(fetcher).toHaveBeenCalledOnce();
  });
  it('preserves diagnostic result when refreshing health fails',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true})));render(<Harness onSettled={()=>{throw new Error('offline');}}/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));expect(await screen.findByText('连接测试通过，配置归属待核对')).toBeInTheDocument();expect(await screen.findByText(/存储健康状态刷新失败/)).toBeInTheDocument();expect(screen.getByText('服务端耗时：未提供')).toBeInTheDocument();expect(screen.getByText(/服务端未返回测试配置归属/).closest('.ant-alert')).toHaveClass('ant-alert-warning');
  });
  it('keeps a confirmed result current after an identical save and health refresh',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true,result:{status:'ok',stage:'completed',message:'读写删除完成',test_scope:'saved',storage_id:'storage',storage_revision:1}})));const view=render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));expect(await screen.findByText('连接测试通过')).toBeInTheDocument();
    view.rerender(<Harness storageConfig={{...storage,health:{status:'ok',message:'连接验证通过'}}}/>);expect(screen.getByText('连接测试通过').closest('.ant-alert')).toHaveClass('ant-alert-success');expect(screen.getByText('测试对象：已保存配置 · 修订 1')).toBeInTheDocument();
  });
  it('marks old diagnostics as historical after another configuration revision replaces it',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true,result:{status:'ok',stage:'completed',message:'旧连接已验证',test_scope:'saved',storage_id:'storage',storage_revision:1}})));const view=render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await screen.findByText('连接测试通过');view.rerender(<Harness storageConfig={{...storage,id:'new-storage',revision:2,health:{status:'untested',message:''}}}/>);
    expect(screen.getByText('此前配置测试通过，此结果不代表当前配置').closest('.ant-alert')).toHaveClass('ant-alert-warning');expect(screen.getByText('旧连接已验证')).toBeInTheDocument();expect(screen.queryByText('连接测试通过',{exact:true})).not.toBeInTheDocument();
  });
  it('does not bind a late response to a newer saved configuration',async()=>{
    let finish!:(value:Response)=>void;vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve;})));const view=render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await waitFor(()=>expect(finish).toBeTypeOf('function'));view.rerender(<Harness storageConfig={{...storage,revision:2}}/>);
    await act(async()=>finish(response({ok:true,result:{status:'ok',stage:'completed',message:'完成',test_scope:'saved',storage_id:'storage',storage_revision:1}})));expect(await screen.findByText('此前配置测试通过，此结果不代表当前配置')).toBeInTheDocument();
  });
  it('does not promote a successful draft test after saving its fields',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true,result:{status:'ok',stage:'completed',message:'草稿验证通过',test_scope:'draft'}})));const view=render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));expect((await screen.findByText('草稿连接测试通过')).closest('.ant-alert')).toHaveClass('ant-alert-warning');view.rerender(<Harness storageConfig={{...storage,revision:2}}/>);expect(screen.getByText('草稿连接测试通过')).toBeInTheDocument();expect(screen.getByText(/此结果不代表已保存配置已通过验证/)).toBeInTheDocument();
  });
  it('invalidates current success after a user edits even only a credential field',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true,result:{status:'ok',stage:'completed',message:'完成',test_scope:'saved',storage_id:'storage',storage_revision:1}})));render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await screen.findByText('连接测试通过');fireEvent.change(document.querySelector('input[id="secret_access_key"]')!,{target:{value:'ephemeral-new-secret'}});expect(screen.getByText('此前配置测试通过，当前表单尚未验证')).toBeInTheDocument();expect(screen.getByText(/表单已修改/)).toBeInTheDocument();expect(screen.queryByText('ephemeral-new-secret')).not.toBeInTheDocument();
  });
  it('keeps form edits during a running request visible when its result arrives',async()=>{
    let finish!:(value:Response)=>void;vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve;})));render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await waitFor(()=>expect(finish).toBeTypeOf('function'));fireEvent.change(screen.getByLabelText('存储桶'),{target:{value:'new-bucket'}});expect(screen.getByText(/表单已修改/)).toBeInTheDocument();await act(async()=>finish(response({ok:true,result:{status:'ok',stage:'completed',message:'完成',test_scope:'saved',storage_id:'storage',storage_revision:1}})));expect(await screen.findByText('此前配置测试通过，当前表单尚未验证')).toBeInTheDocument();
  });
  it('does not claim the newer revision is untested when another window already verified it',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({ok:true,result:{status:'ok',stage:'completed',message:'旧连接已验证',test_scope:'saved',storage_id:'storage',storage_revision:1}})));const view=render(<Harness/>);fireEvent.click(screen.getByRole('button',{name:'测试连接'}));await screen.findByText('连接测试通过');view.rerender(<Harness storageConfig={{...storage,revision:2,health:{status:'ok',message:'当前修订已验证'}}}/>);
    expect(screen.getByText('此前配置测试通过，此结果不代表当前配置').closest('.ant-alert')).toHaveClass('ant-alert-warning');expect(screen.queryByText(/当前配置尚未验证/)).not.toBeInTheDocument();expect(screen.queryByText(/当前表单尚未验证/)).not.toBeInTheDocument();expect(screen.getByText(/上方健康状态对应当前修订/)).toBeInTheDocument();
  });
});
describe('dedicated storage test transport',()=>{
  it('never reports malformed successful HTTP responses as a passed test',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({})));await expect(backupAPI.testStorage(input)).rejects.toMatchObject({code:'storage_test_invalid_response'});
  });
  it('does not clear a current login after a permission failure',async()=>{
    vi.stubGlobal('fetch',vi.fn().mockResolvedValue(response({error:{code:'denied',message:'forbidden'}},403)));render(<Session/>);await expect(backupAPI.testStorage(input)).rejects.toMatchObject({status:403});expect(screen.getByTestId('session')).toHaveTextContent('admin');
  });
  it('ignores a stale unauthorized response after a newer login',async()=>{
    let finish!:(response:Response)=>void;vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve;})));const request=backupAPI.testStorage(input);setSession({...session!,user:{id:'new-admin',username:'admin'}});render(<Session/>);finish(response({error:{code:'unauthorized'}},401));await expect(request).rejects.toMatchObject({status:401});expect(screen.getByTestId('session')).toHaveTextContent('new-admin');
  });
});
