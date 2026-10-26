import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from 'antd';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ModelsPage, SettingsPage } from './Admin';

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
 it('makes group-owned artwork schedules explicit and keeps their legacy model fields read-only',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[{id:'m',channel_id:'c',upstream_id:'fixture',display_name:'Fixture',archived:false,artifact_chinese:true,artifact_english:true,artifact_interval_seconds:1800}]);client.setQueryData(['admin-channels'],[{id:'c',name:'Group',enabled:true,archived:false,protocol:'chat',intelligence_schedule:{enabled:true,model_id:'m',template_ids:['cn_html','en_svg'],interval_seconds:3600}}]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600});
  render(<QueryClientProvider client={client}><App><ModelsPage/></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:/^编辑$/}));
  expect(screen.getByText('本分组已接管定时创作：已开启')).toBeInTheDocument();expect(screen.getByLabelText('中文 HTML 创作')).toBeDisabled();expect(screen.getByLabelText('英文 SVG 创作')).toBeDisabled();expect(screen.getByLabelText('创作间隔（秒）')).toBeDisabled();expect(screen.getByLabelText('周期健康检测')).not.toBeDisabled();expect(screen.getByText('保存后预计每日自动请求约 48 次')).toBeInTheDocument();client.clear();
 });
 it('distinguishes inherited effort from upstream default and constrains thinking by protocol',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[{id:'c',name:'Anthropic fixture',protocol:'anthropic',enabled:true,archived:false}]);client.setQueryData(['admin-settings'],{artifact_timeout_seconds:600});render(<QueryClientProvider client={client}><App><ModelsPage/></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));fireEvent.mouseDown(screen.getByLabelText('所属分组'));fireEvent.click(screen.getByText('Anthropic fixture',{exact:true}));expect(screen.getByLabelText('Thinking 模式')).toBeInTheDocument();fireEvent.mouseDown(screen.getByLabelText('推理强度'));fireEvent.click(screen.getByText('上游默认（覆盖分组）',{exact:true}));expect(screen.getAllByText('上游默认（覆盖分组）').length).toBeGreaterThan(0);fireEvent.mouseDown(screen.getByLabelText('生成协议'));fireEvent.click(screen.getByText('OpenAI Responses',{exact:true}));expect(screen.queryByLabelText('Thinking 模式')).not.toBeInTheDocument();expect(screen.getByLabelText('创作请求超时（秒）')).toHaveValue('600');expect(screen.getByLabelText('周期健康检测')).not.toBeChecked();client.clear();
 });
 it('does not opt a new model into recurring paid calls',async()=>{
  const fetcher=vi.fn().mockRejectedValue(new Error('network should not be used'));vi.stubGlobal('fetch',fetcher);const client=new QueryClient({defaultOptions:{queries:{staleTime:Infinity,retry:false}}});client.setQueryData(['admin-models'],[]);client.setQueryData(['admin-channels'],[]);client.setQueryData(['admin-settings'],{probe_interval_seconds:300,artifact_interval_seconds:1800,artifact_timeout_seconds:180});
  render(<QueryClientProvider client={client}><App><ModelsPage/></App></QueryClientProvider>);fireEvent.click(screen.getByRole('button',{name:'手动添加模型'}));
  expect(await screen.findByText('保存后预计每日自动请求约 0 次')).toBeInTheDocument();expect(screen.getByLabelText('周期健康检测')).not.toBeChecked();expect(screen.getByLabelText('中文 HTML 创作')).not.toBeChecked();expect(screen.getByLabelText('英文 SVG 创作')).not.toBeChecked();expect(fetcher).not.toHaveBeenCalled();client.clear();
 });
});
