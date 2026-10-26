import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Form, InputNumber, Modal, Select, Space, Switch } from 'antd';
import { PlayCircleOutlined } from '@ant-design/icons';
import { api, post, put } from '../api';
import type { Channel, IntelligenceSchedule, Job, Model, PromptTemplate } from '../types';
import { nameOf } from '../lib';

interface Props { channel: Channel; models?: readonly Model[] }
const modelChoices = ({channel, models}:Props) => (models || []).filter(m=>m.channel_id===channel.id&&!m.archived&&!m.missing);
const defaultModel = (channel:Channel, models:readonly Model[]) => models.find(m=>m.id===(channel.intelligence_schedule?.model_id||channel.intelligence_model_id))?.id || models[0]?.id;

export function groupScheduleEnabled(channel:Channel, models?:readonly Model[]) {
  if(channel.intelligence_schedule)return channel.intelligence_schedule.enabled;
  return (models||[]).some(m=>m.channel_id===channel.id&&!m.archived&&!m.missing&&(!channel.intelligence_model_id||channel.intelligence_model_id===m.id)&&(m.artifact_chinese||m.artifact_english));
}

export function RunChannelTestButton(props:Props) {
  const {channel}=props;const models=modelChoices(props);
  const [open,setOpen]=useState(false);const [form]=Form.useForm<{model_id:string;template_id:string}>();
  const {message}=App.useApp();const client=useQueryClient();
  const templates=useQuery({queryKey:['admin-templates'],queryFn:()=>api<PromptTemplate[]>('/admin/templates'),enabled:open});
  const templateID=Form.useWatch('template_id',form);
  const submit=useMutation({
    mutationFn:(values:{model_id:string;template_id:string})=>post<Job>('/admin/jobs',{...values,kind:'artifact'}),
    onSuccess:job=>{setOpen(false);void client.invalidateQueries();void message.success(<span>测试任务 {job.id.slice(0,8)} 已提交，<a href="/admin/jobs">查看任务进度</a></span>);},
  });
  const start=()=>{submit.reset();form.resetFields();form.setFieldsValue({model_id:defaultModel(channel,models),template_id:'cn_html'});setOpen(true);};
  return <>
    <Button size="small" icon={<PlayCircleOutlined aria-hidden="true"/>} disabled={!channel.enabled||models.length===0} onClick={start}>执行测试</Button>
    <Modal title={`执行测试 · ${channel.name}`} open={open} onCancel={()=>!submit.isPending&&setOpen(false)} onOk={()=>form.submit()} okText="提交测试任务" cancelText="取消" confirmLoading={submit.isPending} destroyOnClose>
      {submit.error&&<Alert showIcon type="error" message={submit.error.message}/>}
      {templates.error&&<Alert showIcon type="error" message="模板加载失败" description={templates.error.message}/>}
      <Form form={form} layout="vertical" onFinish={values=>submit.mutate(values)}>
        <Form.Item name="model_id" label="测试模型" rules={[{required:true,message:'请选择当前分组的模型'}]}><Select showSearch optionFilterProp="label" options={models.map(m=>({value:m.id,label:nameOf(m)}))}/></Form.Item>
        <Form.Item name="template_id" label="测试模板" rules={[{required:true,message:'请选择测试模板'}]}><Select loading={templates.isLoading} options={templates.data?.map(t=>({value:t.id,label:t.name}))}/></Form.Item>
        {templates.data?.find(t=>t.id===templateID)&&<Alert type="info" message="模板原文" description={templates.data.find(t=>t.id===templateID)?.prompt}/>}
        <p className="muted">每次使用独立上下文，只发送本次模板，不读取历史作品。提交后由 Worker 执行一次作品生成，可在“任务与作品”查看结果；已公开模型的作品也会出现在智商测试页面。使用所选模型及分组保存的参数，可能产生 API 用量。</p>
      </Form>
    </Modal>
  </>;
}

