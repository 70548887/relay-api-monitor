import { useState } from 'react';
import { Alert, Button, Form, Input, Spin } from 'antd';
import { ArrowLeftOutlined, LockOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Navigate, NavLink, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { api, APIError, post, refreshSession, setSession, useSession } from '../api';
import { adminLoginPath, safeAdminNext } from '../adminRoutes';
import type { Session } from '../types';
import { AdminShell } from './AdminShell';

function useAdminAccess() {
  const session = useSession();
  const status = useQuery({ queryKey: ['setup-status'], queryFn: () => api<{ initialized: boolean }>('/setup/status'), retry: false });
  const auth = useQuery({ queryKey: ['session'], queryFn: ({ signal }) => refreshSession(signal), staleTime: 0, gcTime: 0, enabled: status.data?.initialized === true, retry: false });
  return { session, status, auth };
}

function AccessLoading() {
  return <div className="admin-access-state"><Spin size="large" tip="正在打开管理工作台"><div/></Spin></div>;
}

export function AdminLayout() {
  const { session, status, auth } = useAdminAccess();
  const location = useLocation();
  const next = safeAdminNext(`${location.pathname}${location.search}${location.hash}`);
  if (status.isLoading || auth.isLoading) return <AccessLoading/>;
  if (status.error) return <div className="admin-access-state"><Alert type="error" message="初始化状态读取失败" description={status.error.message} action={<Button onClick={() => void status.refetch()}>重试</Button>}/></div>;
  if (status.data?.initialized === false) return <Navigate to={`/admin/setup?next=${encodeURIComponent(next)}`} replace/>;
  if (!session) return <Navigate to={adminLoginPath(next)} replace/>;
  return <AdminShell session={session}/>;
}

export function AdminLoginPage() { return <AdminAuthPage/>; }
export function AdminSetupPage() { return <AdminAuthPage setup/>; }

function AdminAuthPage({ setup = false }: { setup?: boolean }) {
  const { session, status, auth } = useAdminAccess();
  const [search] = useSearchParams();
  const next = safeAdminNext(search.get('next'));
  if (status.isLoading || auth.isLoading) return <AccessLoading/>;
  if (status.error) return <div className="admin-access-state"><Alert type="error" message="初始化状态读取失败" description={status.error.message} action={<Button onClick={() => void status.refetch()}>重试</Button>}/></div>;
  if (!setup && status.data?.initialized === false) return <Navigate to={`/admin/setup?next=${encodeURIComponent(next)}`} replace/>;
  if (setup && status.data?.initialized === true) return <Navigate to={session ? next : adminLoginPath(next)} replace/>;
  if (session) return <Navigate to={next} replace/>;
  return <LoginForm setup={setup} next={next} error={auth.error instanceof APIError && auth.error.status !== 401 ? auth.error.message : undefined}/>;
}

function LoginForm({ setup, next, error }: { setup: boolean; next: string; error?: string }) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(values: { username: string; password: string }) {
    setBusy(true);
    setFailure('');
    try {
      const credentials={username:values.username,password:values.password};
      if (setup) await post('/setup', credentials);
      const session = await post<Session>('/auth/login', credentials);
      client.setQueryData(['setup-status'], { initialized: true });
      client.setQueryData(['session'], session);
      setSession(session);
      navigate(next, { replace: true });
    } catch (error) {
      setFailure((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="admin-auth-page">
    <header className="admin-auth-header"><NavLink to="/admin/login" className="admin-brand"><div className="brand-mark">r<span>↗</span></div><div><strong>Relay Lab<span className="brand-dot">.</span></strong><span>CONTROL CENTER</span></div></NavLink><NavLink to="/" className="admin-public-link"><ArrowLeftOutlined aria-hidden="true"/>访问用户端</NavLink></header>
    <main className="admin-auth-main">
      <section className="admin-auth-intro"><div className="eyebrow">RELAY LAB / ADMINISTRATION</div><h2>让观测，<br/>井然有序。</h2><p>管理中转平台与模型，编排自动检测，<br/>让每一次执行都有迹可循。</p><div className="admin-auth-graphic" aria-hidden="true"><span/><span/><span/><div>R↗</div></div><span className="admin-auth-intro-note">专注观测 · 从容管理</span></section>
      <section className="admin-auth-card">
        <div className="admin-auth-lock"><LockOutlined aria-hidden="true"/></div><div className="admin-auth-card-label">管理工作台</div><h1>{setup ? '创建首个管理员' : '欢迎回来'}</h1><p>{setup ? '首次启动，请创建管理员账号。' : '登录以管理你的中转观测站。'}</p>
        {(failure || error) && <Alert message={failure || error} type="error" showIcon/>}
        <Form layout="vertical" requiredMark={false} onFinish={values => void submit(values)}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}><Input size="large" autoComplete="username" placeholder="管理员用户名"/></Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }, ...(setup ? [{ min: 12, message: '请使用至少 12 位密码' }] : [])]}><Input.Password size="large" autoComplete={setup ? 'new-password' : 'current-password'} placeholder={setup ? '至少 12 位密码' : '输入登录密码'}/></Form.Item>
          {setup && <Form.Item label="确认密码" name="confirm" dependencies={['password']} rules={[{ required: true, message: '请再次输入密码' }, ({ getFieldValue }) => ({ validator: (_, value) => !value || getFieldValue('password') === value ? Promise.resolve() : Promise.reject(new Error('两次密码不一致')) })]}><Input.Password size="large" autoComplete="new-password"/></Form.Item>}
          <Button type="primary" htmlType="submit" block size="large" loading={busy}>{setup ? '创建管理员并登录' : '登录工作台'}</Button>
        </Form>
        <div className="admin-auth-card-note"><LockOutlined aria-hidden="true"/>独立管理入口 · 安全会话保护</div>
      </section>
    </main>
    <footer className="admin-auth-footer">Relay Lab · 中转观测站管理工作台</footer>
  </div>;
}
