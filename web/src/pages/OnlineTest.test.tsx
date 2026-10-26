import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import OnlineTest from './OnlineTest';

beforeEach(()=>{vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function fillConnection(){fireEvent.change(screen.getByLabelText('接口地址'),{target:{value:'https://example.test/v1'}});fireEvent.change(screen.getByLabelText('API Key'),{target:{value:'memory-only-key'}});fireEvent.change(screen.getByLabelText('模型 ID'),{target:{value:'fixture-model'}})}
describe('visitor test request lifecycle',()=>{
 it('uses a 600 second creative timeout and displays actual reasoning parameters from the stream',async()=>{
  const fetcher=vi.fn().mockImplementation(()=>Promise.resolve(new Response('event: meta\ndata: {"requested_model":"fixture-model","reasoning_effort":"high","thinking_mode":"","thinking_budget":0}\n\nevent: done\ndata: {"requested_model":"fixture-model","returned_model":"fixture-return","reasoning_effort":"high","thinking_mode":"","thinking_budget":0,"http_status":200,"duration_ms":1000,"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}\n\n',{headers:{'Content-Type':'text/event-stream'}})));vi.stubGlobal('fetch',fetcher);render(<OnlineTest/>);fillConnection();fireEvent.click(screen.getByText('中文动画',{exact:true}));fireEvent.mouseDown(screen.getByLabelText('推理强度'));fireEvent.click(screen.getByText('high',{exact:true}));await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'开始测试'}))});await screen.findByText('返回模型 fixture-return');const body=JSON.parse(fetcher.mock.calls[0][1].body as string);expect(body).toMatchObject({timeout_seconds:600,reasoning_effort:'high',thinking_mode:'',thinking_budget:0,template_id:'cn_html'});expect(screen.getByText('实际推理强度 high')).toBeInTheDocument();expect(screen.getByText('实际 Thinking 上游默认')).toBeInTheDocument();
 });
 it('aborts model discovery on leaving the page without persisting the key',async()=>{
  let signal:AbortSignal|undefined;const fetcher=vi.fn().mockImplementation((_url:string,init:RequestInit)=>{signal=init.signal as AbortSignal;return new Promise(()=>{})});vi.stubGlobal('fetch',fetcher);
  const view=render(<OnlineTest/>);fillConnection();fireEvent.click(screen.getByRole('button',{name:'读取模型'}));await waitFor(()=>expect(fetcher).toHaveBeenCalledOnce());expect(screen.getByRole('button',{name:'开始测试'})).toBeDisabled();view.unmount();expect(signal?.aborted).toBe(true);expect(JSON.stringify(localStorage)).not.toContain('memory-only-key');expect(JSON.stringify(sessionStorage)).not.toContain('memory-only-key');
 });
 it('stops a stalled response stream and clearing removes the stopped state',async()=>{
  let canceled=false;const stream=new ReadableStream<Uint8Array>({start(writer){writer.enqueue(new TextEncoder().encode('event: delta\ndata: {"text":"partial output"}\n\n'))},cancel(){canceled=true}});vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(stream,{headers:{'Content-Type':'text/event-stream'}})));
  render(<OnlineTest/>);fillConnection();fireEvent.click(screen.getByRole('button',{name:'开始测试'}));await screen.findByText('partial output');fireEvent.click(screen.getByRole('button',{name:'停止生成'}));await screen.findByText('已停止');expect(canceled).toBe(true);expect(screen.queryByText('测试失败')).not.toBeInTheDocument();fireEvent.click(screen.getByRole('button',{name:'清空'}));expect(screen.queryByText('已停止')).not.toBeInTheDocument();expect(screen.getByLabelText('API Key')).toHaveValue('');
 });
 it('keeps an error terminal when a malformed stream sends done afterwards',async()=>{
  const stream=new ReadableStream<Uint8Array>({start(writer){writer.enqueue(new TextEncoder().encode('event: error\ndata: {"message":"fixture upstream failure"}\n\nevent: done\ndata: {"returned_model":"should-not-complete","http_status":200}\n\n'));writer.close()}});vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(stream,{headers:{'Content-Type':'text/event-stream'}})));
  render(<OnlineTest/>);fillConnection();await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'开始测试'}))});await screen.findByText('fixture upstream failure');expect(screen.queryByText('已完成')).not.toBeInTheDocument();expect(screen.queryByText(/should-not-complete/)).not.toBeInTheDocument();expect(screen.getByText('请求失败')).toBeInTheDocument();
 });
});
