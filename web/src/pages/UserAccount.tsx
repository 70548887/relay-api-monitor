import { useState } from 'react';
import { Link, Navigate, useSearchParams } from 'react-router-dom';
import { useQuery,useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Form, Input, Spin } from 'antd';
import { ApiOutlined, ArrowRightOutlined, CheckCircleFilled, ExperimentOutlined, HistoryOutlined, LockOutlined, LogoutOutlined, SafetyOutlined, UserOutlined } from '@ant-design/icons';
import '../account.css';
import { PageHeading } from '../components/Common';
import { refreshUserSession,safeUserNext,setUserSession,userPost,useUserSession,type UserSession } from '../userApi';

function useMember(){return useQuery({queryKey:['user-session'],queryFn:({signal})=>refreshUserSession(signal),retry:false,staleTime:30000})}

function MemberAccess({register=false}:{register?:boolean}){
 const q=useMember();const session=useUserSession();const [params]=useSearchParams();const next=safeUserNext(params.get('next'));const [error,setError]=useState('');const [busy,setBusy]=useState(false);const client=useQueryClient();
 if(q.isLoading)return <div className="center-loading"><Spin/></div>;
 if(session)return <Navigate to={next} replace/>;
 return <div className="member-access"><section className="member-access-intro"><div className="eyebrow">RELAY LAB / 用户中心</div><h1>{register?'创建你的用户账号':'登录用户中心'}</h1><p>查看模型状态、比较生成作品，在自己的账号下使用在线测试。</p><ul><li>监控与作品随时查看</li><li>账号设置和登录状态集中管理</li><li>在线测试使用你自己的 API Key</li></ul></section><section className="panel member-access-form"><UserOutlined aria-hidden="true"/><h2>{register?'注册用户账号':'欢迎回来'}</h2><p className="muted">{register?'填写用户名和密码即可创建普通用户账号。':'使用普通用户账号登录。'}</p>{(error||q.error)&&<Alert showIcon type="error" message={error||q.error?.message}/>}<Form layout="vertical" onFinish={async(values:{username:string;password:string})=>{setBusy(true);setError('');try{const data=await userPost<UserSession>(register?'/auth/register':'/auth/login',{username:values.username,password:values.password});setUserSession(data);client.setQueryData(['user-session'],data)}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}><Form.Item name="username" label="用户名" rules={[{required:true,message:'请输入用户名'},{max:100,message:'用户名过长'}]}><Input autoComplete="username" size="large"/></Form.Item><Form.Item name="password" label="密码" rules={[{required:true,message:'请输入密码'},...(register?[{min:12,message:'密码至少 12 位'}]:[])]}><Input.Password autoComplete={register?'new-password':'current-password'} size="large"/></Form.Item>{register&&<Form.Item name="confirm" label="确认密码" dependencies={['password']} rules={[{required:true,message:'请再次输入密码'},({getFieldValue})=>({validator:(_,value)=>value===getFieldValue('password')?Promise.resolve():Promise.reject(new Error('两次密码不一致'))})]}><Input.Password autoComplete="new-password" size="large"/></Form.Item>}<Button type="primary" htmlType="submit" block size="large" loading={busy}>{register?'注册并登录':'登录用户端'}</Button></Form><p className="member-access-switch">{register?'已有账号？':'还没有账号？'} <Link to={`${register?'/login':'/register'}?next=${encodeURIComponent(next)}`}>{register?'前往登录':'注册账号'}</Link></p></section></div>
}
export function UserLoginPage(){return <MemberAccess/>}
export function UserRegisterPage(){return <MemberAccess register/>}
export function UserAccountPage() {
 const q = useMember();
 const session = useUserSession();
 const client = useQueryClient();
 const { message } = App.useApp();
 const [action, setAction] = useState<'password' | 'logout' | null>(null);
 const [error, setError] = useState('');
 const [form] = Form.useForm();
 if (q.isLoading) return <div className="center-loading"><Spin/></div>;
 if (q.error) return <Alert showIcon type="error" message="账号状态读取失败" description={q.error.message} action={<Button onClick={() => void q.refetch()}>重试</Button>}/>;
 if (!session) return <Navigate to="/login?next=%2Faccount" replace/>;

 const signOut = async () => {
  setAction('logout');
  try {
   await userPost('/auth/logout');
   setUserSession(null);
   client.setQueryData(['user-session'], null);
  } catch (e) { void message.error((e as Error).message); }
  finally { setAction(null); }
 };
 const changePassword = async (values: { old_password: string; new_password: string }) => {
  setAction('password');
  setError('');
  try {
   await userPost('/auth/password', { old_password: values.old_password, new_password: values.new_password });
   form.resetFields();
   setUserSession(null);
   client.setQueryData(['user-session'], null);
   void message.success('密码已修改，请重新登录');
  } catch (e) { setError((e as Error).message); }
  finally { setAction(null); }
 };

 return <div className="account-page">
  <PageHeading eyebrow="ACCOUNT / 用户中心" title="我的账号" description="管理个人资料、测试连接与登录安全。"/>
  <div className="account-layout">
   <div className="account-sidebar">
    <section className="panel account-profile" aria-labelledby="account-profile-title">
     <div className="account-section-heading">
      <h2 id="account-profile-title">账号信息</h2>
      <span className="account-status"><CheckCircleFilled aria-hidden="true"/> 账号正常</span>
     </div>
     <div className="account-identity">
      <span className="account-avatar" aria-hidden="true">{Array.from(session.user.username)[0]?.toUpperCase() || 'U'}</span>
      <div><strong>{session.user.username}</strong><span>普通用户 · 个人空间</span></div>
     </div>
     <dl className="account-facts">
      <div><dt>用户名</dt><dd>{session.user.username}</dd></div>
      <div><dt>账号类型</dt><dd>普通用户</dd></div>
     </dl>
     <div className="account-profile-footer">
      <Button icon={<LogoutOutlined aria-hidden="true"/>} loading={action === 'logout'} disabled={action === 'password'} onClick={() => void signOut()}>退出用户登录</Button>
     </div>
    </section>
    <section className="panel account-shortcuts" aria-labelledby="account-shortcuts-title">
     <h2 id="account-shortcuts-title">快捷入口</h2>
     <nav aria-label="账号快捷入口">
      <Link to="/account/connections" className="account-shortcut">
       <span className="account-shortcut-icon"><ApiOutlined aria-hidden="true"/></span>
       <span className="account-shortcut-text"><strong>我的连接</strong><span>管理 API 地址与已保存的密钥</span></span>
       <ArrowRightOutlined aria-hidden="true" className="account-shortcut-arrow"/>
      </Link>
      <Link to="/account/tests" className="account-shortcut">
       <span className="account-shortcut-icon"><HistoryOutlined aria-hidden="true"/></span>
       <span className="account-shortcut-text"><strong>我的测试记录</strong><span>查看历史响应与生成作品</span></span>
       <ArrowRightOutlined aria-hidden="true" className="account-shortcut-arrow"/>
      </Link>
      <Link to="/test" className="account-shortcut">
       <span className="account-shortcut-icon"><ExperimentOutlined aria-hidden="true"/></span>
       <span className="account-shortcut-text"><strong>在线测试</strong><span>选择连接，开始一次新的测试</span></span>
       <ArrowRightOutlined aria-hidden="true" className="account-shortcut-arrow"/>
      </Link>
     </nav>
    </section>
   </div>
   <section className="panel account-security" aria-labelledby="account-security-title">
    <div className="account-security-heading">
     <span className="account-security-icon"><SafetyOutlined aria-hidden="true"/></span>
     <div><h2 id="account-security-title">修改密码</h2><p>更新登录密码，保护你的个人账号。</p></div>
    </div>
    <div className="account-security-notice"><LockOutlined aria-hidden="true"/><p>修改后需要重新登录，其他设备上的登录也会失效。</p></div>
    {error && <Alert showIcon type="error" message={error} className="account-form-error"/>}
    <Form form={form} layout="vertical" requiredMark={false} disabled={action !== null} onFinish={changePassword} className="account-password-form">
     <Form.Item name="old_password" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}>
      <Input.Password autoComplete="current-password" placeholder="输入当前使用的密码" size="large"/>
     </Form.Item>
     <Form.Item name="new_password" label="新密码" extra="至少 12 个字符，建议组合字母、数字和符号。" rules={[{ required: true, message: '请输入新密码' }, { min: 12, message: '密码至少 12 位' }]}>
      <Input.Password autoComplete="new-password" placeholder="设置新的登录密码" size="large"/>
     </Form.Item>
     <Form.Item name="confirm" label="确认新密码" dependencies={['new_password']} rules={[{ required: true, message: '请再次输入新密码' }, ({ getFieldValue }) => ({ validator: (_, value) => value === getFieldValue('new_password') ? Promise.resolve() : Promise.reject(new Error('两次密码不一致')) })]}>
      <Input.Password autoComplete="new-password" placeholder="再次输入新密码" size="large"/>
     </Form.Item>
     <div className="account-form-footer">
      <span>保存后将返回登录页面</span>
      <Button type="primary" htmlType="submit" size="large" loading={action === 'password'}>保存新密码</Button>
     </div>
    </Form>
   </section>
  </div>
 </div>;
}
