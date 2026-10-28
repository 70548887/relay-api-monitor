import{cleanup,render,screen}from'@testing-library/react';
import{afterEach,expect,it}from'vitest';
import{ScheduleStatus}from'./ScheduleStatus';
import{intervalLabel}from'../intelligence';
import type{IntelligenceGroupStatus}from'../types';
afterEach(cleanup);
it('shows the saved interval, Beijing time and explicit visibility reason',()=>{
 const group={channel_id:'c',channel_name:'Group',platform_id:'p',platform_name:'Platform',visibility_reasons:[],plans:[{model_id:'private-model',requested_model:'model-name',template_id:'cn_html',source:'group',enabled:true,interval_seconds:6000,next_run_at:'2026-10-27T12:00:00Z',state:'worker_offline',blocking_reason:'Worker 离线，恢复后继续调度',visibility_reasons:['模型未公开']}]}as IntelligenceGroupStatus;
 render(<ScheduleStatus group={group}/>);expect(screen.getByText('Worker 离线')).toBeInTheDocument();expect(screen.getByText(/2026\/10\/27 20:00:00/)).toBeInTheDocument();expect(screen.getByText(/6000 秒 · 1 小时 40 分/)).toBeInTheDocument();expect(screen.getByText(/前台不可见：模型未公开/)).toBeInTheDocument();expect(screen.getByRole('link',{name:'检查公开设置'})).toHaveAttribute('href','/admin/models?model=private-model');expect(screen.queryByText('即将执行')).not.toBeInTheDocument();
});
it('converts intervals without changing units',()=>{expect(intervalLabel(6000)).toBe('6000 秒 · 1 小时 40 分');expect(intervalLabel(1800)).toBe('1800 秒 · 30 分')});
