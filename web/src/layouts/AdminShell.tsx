import { useState } from 'react';
import { Alert, Button } from 'antd';
import { ApiOutlined, AppstoreOutlined, ArrowLeftOutlined, CloudDownloadOutlined, DashboardOutlined, FileTextOutlined, LogoutOutlined, SettingOutlined, UserOutlined } from '@ant-design/icons';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { post, setSession } from '../api';
import type { Session } from '../types';

const destinations = [
  { to: '/admin', label: '总览', icon: <DashboardOutlined aria-hidden="true"/>, end: true },
  { to: '/admin/platforms', label: '平台与分组', icon: <ApiOutlined aria-hidden="true"/> },
  { to: '/admin/models', label: '模型配置', icon: <AppstoreOutlined aria-hidden="true"/> },
  { to: '/admin/jobs', label: '任务与作品', icon: <FileTextOutlined aria-hidden="true"/> },
  { to: '/admin/updates', label: '在线升级', icon: <CloudDownloadOutlined aria-hidden="true"/> },
  { to: '/admin/user-connections', label: '用户连接', icon: <ApiOutlined aria-hidden="true"/> },
  { to: '/admin/test-records', label: '在线测试记录', icon: <FileTextOutlined aria-hidden="true"/> },
  { to: '/admin/users', label: '用户管理', icon: <UserOutlined aria-hidden="true"/> },
  { to: '/admin/settings', label: '系统设置', icon: <SettingOutlined aria-hidden="true"/> },
];

export function AdminShell({ session }: { session: Session }) {
  const location = useLocation();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState('');
  const current = destinations.find(item => item.to === location.pathname.replace(/\/$/, '')) || destinations[0];

  async function logout() {
    setBusy(true);
    setFailure('');
    try {
      await post('/auth/logout');
      setSession(null);
      navigate('/admin/login', { replace: true });
    } catch (error) {
      setFailure((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="admin-workspace">
    <header className="admin-topbar">
      <NavLink to="/admin" className="admin-brand" aria-label="Relay Lab 管理总览">
        <div className="brand-mark">r<span>↗</span></div>
        <div><strong>Relay Lab<span className="brand-dot">.</span></strong><span>CONTROL CENTER</span></div>
      </NavLink>
      <div className="admin-topbar-right"><span className="admin-mode-label">管理工作台</span><NavLink to="/" className="admin-public-link"><ArrowLeftOutlined aria-hidden="true"/>访问用户端</NavLink></div>
    </header>
    <div className="admin-workspace-body">
      <aside className="admin-workspace-sidebar">
        <div className="admin-workspace-caption">工作空间<span>WORKSPACE</span></div>
        <nav aria-label="管理导航">{destinations.map(item => <NavLink key={item.to} to={item.to} end={item.end}>{item.icon}<span>{item.label}</span></NavLink>)}</nav>
        <div className="admin-account">
          <div className="admin-account-avatar" aria-hidden="true">{session.user.username.slice(0, 1).toUpperCase()}</div>
          <div className="admin-account-name"><strong>{session.user.username}</strong><span>管理员</span></div>
          <Button type="text" title="退出登录" aria-label="退出登录" icon={<LogoutOutlined aria-hidden="true"/>} loading={busy} onClick={() => void logout()}/>
        </div>
      </aside>
      <main className="admin-workspace-main">
        <div className="admin-breadcrumb" aria-label="当前位置"><span>控制中心</span><span aria-hidden="true">/</span><strong>{current.label}</strong></div>
        {failure && <Alert className="admin-shell-error" type="error" message="退出登录失败" description={failure} showIcon closable onClose={() => setFailure('')}/>}
        <div className="admin-content"><Outlet/></div>
        <footer className="admin-workspace-footer"><span>Relay Lab · 管理工作台</span><span>CONTROL CENTER</span></footer>
      </main>
    </div>
  </div>;
}
