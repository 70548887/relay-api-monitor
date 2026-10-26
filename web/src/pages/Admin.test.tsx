import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ModelsPage } from './Admin';

beforeEach(()=>{const getStyle=window.getComputedStyle.bind(window);vi.spyOn(window,'getComputedStyle').mockImplementation(element=>getStyle(element));vi.stubGlobal('matchMedia',vi.fn().mockReturnValue({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn()}));vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.restoreAllMocks();vi.unstubAllGlobals()});
describe('model configuration form',()=>{
 it('distinguishes inherited effort from upstream default and constrains thinking by protocol',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[{id:'c',name:'Anthropic fixture',protocol:'anthropic',enabled:true,archived:false}]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600});render(<QueryClientProvider client={client}><App><ModelsPage/></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));fireEvent.mouseDown(screen.getByLabelText('所属分组'));fireEvent.click(screen.getByText('Anthropic fixture',{exact:true}));expect(screen.getByLabelText('Thinking 模式')).toBeInTheDocument();fireEvent.mouseDown(screen.getByLabelText('推理强度'));fireEvent.click(screen.getByText('上游默认（覆盖分组）',{exact:true}));expect(screen.getAllByText('上游默认（覆盖分组）').length).toBeGreaterThan(0);fireEvent.mouseDown(screen.getByLabelText('生成协议'));fireEvent.click(screen.getByText('OpenAI Responses',{exact:true}));expect(screen.queryByLabelText('Thinking 模式')).not.toBeInTheDocument();expect(screen.getByLabelText('请求超时（秒）')).toHaveValue('600');expect(screen.getByLabelText('周期健康检测')).not.toBeChecked();client.clear();
 });
 it('does not opt a new model into recurring paid calls',async()=>{
  const fetcher=vi.fn().mockRejectedValue(new Error('network should not be used'));vi.stubGlobal('fetch',fetcher);const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[]);client.setQueryData(['admin-settings'],{probe_interval_seconds:300,artifact_interval_seconds:1800,artifact_timeout_seconds:180});
  render(<QueryClientProvider client={client}><App><ModelsPage/></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));
  expect(await screen.findByText('保存后预计每日自动请求约 0 次')).toBeInTheDocument();expect(screen.getByLabelText('周期健康检测')).not.toBeChecked();expect(screen.getByLabelText('中文 HTML 创作')).not.toBeChecked();expect(screen.getByLabelText('英文 SVG 创作')).not.toBeChecked();expect(fetcher).not.toHaveBeenCalled();client.clear();
 });
});
