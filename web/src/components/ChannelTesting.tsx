import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Checkbox, Form, InputNumber, Modal, Select, Space, Switch } from 'antd';
import { PlayCircleOutlined } from '@ant-design/icons';
import { api, post, put } from '../api';
import type { Channel, IntelligenceSchedule, Job, Model, PromptTemplate, IntelligenceGroupStatus, Protocol } from '../types';
import { ReasoningFields } from './ReasoningFields';
import { ScheduleStatus } from './ScheduleStatus';
import { intervalLabel } from '../intelligence';
import { nameOf } from '../lib';
import { channelModelControls, effectiveArtifactFlags } from '../adminState';

interface Props { channel: Channel; models?: readonly Model[]; status?:IntelligenceGroupStatus }
const modelChoices = ({channel, models}:Props) => (models || []).filter(m=>m.channel_id===channel.id&&!m.archived&&!m.missing);
const defaultModel = (channel:Channel, models:readonly Model[]) => {
  const configured=channel.intelligence_schedule?.model_id||channel.intelligence_model_id;
  if(configured)return models.some(model=>model.id===configured)?configured:undefined;
  return models.find(model=>model.upstream_id==='gpt-6.1-sol')?.id;
};

export function groupScheduleEnabled(channel:Channel, models?:readonly Model[]) {
  if(!defaultModel(channel,modelChoices({channel,models})))return false;
  if(channel.model_controls&&!channel.model_controls.artifact_chinese&&!channel.model_controls.artifact_english)return false;
  if(channel.intelligence_schedule)return channel.intelligence_schedule.enabled;
  if(channel.model_controls)return channel.model_controls.artifact_chinese||channel.model_controls.artifact_english;
  return (models||[]).some(m=>m.channel_id===channel.id&&!m.archived&&!m.missing&&(!channel.intelligence_model_id||channel.intelligence_model_id===m.id)&&(m.artifact_chinese||m.artifact_english));
}

export function RunChannelTestButton(props:Props) {
  const {channel}=props;const models=modelChoices(props);
  const [open,setOpen]=useState(false);const [form]=Form.useForm<{model_id:string;template_id:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number}>();
  const {message}=App.useApp();const client=useQueryClient();
  const templates=useQuery({queryKey:['admin-templates'],queryFn:()=>api<PromptTemplate[]>('/admin/templates'),enabled:open});
  const templateID=Form.useWatch('template_id',form);
  const submit=useMutation({
    mutationFn:(values:{model_id:string;template_id:string;reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number})=>{
      const {model_id,template_id,reasoning_effort,thinking_mode,thinking_budget}=values;
      return post<Job>('/admin/jobs',{model_id,template_id,kind:'artifact',...(reasoning_effort?{reasoning_effort}:{}),...(thinking_mode?{thinking_mode}:{}),...(thinking_budget?{thinking_budget}: {})});
    },
    onSuccess:job=>{setOpen(false);void client.invalidateQueries();void message.success(<span>测试任务 {job.id.slice(0,8)} 已提交，<a href="/admin/jobs">查看任务进度</a></span>);},
  });
  const start=()=>{submit.reset();form.resetFields();form.setFieldsValue({model_id:defaultModel(channel,models),template_id:'cn_html',reasoning_effort:'',thinking_mode:'',thinking_budget:0});setOpen(true);};
  const selectedModel=Form.useWatch('model_id',form);
  const selected= models.find(m=>m.id===selectedModel);
  const protocol=(selected?.protocol||channel.protocol||'chat') as Protocol;
  return <>
    <Button size="small" icon={<PlayCircleOutlined aria-hidden="true"/>} disabled={!channel.enabled||models.length===0} onClick={start}>执行测试</Button>
    <Modal title={`执行测试 · ${channel.name}`} open={open} onCancel={()=>!submit.isPending&&setOpen(false)} onOk={()=>form.submit()} okText="提交测试任务" cancelText="取消" confirmLoading={submit.isPending} destroyOnClose>
      {submit.error&&<Alert showIcon type="error" message={submit.error.message}/>}
      {templates.error&&<Alert showIcon type="error" message="模板加载失败" description={templates.error.message}/>}
      <Form form={form} layout="vertical" onFinish={values=>submit.mutate(values)}>
        <Form.Item name="model_id" label="测试模型" rules={[{required:true,message:'请选择当前分组的模型'}]}><Select showSearch optionFilterProp="label" onChange={()=>{form.setFieldsValue({reasoning_effort:'',thinking_mode:'',thinking_budget:0})}} options={models.map(m=>({value:m.id,label:nameOf(m)}))}/></Form.Item>
        <ReasoningFields form={form} protocol={protocol} inherit />
        <Form.Item name="template_id" label="测试模板" rules={[{required:true,message:'请选择测试模板'}]}><Select loading={templates.isLoading} options={templates.data?.map(t=>({value:t.id,label:t.name}))}/></Form.Item>
        {templates.data?.find(t=>t.id===templateID)&&<Alert type="info" message="模板原文" description={templates.data.find(t=>t.id===templateID)?.prompt}/>}
        <p className="muted">每次使用独立上下文，只发送本次模板，不读取历史作品。提交后由 Worker 执行一次作品生成，可在“任务与作品”查看结果；已公开模型的作品也会出现在智商测试页面。使用所选模型及分组保存的参数，可能产生 API 用量。</p>
      </Form>
    </Modal>
  </>;
}

