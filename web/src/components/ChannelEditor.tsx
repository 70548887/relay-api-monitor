import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Tabs, Tag } from 'antd';
import type { NamePath } from 'antd/es/form/interface';
import { ApiOutlined, CheckCircleOutlined, ExportOutlined, SyncOutlined } from '@ant-design/icons';
import { post, put } from '../api';
import type { Channel, Model, Platform, Protocol } from '../types';
import { activeRecords, channelModelControls, channelUpdate } from '../adminState';
import { nameOf, time } from '../lib';
import { reasoningLabel, verificationLabel } from '../intelligence';
import { ReasoningFields, changeReasoningProtocol } from './ReasoningFields';
import './ChannelEditor.css';

type EditorTab='connection'|'models'|'reasoning'|'advanced';
type EditorValues=Partial<Channel>&{api_key?:string};
type Operation='save'|'sync'|'apply';
interface Props {
 open:boolean;
 channel?:Channel;
 platforms:readonly Platform[];
 models:readonly Model[];
 modelsLoading?:boolean;
 onClose:()=>void;
}
const protocolOptions=[{value:'chat',label:'OpenAI Chat Completions'},{value:'responses',label:'OpenAI Responses'},{value:'anthropic',label:'Anthropic Messages'}];
const protocolName=(value?:string)=>protocolOptions.find(item=>item.value===value)?.label||'未选择';
const fieldTab=(name:NamePath):EditorTab=>{
 const first=String(Array.isArray(name)?name[0]:name);
 if(['reasoning_effort','thinking_mode','thinking_budget'].includes(first))return 'reasoning';
 if(['enabled','public','model_controls','intelligence_model_id'].includes(first))return 'models';
 if(['models_path','generate_path','chat_token_field','anthropic_version','sort','group_provider','group_path','group_field'].includes(first))return 'advanced';
 return 'connection';
};
function scheduledModel(channel:Channel|undefined,models:readonly Model[]) {
 const configured=channel?.intelligence_schedule?.model_id||channel?.intelligence_model_id;
 if(configured)return configured;
 const available=models.filter(model=>model.channel_id===channel?.id&&!model.archived&&!model.missing);
 return available.find(model=>model.upstream_id==='gpt-6.1-sol')?.id;
}
function initialValues(channel?:Channel,models:readonly Model[]=[]):EditorValues {
 return {enabled:true,public:true,protocol:'chat',discovery_protocol:'openai',sort:0,reasoning_effort:'',thinking_mode:'',thinking_budget:0,group_provider:'none',group_path:'',group_field:'',anthropic_version:'2023-06-01',chat_token_field:'max_tokens',...channel,intelligence_model_id:scheduledModel(channel,models)||'',model_controls:channelModelControls(channel),api_key:''};
}
function endpointPreview(base?:string,path?:string,suffix='/models') {
 try{
  const url=new URL(base?.trim()||'');
  if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash)return '请先填写有效的接口地址';
  if(path){if(!path.startsWith('/')||path.startsWith('//')||/[?#\\]/.test(path))return '接口路径格式不正确';url.pathname=path}
  else url.pathname=(url.pathname.replace(/\/+$/,'')||'/v1')+suffix;
  return url.toString();
 }catch{return '请先填写有效的接口地址'}
}
const pathRules=[{validator:(_:unknown,value?:string)=>!value||/^\/(?!\/)[^?#\\]*$/.test(value)?Promise.resolve():Promise.reject(new Error('请填写以 / 开头且不含域名、查询参数或 # 的路径'))}];

export function ChannelEditor({open,channel,platforms,models,modelsLoading=false,onClose}:Props) {
 const [form]=Form.useForm<EditorValues>();
 const [record,setRecord]=useState<Channel|undefined>(channel);
 const [tab,setTab]=useState<EditorTab>('connection');
 const [busy,setBusy]=useState<Operation|null>(null);
 const [error,setError]=useState('');
 const [syncCount,setSyncCount]=useState<number>();
 const lock=useRef(false);
 const targetEdited=useRef(false);
 const contentRef=useRef<HTMLDivElement>(null);
 const client=useQueryClient();
 const {message}=App.useApp();
 const watched=Form.useWatch([],form);
 const values:EditorValues={...initialValues(record,models),...watched};
 const protocol=(values.protocol||'chat') as Protocol;
 const discovery=values.discovery_protocol||'openai';
 const provider=values.group_provider||'none';
 const controls=values.model_controls||channelModelControls(record);
 const savedControls=channelModelControls(record);
 const groupModels=activeRecords(models).filter(model=>model.channel_id===record?.id);
 const available=groupModels.filter(model=>!model.missing);
 const targetModel=available.find(model=>values.intelligence_model_id?model.id===values.intelligence_model_id:model.upstream_id==='gpt-6.1-sol');
 const originalTargetID=record?.intelligence_schedule?.model_id||record?.intelligence_model_id;
 const unavailableOriginalTarget=!!originalTargetID&&!available.some(model=>model.id===originalTargetID);
 const platformName=platforms.find(item=>item.id===values.platform_id)?.name||'请选择所属平台';
 const selectedName=values.name||'未命名分组';
 const suffix=protocol==='responses'?'/responses':protocol==='anthropic'?'/messages':'/chat/completions';

 useEffect(()=>{
  if(!open){form.setFieldsValue({api_key:''});return}
  setRecord(channel);setTab('connection');setError('');setSyncCount(undefined);lock.current=false;targetEdited.current=false;setBusy(null);
  if(contentRef.current)contentRef.current.scrollTop=0;
  form.resetFields();form.setFieldsValue(initialValues(channel,models));
 },[open,channel?.id,form]);
 useEffect(()=>{
  if(!open||!record||targetEdited.current||form.getFieldValue('intelligence_model_id'))return;
  const selected=scheduledModel(record,models);
  if(selected)form.setFieldsValue({intelligence_model_id:selected});
 },[open,record?.id,models,form]);

 const exceptions=(model:Model)=>{
  const reasons:string[]=[];
  if(values.public===record?.public&&values.public&&!model.public)reasons.push('未公开');
  if(controls.probe_enabled===savedControls.probe_enabled&&controls.probe_enabled&&!model.probe_enabled)reasons.push('健康检测已关闭');
  const hasLegacyPlan=!!record?.intelligence_schedule&&!record.model_controls;
  if(controls.artifact_chinese===savedControls.artifact_chinese&&controls.artifact_chinese&&(model.artifact_overrides?.chinese===false||(!hasLegacyPlan&&!model.artifact_chinese)))reasons.push('中文创作已关闭');
  if(controls.artifact_english===savedControls.artifact_english&&controls.artifact_english&&(model.artifact_overrides?.english===false||(!hasLegacyPlan&&!model.artifact_english)))reasons.push('英文创作已关闭');
  return reasons;
 };
 const exceptionCount=groupModels.filter(model=>exceptions(model).length>0).length;
 const targetExceptions=targetModel?exceptions(targetModel):[];
 const legacyAll=record?.intelligence_schedule?.model_scope==='all';
 const targetCaption=legacyAll?'旧版计划 · 保存后收窄':'唯一自动测试目标';
 const targetDescription=legacyAll?'当前仍为全部模型计划，保存后仅执行指定模型。':!values.enabled?'分组已停用，自动任务暂停。':targetModel?'健康检测与定时创作仅使用此模型。':values.intelligence_model_id?'原目标不可用，自动任务暂停，需重新选择。':'等待目录中的 gpt-6.1-sol，或手动指定模型。';
 const changeTab=(next:EditorTab)=>{setTab(next);if(contentRef.current)contentRef.current.scrollTop=0};
 const close=()=>{if(!lock.current)onClose()};
 async function invalidate() {
  await Promise.all([
   client.invalidateQueries({queryKey:['admin-channels']}),
   client.invalidateQueries({queryKey:['admin-intelligence-status']}),
   client.invalidateQueries({queryKey:['monitor']}),
   client.invalidateQueries({queryKey:['intelligence']}),
  ]);
 }
 function cacheChannel(saved:Channel) {
  client.setQueryData<Channel[]>(['admin-channels'],previous=>previous?.some(item=>item.id===saved.id)?previous.map(item=>item.id===saved.id?saved:item):[...(previous||[]),saved]);
 }
 async function perform(operation:Operation) {
  if(lock.current)return;
  lock.current=true;setBusy(operation);setError('');if(operation==='sync')setSyncCount(undefined);
  let savedCompleted=false;
  try{
   const validated=await form.validateFields();
   const submitted={...form.getFieldsValue(true),...validated,api_key:validated.api_key||'',intelligence_model_id:validated.intelligence_model_id||''};
   const body={...submitted,selected_model_only:true,...(operation==='apply'?{apply_model_controls:true}:{})};
   const saved=record
    ?await put<Channel>(`/admin/channels/${record.id}`,channelUpdate(record,body))
    :await post<Channel>('/admin/channels',body);
   savedCompleted=true;setRecord(saved);cacheChannel(saved);targetEdited.current=false;
   // Once stored, discard the replacement secret. The response carries only its mask.
   form.setFieldsValue({...submitted,...saved,model_controls:channelModelControls(saved),api_key:''});
   if(operation==='sync'){
    const result=await post<{count:number;models:Model[]}>(`/admin/channels/${saved.id}/sync-models`,{});
    client.setQueryData<Model[]>(['admin-models'],previous=>[...(previous||[]).filter(item=>item.channel_id!==saved.id),...result.models]);
    setSyncCount(result.count);await invalidate();
    const latest=client.getQueryData<Channel[]>(['admin-channels'])?.find(item=>item.id===saved.id);
    if(latest)setRecord(latest);
    void message.success(`已保存并同步当前分组的 ${result.count} 个模型`);
   }else{
    await Promise.all([invalidate(),client.invalidateQueries({queryKey:['admin-models']})]);
    void message.success(operation==='apply'?'已保存并应用到全部模型':'分组设置已保存');
    onClose();
   }
  }catch(cause){
   const validation=cause as {errorFields?:{name:NamePath;errors:string[]}[]};
   if(validation.errorFields?.length){
    const name=validation.errorFields[0].name;changeTab(fieldTab(name));
    if(typeof requestAnimationFrame==='function')requestAnimationFrame(()=>{form.getFieldInstance(name)?.focus?.();if(typeof HTMLElement.prototype.scrollIntoView==='function')form.scrollToField(name,{block:'center'})});
   }else{
    const text=cause instanceof Error?cause.message:'操作失败，请稍后重试';
    setError(operation==='sync'&&savedCompleted?`分组已保存，但获取模型失败：${text}`:text);
    if(operation==='sync'&&savedCompleted)await Promise.allSettled([invalidate(),client.invalidateQueries({queryKey:['admin-models']})]);
   }
  }finally{lock.current=false;setBusy(null)}
 }
 const catalog=<div className="channel-editor-catalog"><div><h3>模型目录 <Tag>{modelsLoading?'读取中':`${available.length} 个可用模型`}</Tag></h3><p>先保存当前配置，再同步这个分组的模型目录。</p><small>同步仅更新目录，健康检测与定时创作均只执行指定模型，默认 gpt-6.1-sol。</small>{syncCount!==undefined&&<small className="channel-editor-sync-success">本次已获取 {syncCount} 个模型</small>}</div><Button icon={<SyncOutlined aria-hidden="true"/>} loading={busy==='sync'} disabled={!!busy} onClick={()=>void perform('sync')}>保存并获取模型</Button></div>;
 const sectionTitle=(title:string,description?:string)=><div className="channel-editor-section-title"><h3>{title}</h3>{description&&<p>{description}</p>}</div>;
 const tabs=[
  {key:'connection',label:'基础连接',forceRender:true,children:<section className="channel-editor-section" aria-label="基础连接设置">
   <div className="channel-editor-form-grid"><Form.Item name="platform_id" label="所属平台" rules={[{required:true,message:'请选择平台'}]}><Select options={platforms.map(item=>({label:item.name,value:item.id}))}/></Form.Item><Form.Item name="name" label="分组名称" rules={[{required:true,whitespace:true,message:'请输入分组名称'}]}><Input/></Form.Item></div>
   <Form.Item name="base_url" label="接口地址" rules={[{required:true,message:'请输入接口地址'},{type:'url',message:'请输入完整 HTTP 或 HTTPS 地址'}]}><Input placeholder="https://api.example.com/v1" autoComplete="off"/></Form.Item>
   <Form.Item name="api_key" label="API Key" extra={record?'当前密钥已加密保存；留空保留，填写则替换。':'保存后密钥由服务器加密存储。'} rules={record?[]:[{required:true,whitespace:true,message:'请输入 API Key'}]}><Input.Password placeholder={record?'留空保留当前 Key，填写则替换':'请输入 API Key'} autoComplete="new-password"/></Form.Item>
   <div className="channel-editor-form-grid"><Form.Item name="protocol" label="默认生成协议"><Select options={protocolOptions} onChange={value=>changeReasoningProtocol(form,value)}/></Form.Item><Form.Item name="discovery_protocol" label="模型发现协议"><Select options={[{label:'OpenAI',value:'openai'},{label:'Anthropic',value:'anthropic'}]}/></Form.Item></div>
   <p className="channel-editor-hint">生成与发现协议独立配置；接口路径及兼容参数可在“高级设置”中调整。</p>
   {catalog}
  </section>},
  {key:'models',label:'模型与开关',forceRender:true,children:<section className="channel-editor-section" aria-label="模型与开关设置">
   {sectionTitle('分组开关','分组主开关统一生效，模型仍可保留单独关闭的设置。')}
   <div className="channel-editor-form-grid channel-editor-group-switches">
    <div className="channel-editor-switch-card"><div className="channel-editor-switch-text"><h4>启用分组</h4><p>停用后暂停本分组的接口调用。</p></div><Form.Item name="enabled" label="启用分组" valuePropName="checked" noStyle><Switch aria-label="启用分组"/></Form.Item></div>
    <div className="channel-editor-switch-card"><div className="channel-editor-switch-text"><h4>公开展示</h4><p>控制分组和所属模型的前台可见性。</p></div><Form.Item name="public" label="公开展示" valuePropName="checked" noStyle><Switch aria-label="公开展示"/></Form.Item></div>
   </div>
   {sectionTitle('定时任务允许开关','健康检测与创作均仅执行下方选定模型；其他模型保留目录和手动测试能力。')}
   <div className="channel-editor-switch-grid">{([
    ['probe_enabled','全部模型健康检测','周期检测模型可用性'],
    ['artifact_chinese','全部模型中文 HTML 创作','鹈鹕骑自行车 HTML 动画'],
    ['artifact_english','全部模型英文 SVG 创作','鹈鹕骑自行车 SVG'],
   ] as const).map(([key,label,description])=><div className="channel-editor-switch-card" key={key}><div className="channel-editor-switch-text"><h4>{label.replace('全部模型','')}</h4><p>{description}</p></div><Form.Item name={['model_controls',key]} label={label} valuePropName="checked" noStyle><Switch aria-label={label}/></Form.Item></div>)}</div>
   <p className="channel-editor-hint">普通保存仅应用改动的主开关，未改动项保留模型例外。旧分组首次普通保存也保留原有状态。</p>
   <Form.Item name="intelligence_model_id" label="定时测试模型" extra={unavailableOriginalTarget&&values.intelligence_model_id===originalTargetID?'原指定模型已失效，任务暂停；普通保存不会改换模型。请明确重新选择测试目标。':'健康检测与定时创作均只执行此模型。清空后恢复默认 gpt-6.1-sol；目录中不存在时等待匹配。'} rules={[{validator:(_,value?:string)=>!value||available.some(model=>model.id===value)||!!record&&value===originalTargetID?Promise.resolve():Promise.reject(new Error('请选择当前分组的有效模型'))}]}><Select allowClear showSearch onChange={()=>{targetEdited.current=true}} optionFilterProp="label" placeholder="默认 gpt-6.1-sol，未找到时等待同步或手动选择" options={[...(unavailableOriginalTarget?[{value:originalTargetID,label:'原指定模型已失效，请重新选择',disabled:true}]:[]),...available.map(model=>({value:model.id,label:nameOf(model)}))]}/></Form.Item>
   <div className="channel-editor-model-list"><div className="channel-editor-model-heading"><h3>关联模型 <Tag>{groupModels.length}</Tag></h3>{record&&<a href={`/admin/models?channel=${encodeURIComponent(record.id)}`} target="_blank" rel="noopener noreferrer">模型管理 <ExportOutlined aria-hidden="true"/></a>}</div><Table<Model> rowKey="id" size="small" loading={modelsLoading} dataSource={groupModels.slice(0,5)} pagination={false} locale={{emptyText:record?'尚无关联模型，可保存并获取目录':'保存分组后可获取模型目录'}} columns={[{title:'模型',key:'model',render:(_:unknown,model:Model)=><div><strong>{nameOf(model)}</strong>{model.display_name&&model.display_name!==model.upstream_id&&<small>{model.upstream_id}</small>}{model.missing&&<Tag>上游目录缺失</Tag>}</div>},{title:'独立设置',key:'setting',render:(_:unknown,model:Model)=>{const reasons=exceptions(model);return <Space wrap>{reasons.length?reasons.map(reason=><Tag key={reason}>{reason}</Tag>):<Tag color="success">按分组设置</Tag>}</Space>}},{title:'操作',key:'action',width:82,render:(_:unknown,model:Model)=><a href={`/admin/models?model=${encodeURIComponent(model.id)}`} target="_blank" rel="noopener noreferrer">模型设置 <ExportOutlined aria-hidden="true"/></a>}]}/>{groupModels.length>5&&<small className="channel-editor-hint">显示前 5 / {groupModels.length} 个模型，其余可在模型管理查看。</small>}</div>
   {catalog}
   <div className="channel-editor-bulk"><div><h4>批量应用当前开关</h4><p>保存并覆盖所属 {groupModels.length} 个未归档模型的单独公开、监控及创作允许开关；定时创作仍只执行指定模型。</p></div><Button disabled={!record||!!busy} loading={busy==='apply'} onClick={()=>void perform('apply')}>保存并应用到全部模型</Button></div>
  </section>},
  {key:'reasoning',label:'推理参数',forceRender:true,children:<section className="channel-editor-section" aria-label="推理参数设置">
   {sectionTitle('分组推理默认值','适用于未单独设置这些参数的模型；模型显式配置优先。')}
   <ReasoningFields form={form} protocol={protocol}/>
   <Alert type="info" showIcon message="模型设置优先于分组默认值" description="选择上游默认时省略对应参数。不同模型支持的设置不同，按所选值发送，不自动降级。"/>
   {protocol!=='anthropic'&&<p className="channel-editor-hint">Thinking 模式与预算仅在生成协议为 Anthropic Messages 时显示。</p>}
   {!!available.length&&<div className="channel-editor-model-list"><h3>推理强度配置来源</h3><Table<Model> rowKey="id" size="small" dataSource={available.slice(0,3)} pagination={false} columns={[{title:'模型',key:'model',render:(_:unknown,model:Model)=>nameOf(model)},{title:'配置来源',key:'source',render:(_:unknown,model:Model)=>model.reasoning_effort?'模型单独设置':'继承分组'},{title:'生效值',key:'value',render:(_:unknown,model:Model)=><Tag>{reasoningLabel(model.reasoning_effort||values.reasoning_effort)}</Tag>}]}/></div>}
  </section>},
  {key:'advanced',label:'高级设置',forceRender:true,children:<section className="channel-editor-section" aria-label="高级设置">
   {sectionTitle('接口路径与兼容设置','自定义路径替换整个地址路径；留空才使用协议默认路径。')}
   <div className="channel-editor-form-grid"><Form.Item name="models_path" label="模型列表路径" rules={pathRules}><Input placeholder="留空使用默认 /models" maxLength={4096}/></Form.Item><Form.Item name="generate_path" label="生成路径" rules={pathRules}><Input placeholder={`留空使用默认 ${suffix}`} maxLength={4096}/></Form.Item></div>
   <div className="channel-editor-endpoints"><p><span>模型列表：</span><code>{endpointPreview(values.base_url,values.models_path)}</code></p><p><span>生成接口：</span><code>{endpointPreview(values.base_url,values.generate_path,suffix)}</code></p></div>
   <div className="channel-editor-form-grid">{protocol==='chat'&&<Form.Item name="chat_token_field" label="Chat Token 字段"><Select options={[{label:'max_tokens',value:'max_tokens'},{label:'max_completion_tokens',value:'max_completion_tokens'}]}/></Form.Item>}{(protocol==='anthropic'||discovery==='anthropic')&&<Form.Item name="anthropic_version" label="Anthropic Version"><Input placeholder="2023-06-01" maxLength={100}/></Form.Item>}<Form.Item name="sort" label="排序" extra="数值越小，在列表中越靠前。"><InputNumber style={{width:'100%'}}/></Form.Item></div>
   {sectionTitle('令牌绑定组验证','可选，仅查询 Key 的绑定信息，不证明请求实际路由；未配置不影响模型调用。')}
   <Form.Item name="group_provider" label="分组查询方式"><Select options={[{value:'none',label:'不支持 / 不查询'},{value:'newapi',label:'New API 令牌信息'},{value:'custom',label:'自定义接口'}]}/></Form.Item>
   {provider==='newapi'&&<div className="channel-editor-endpoints"><p><span>固定查询路径：</span><code>/api/token/self</code></p><small>仅查询令牌绑定组信息，不读取账户列表。</small></div>}
   {provider==='custom'&&<div className="channel-editor-form-grid"><Form.Item name="group_path" label="分组查询路径" rules={[{required:true,message:'请输入自定义查询路径'},...pathRules]}><Input placeholder="例如 /api/token/info" maxLength={4096}/></Form.Item><Form.Item name="group_field" label="分组字段路径" rules={[{pattern:/^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$/,message:'请使用 JSON 点路径，例如 data.group'}]}><Input placeholder="默认 data.group" maxLength={256}/></Form.Item></div>}
   <div className="channel-editor-verification"><Tag>{provider==='none'?'未配置，可选':verificationLabel({status:record?.group_verification_status||'unverified',group:record?.verified_group})}</Tag>{record?.group_verified_at&&<span>最近查询 {time(record.group_verified_at)}</span>}{provider!=='none'&&record?.group_verification_error&&<p>{record.group_verification_error}</p>}</div>
  </section>},
 ];

 return <Modal centered className="channel-editor-modal" title={<div className="channel-editor-title"><span className="channel-editor-title-icon"><ApiOutlined aria-hidden="true"/></span><div><h2>{record?'编辑分组':'新增分组'}</h2><p>{platformName} / {values.name||'新分组'}</p></div></div>} open={open} onCancel={close} closable={!busy} maskClosable={!busy} keyboard={!busy} width={1280} destroyOnClose footer={<div className="channel-editor-footer"><div className="channel-editor-footer-note"><span>切换标签保留输入，保存时统一提交。</span><small>改动的主开关统一生效；未改动项保留模型单独设置。</small></div><Space><Button disabled={!!busy} onClick={close}>取消</Button><Button type="primary" loading={busy==='save'} disabled={!!busy} onClick={()=>void perform('save')}>保存更改</Button></Space></div>}>
  <Form form={form} layout="vertical" preserve disabled={!!busy} onFinish={()=>void perform('save')}>
   {error&&<Alert className="channel-editor-error" type="error" showIcon message={error}/>}
   <div className="channel-editor-layout" ref={contentRef}><div className="channel-editor-fields"><Tabs className="channel-editor-tabs" activeKey={tab} onChange={key=>changeTab(key as EditorTab)} items={tabs} destroyInactiveTabPane={false}/></div>
    <aside className="channel-editor-summary" role="region" aria-label="分组摘要">
     <span className="channel-editor-summary-eyebrow">本次配置</span><h3>{selectedName}</h3><p className="channel-editor-summary-platform">{platformName}</p>
     <div className="channel-editor-summary-tags"><Tag color={values.enabled?'success':'default'}>{values.enabled?'分组启用':'分组停用'}</Tag><Tag>{values.public?'公开展示':'私有'}</Tag></div>
     <dl><div><dt>目录模型</dt><dd>{modelsLoading?'读取中':`${groupModels.length} 个`}</dd></div><div><dt>生成协议</dt><dd>{protocolName(protocol)}</dd></div><div><dt>推理强度</dt><dd>{reasoningLabel(values.reasoning_effort)}</dd></div><div><dt>密钥状态</dt><dd className="summary-key-state">{values.api_key?'新密钥待保存':record?'已加密保存':'未设置'}</dd></div><div className="summary-target-row"><dt title={targetCaption}>测试模型</dt><dd>{targetModel?nameOf(targetModel):values.intelligence_model_id?'原目标不可用':'等待指定模型'}</dd></div></dl>
     <p className={`summary-target-description${!targetModel||!values.enabled||legacyAll?' is-waiting':''}`}>{targetDescription}</p>
     <div className="summary-task-list"><h4>分组任务许可</h4>{([['probe_enabled','健康检测'],['artifact_chinese','中文 HTML'],['artifact_english','英文 SVG']] as const).map(([key,label])=><div key={key}><span>{label}</span><strong className={controls[key]?'enabled':'disabled'}>{controls[key]?'允许':'关闭'}</strong></div>)}</div>
     {targetExceptions.length>0?<div className="summary-exceptions"><strong>指定模型的单独设置</strong><p>{targetExceptions.join(' · ')}</p></div>:targetModel&&<p className="summary-no-exceptions"><CheckCircleOutlined aria-hidden="true"/> 指定模型无单独关闭项</p>}
     {exceptionCount>0&&<Button className="channel-editor-exceptions-link" type="link" size="small" onClick={()=>changeTab('models')}>查看目录独立设置（{exceptionCount}）</Button>}
     {record?.last_sync_at&&<small className="channel-editor-summary-sync">最近同步 {time(record.last_sync_at)}</small>}
    </aside>
   </div>
  </Form>
 </Modal>;
}
