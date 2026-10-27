import '../private-tests.css';
import {useEffect,useState} from 'react';
import {Alert,Button,Form,Input,Modal,Popconfirm,Select,Space,Table,Tag} from 'antd';
import {Link} from 'react-router-dom';
import {useQuery,useQueryClient} from '@tanstack/react-query';
import {api} from '../api';
import {userApi,useUserSession} from '../userApi';
import type {Connection} from '../privateTests';
import {privateNotice} from '../privateTests';
import {PrivateTestNav} from '../components/PrivateTestNav';
import {PageHeading} from '../components/Common';
import {time} from '../lib';

export default function Connections({admin=false}:{admin?:boolean}){
 const session=useUserSession();const client=useQueryClient();const [edit,setEdit]=useState<Connection>();const [open,setOpen]=useState(false);const [error,setError]=useState('');const [busy,setBusy]=useState(false);const [form]=Form.useForm();
 const send=admin?api:userApi;const path=admin?'/admin/user-connections':'/connections';
 const query=useQuery({queryKey:['private-connections',admin?'admin':session?.user.id],queryFn:()=>send<Connection[]>(path),staleTime:0,gcTime:0});
 useEffect(()=>()=>form.resetFields(),[form]);
 const show=(item?:Connection)=>{setEdit(item);form.resetFields();form.setFieldsValue(item?{...item,api_key:''}:{name:'',base_url:'',protocol:'chat',discovery_protocol:'openai',anthropic_version:'2023-06-01',chat_token_field:'max_tokens'});setOpen(true);setError('')};
 async function perform(fn:()=>Promise<unknown>){setBusy(true);setError('');try{await fn();await client.invalidateQueries({queryKey:['private-connections']})}catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 return <><PageHeading eyebrow={admin?'ADMIN / 用户连接':'ACCOUNT / 我的连接'} title={admin?'用户连接':'保存的连接'} description={admin?'查看连接状态，可禁用连接；密钥不提供查看或代测。':privateNotice} extra={!admin&&<Button type="primary" onClick={()=>show()}>新增连接</Button>}/>{!admin&&<PrivateTestNav active="connections"/>}{(error||query.error)&&<Alert type="error" showIcon message={error||query.error?.message}/>}<section className="panel private-connections-panel"><Table rowKey="id" loading={query.isLoading} dataSource={query.data||[]} scroll={{x:900}} columns={[
 ...(admin?[{title:'用户',dataIndex:'username'}]:[]),{title:'连接',dataIndex:'name'},{title:'接口',dataIndex:'base_url'},{title:'协议',dataIndex:'protocol'},{title:'Key',dataIndex:'key_mask'},{title:'状态',render:(_:unknown,c:Connection)=><Tag color={c.disabled?'error':'success'}>{c.disabled?'管理员已禁用':'可用'}</Tag>},{title:'最近模型读取',render:(_:unknown,c:Connection)=><span>{time(c.last_models_at)} · {c.last_models_count} 个</span>},{title:'操作',render:(_:unknown,c:Connection)=><Space>{admin?<Button disabled={busy} onClick={()=>void perform(()=>api(path+`/${c.id}/status`,{method:'PATCH',body:JSON.stringify({disabled:!c.disabled})}))}>{c.disabled?'恢复连接':'禁用连接'}</Button>:<><Link to={`/test?connection=${encodeURIComponent(c.id)}`}>使用</Link><Button onClick={()=>show(c)}>编辑</Button><Popconfirm title="删除连接及保存的 Key？历史记录仍保留。" onConfirm={()=>perform(()=>userApi(path+`/${c.id}`,{method:'DELETE'}))}><Button danger disabled={busy}>删除</Button></Popconfirm></>}</Space>}
 ]}/></section><Modal title={edit?'编辑连接':'保存连接'} open={open} destroyOnClose onCancel={()=>{setOpen(false);form.resetFields()}} confirmLoading={busy} onOk={()=>void form.validateFields().then(values=>perform(async()=>{await userApi('/connections'+(edit?`/${edit.id}`:''),{method:edit?'PUT':'POST',body:JSON.stringify({...values,revision:edit?.revision})});setOpen(false);form.resetFields()}))}>{error&&<Alert type="error" message={error}/>}<Form form={form} layout="vertical"><Form.Item name="name" label="连接名称" rules={[{required:true}]}><Input/></Form.Item><Form.Item name="base_url" label="接口地址" rules={[{required:true},{type:'url'}]}><Input/></Form.Item><Form.Item name="api_key" label="API Key" rules={edit?[]:[{required:true}]} extra={edit?'留空保留原密钥；更换域名、端口或协议需重新输入。':'加密保存后仅显示掩码。'}><Input.Password autoComplete="new-password"/></Form.Item><Form.Item name="protocol" label="生成协议"><Select options={[{value:'chat',label:'Chat Completions'},{value:'responses',label:'Responses'},{value:'anthropic',label:'Anthropic Messages'}]}/></Form.Item><Form.Item name="discovery_protocol" label="模型发现协议"><Select options={[{value:'openai',label:'OpenAI'},{value:'anthropic',label:'Anthropic'}]}/></Form.Item><Form.Item name="models_path" label="模型列表路径"><Input/></Form.Item><Form.Item name="generate_path" label="生成路径"><Input/></Form.Item><Form.Item name="anthropic_version" label="Anthropic Version"><Input/></Form.Item><Form.Item name="chat_token_field" label="Chat 输出字段"><Select options={[{value:'max_tokens'},{value:'max_completion_tokens'}]}/></Form.Item></Form></Modal></>;
}