export function ChannelScheduleControl(props:Props) {
  const {channel}=props;const models=modelChoices(props);const enabled=groupScheduleEnabled(channel,props.models);const awaitingTarget=!defaultModel(channel,models);
  const [open,setOpen]=useState(false);const [form]=Form.useForm<IntelligenceSchedule>();
  const client=useQueryClient();const {message}=App.useApp();
  const templates=useQuery({queryKey:['admin-templates'],queryFn:()=>api<PromptTemplate[]>('/admin/templates'),enabled:open});
  const interval=Form.useWatch('interval_seconds',form)||1800;const selectedTemplates=Form.useWatch('template_ids',form)||[];
  const formEnabled=Form.useWatch('enabled',form);const selectedModel=Form.useWatch('model_id',form);
  const controls=channelModelControls(channel);
  const nextControls=channel.model_controls?{...controls,artifact_chinese:!!formEnabled&&selectedTemplates.includes('cn_html'),artifact_english:!!formEnabled&&selectedTemplates.includes('en_svg')}:undefined;
  const prospective={...channel,model_controls:nextControls,intelligence_schedule:{enabled:!!formEnabled,model_id:selectedModel||'',model_scope:'selected' as const,template_ids:selectedTemplates,interval_seconds:interval}};
  const prospectiveModels=models.map(model=>{
    if(!nextControls)return model;
    const updated={...model,artifact_overrides:{...model.artifact_overrides}};
    if(controls.artifact_chinese!==nextControls.artifact_chinese){updated.artifact_chinese=nextControls.artifact_chinese;delete updated.artifact_overrides.chinese}
    if(controls.artifact_english!==nextControls.artifact_english){updated.artifact_english=nextControls.artifact_english;delete updated.artifact_overrides.english}
    return updated;
  });
  const eligibleTemplateCalls=prospectiveModels.reduce((total,model)=>{const flags=effectiveArtifactFlags(model,prospective);return total+Number(flags.artifact_chinese)+Number(flags.artifact_english)},0);
  const dailyCalls=channel.enabled&&formEnabled?Math.ceil(86400/Math.max(60,interval)*eligibleTemplateCalls):0;
  const save=useMutation({
    mutationFn:(values:Partial<IntelligenceSchedule>)=>put<Channel>(`/admin/channels/${channel.id}/intelligence-schedule`,values),
    onSuccess:()=>{setOpen(false);void client.invalidateQueries();void message.success('分组定时测试设置已保存');},
  });
  const configure=(turnOn=channel.intelligence_schedule?.enabled??enabled)=>{
    save.reset();form.resetFields();const id=defaultModel(channel,models);const model=models.find(m=>m.id===id);
    const plan=channel.intelligence_schedule;
    const defaults=channel.model_controls?['cn_html','en_svg'].filter(template=>template==='cn_html'?controls.artifact_chinese:controls.artifact_english):model?.artifact_chinese||model?.artifact_english?['cn_html','en_svg'].filter(template=>template==='cn_html'?model.artifact_chinese:model.artifact_english):['cn_html','en_svg'];
    form.setFieldsValue({enabled:turnOn,model_id:id,model_scope:'selected',template_ids:plan?.template_ids?.length?plan.template_ids:defaults,interval_seconds:plan?.interval_seconds||model?.artifact_interval_seconds||1800});setOpen(true);
  };
  return <>
    <Space direction="vertical" size={4}>
      <Switch size="small" aria-label={`${channel.name} 分组定时测试`} checked={enabled} loading={save.isPending} disabled={!channel.enabled} onChange={checked=>checked?configure(true):save.mutate({enabled:false})}/>
      <Button size="small" type="link" disabled={!channel.enabled||save.isPending} onClick={()=>configure()}>{awaitingTarget?'待选择模型 · 设置':enabled?'已开启 · 设置':'已关闭 · 设置'}</Button>{awaitingTarget&&<span className="muted">等待选择或同步模型：默认 gpt-6.1-sol，未找到时不会执行。</span>}<ScheduleStatus group={props.status}/>
      {save.error&&!open&&<span className="error-text" role="alert">{save.error.message}</span>}
    </Space>
    <Modal title={`分组定时测试 · ${channel.name}`} open={open} onCancel={()=>!save.isPending&&setOpen(false)} onOk={()=>form.submit()} okText="保存定时设置" cancelText="取消" confirmLoading={save.isPending} destroyOnClose>
      {save.error&&<Alert showIcon type="error" message={save.error.message}/>}
      {templates.error&&<Alert showIcon type="error" message="模板加载失败" description={templates.error.message}/>}
      <Alert showIcon type="info" message="仅对选定模型执行定时测试" description="健康检测和定时创作均只执行选定模型，默认 gpt-6.1-sol。获取模型目录不会扩大任务范围。"/>
      <ScheduleStatus group={props.status}/><Form form={form} layout="vertical" onFinish={values=>save.mutate({...values,model_scope:'selected'})}>
        <Form.Item name="enabled" label="开启分组定时测试" valuePropName="checked"><Switch/></Form.Item>
        <Form.Item name="model_id" label="定时测试模型" rules={[{validator:(_,value?:string)=>!formEnabled||!!value&&models.some(model=>model.id===value)?Promise.resolve():Promise.reject(new Error('请选择当前分组的定时测试模型'))}]} extra="只执行这一个模型；未找到 gpt-6.1-sol 时请手动选择，不会使用目录中的第一个模型。"><Select showSearch optionFilterProp="label" placeholder="默认 gpt-6.1-sol，请先获取模型目录" options={models.map(model=>({value:model.id,label:nameOf(model)}))}/></Form.Item>
        <Form.Item name="template_ids" label="定时测试模板" rules={[{validator:(_,value:string[]|undefined)=>!formEnabled||!!value?.length?Promise.resolve():Promise.reject(new Error('请至少选择一个模板'))}]}>
          <Checkbox.Group className="schedule-template-options" options={[
            {label:'中文 HTML 动画',value:'cn_html'},
            {label:'English SVG',value:'en_svg'},
          ]}/>
        </Form.Item>
        <Form.Item name="interval_seconds" label="测试间隔（秒）" rules={[{required:true,message:'请输入执行间隔'},{type:'number',min:60,max:604800,message:'间隔须为 60 至 604800 秒'}]}><InputNumber min={60} max={604800} style={{width:'100%'}}/></Form.Item>
        <p className="muted">间隔：{intervalLabel(interval)} · 仅选定 1 个模型，{eligibleTemplateCalls} 个启用的“模型＋模板”组合</p><Alert showIcon type="info" message={`预计每天 ${dailyCalls} 次作品调用`} description="按所选模型及实际模板开关估算。分组模板开关统一调整允许状态，实际定时创作始终只执行选定模型。开启后立即排队一次，之后按周期执行；重复保存相同设置不重置时间。"/>
      </Form>
    </Modal>
  </>;
}