export function ChannelScheduleControl(props:Props) {
  const {channel}=props;const models=modelChoices(props);const enabled=groupScheduleEnabled(channel,props.models);
  const [open,setOpen]=useState(false);const [form]=Form.useForm<IntelligenceSchedule>();
  const client=useQueryClient();const {message}=App.useApp();
  const templates=useQuery({queryKey:['admin-templates'],queryFn:()=>api<PromptTemplate[]>('/admin/templates'),enabled:open});
  const interval=Form.useWatch('interval_seconds',form)||1800;const selectedTemplates=Form.useWatch('template_ids',form)||[];
  const formEnabled=Form.useWatch('enabled',form);
  const save=useMutation({
    mutationFn:(values:Partial<IntelligenceSchedule>)=>put<Channel>(`/admin/channels/${channel.id}/intelligence-schedule`,values),
    onSuccess:()=>{setOpen(false);void client.invalidateQueries();void message.success('分组定时测试设置已保存');},
  });
  const configure=(turnOn=enabled)=>{
    save.reset();form.resetFields();const id=defaultModel(channel,models);const model=models.find(m=>m.id===id);
    const plan=channel.intelligence_schedule;
    form.setFieldsValue({enabled:turnOn,model_id:plan?.model_id||id,template_ids:plan?.template_ids?.length?plan.template_ids:model?.artifact_english&&!model.artifact_chinese?['en_svg']:['cn_html'],interval_seconds:plan?.interval_seconds||model?.artifact_interval_seconds||1800});setOpen(true);
  };
  return <>
    <Space direction="vertical" size={4}>
      <Switch size="small" aria-label={`${channel.name} 分组定时测试`} checked={enabled} loading={save.isPending} disabled={!channel.enabled||(!enabled&&models.length===0)} onChange={checked=>checked?configure(true):save.mutate({enabled:false})}/>
      <Button size="small" type="link" disabled={!channel.enabled||save.isPending} onClick={()=>configure()}>{enabled?'已开启 · 设置':'已关闭 · 设置'}</Button>
      {save.error&&!open&&<span className="error-text" role="alert">{save.error.message}</span>}
    </Space>
    <Modal title={`分组定时测试 · ${channel.name}`} open={open} onCancel={()=>!save.isPending&&setOpen(false)} onOk={()=>form.submit()} okText="保存定时设置" cancelText="取消" confirmLoading={save.isPending} destroyOnClose>
      {save.error&&<Alert showIcon type="error" message={save.error.message}/>}
      {templates.error&&<Alert showIcon type="error" message="模板加载失败" description={templates.error.message}/>}
      <Form form={form} layout="vertical" onFinish={values=>save.mutate(values)}>
        <Form.Item name="enabled" label="开启分组定时测试" valuePropName="checked"><Switch/></Form.Item>
        <Form.Item name="model_id" label="定时测试模型" rules={[{required:formEnabled,message:'请选择当前分组的模型'}]}><Select showSearch optionFilterProp="label" options={models.map(m=>({value:m.id,label:nameOf(m)}))}/></Form.Item>
        <Form.Item name="template_ids" label="定时测试模板" rules={[{required:formEnabled,message:'请至少选择一个模板'}]}><Select mode="multiple" loading={templates.isLoading} options={templates.data?.map(t=>({value:t.id,label:t.name}))}/></Form.Item>
        <Form.Item name="interval_seconds" label="测试间隔（秒）" rules={[{required:true,message:'请输入执行间隔'},{type:'number',min:60,max:604800,message:'间隔须为 60 至 604800 秒'}]}><InputNumber min={60} max={604800} style={{width:'100%'}}/></Form.Item>
        <Alert showIcon type="info" message={`预计每天 ${formEnabled?Math.ceil(86400/Math.max(60,interval))*selectedTemplates.length:0} 次作品调用`} description="保存后按本分组选择的模型、模板和间隔调度。关闭后取消尚未开始的定时任务，已发出的任务继续完成；健康监控按模型配置执行。"/>
      </Form>
    </Modal>
  </>;
}
