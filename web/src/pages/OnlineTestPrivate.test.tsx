import {act,cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react';
import {afterEach,beforeEach,expect,it,vi} from 'vitest';
import OnlineTest from './OnlineTest';
import {setUserSession} from '../userApi';

const connection={id:'saved-connection',user_id:'user',name:'Saved endpoint',base_url:'https://fixture.example/v1',protocol:'chat',key_mask:'••••xyz',revision:2,disabled:false};
beforeEach(()=>{Object.defineProperty(window,'matchMedia',{writable:true,value:vi.fn().mockImplementation(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal('ResizeObserver',class{observe(){}disconnect(){}});setUserSession({user:{id:'user',username:'user'},csrf_token:'member-csrf'});window.history.replaceState({},'','/test?connection=saved-connection')});
afterEach(()=>{cleanup();setUserSession(null);vi.unstubAllGlobals();window.history.replaceState({},'','/')});

it('uses a saved reference without returning a key and exposes its persisted record',async()=>{
 const fetcher=vi.fn(async(url:string,_init?:RequestInit)=>{if(url==='/api/v1/user/connections')return new Response(JSON.stringify([connection]));if(url==='/api/v1/test/generate')return new Response('event: record\ndata: {"record_id":"saved-record","saved":true}\n\nevent: delta\ndata: {"text":"private text"}\n\nevent: done\ndata: {"record_id":"saved-record","saved":true,"requested_model":"model","returned_model":"actual","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}\n\n',{headers:{'Content-Type':'text/event-stream'}});throw new Error('Unexpected '+url)});
 vi.stubGlobal('fetch',fetcher);render(<OnlineTest/>);await waitFor(()=>expect(screen.getByLabelText('API Key')).toBeDisabled());fireEvent.change(screen.getByLabelText('模型 ID'),{target:{value:'model'}});fireEvent.click(screen.getByRole('button',{name:'开始测试'}));await screen.findByText('private text');const request=fetcher.mock.calls.find(call=>call[0]==='/api/v1/test/generate')!;const body=JSON.parse(request[1]!.body as string);expect(body.connection_id).toBe(connection.id);expect(body.connection_revision).toBe(connection.revision);expect(body.client_request_id).toBeTruthy();expect(body).not.toHaveProperty('api_key');expect(screen.getByRole('link',{name:'查看本次记录'})).toHaveAttribute('href','/account/tests/saved-record');expect(screen.getByText(/连接密钥加密保存/)).toBeInTheDocument();
});

it('prefills a historical prompt without sending and requires confirmation after connection revision changes',async()=>{
 window.history.replaceState({},'','/test?record=history');const record={connection_id:connection.id,connection_revision:1,protocol:'chat',model:'historical-model',prompt:'Original saved prompt',max_tokens:128,timeout_seconds:60,reasoning_effort:'',thinking_mode:'',thinking_budget:0,template_id:''};
 const fetcher=vi.fn(async(url:string)=>new Response(JSON.stringify(url==='/api/v1/user/connections'?[connection]:record)));vi.stubGlobal('fetch',fetcher);render(<OnlineTest/>);await waitFor(()=>expect(screen.getByLabelText('提示词')).toHaveValue(record.prompt));expect(screen.getByRole('button',{name:'开始测试'})).toBeDisabled();expect(fetcher.mock.calls.some(call=>call[0]==='/api/v1/test/generate')).toBe(false);fireEvent.click(screen.getByRole('button',{name:'确认当前连接'}));expect(screen.getByRole('button',{name:'开始测试'})).not.toBeDisabled();await act(async()=>{setUserSession(null)});expect(screen.getByLabelText('提示词')).not.toHaveValue(record.prompt);
});

it('refreshes a changed saved connection but never automatically repeats a failed generation',async()=>{
 let connectionsRead=0;let attempts=0;
 const fetcher=vi.fn(async(url:string)=>{if(url==='/api/v1/user/connections'){connectionsRead++;return new Response(JSON.stringify([{...connection,revision:connectionsRead===1?2:3}]))}if(url==='/api/v1/test/generate'){attempts++;return new Response(JSON.stringify({error:{code:'connection_changed',message:'连接配置已变化，请重新确认后测试'}}),{status:409})}throw new Error('Unexpected '+url)});
 vi.stubGlobal('fetch',fetcher);render(<OnlineTest/>);await waitFor(()=>expect(screen.getByLabelText('API Key')).toBeDisabled());fireEvent.change(screen.getByLabelText('模型 ID'),{target:{value:'model'}});fireEvent.click(screen.getByRole('button',{name:'开始测试'}));await screen.findByText('连接配置已变化，请确认当前连接后重新开始。');expect(screen.getByRole('button',{name:'开始测试'})).toBeDisabled();expect(attempts).toBe(1);fireEvent.click(screen.getByRole('button',{name:'确认当前连接'}));expect(attempts).toBe(1);expect(screen.getByRole('button',{name:'开始测试'})).not.toBeDisabled();
});
