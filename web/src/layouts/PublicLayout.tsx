import { useEffect } from 'react';
import { ArrowUpOutlined } from '@ant-design/icons';
import { useQuery,useQueryClient } from '@tanstack/react-query';
import { NavLink, Outlet } from 'react-router-dom';
import { refreshUserSession, useUserSession } from '../userApi';

export function PublicLayout() {
  const session = useUserSession();
 const client=useQueryClient();
 useEffect(()=>()=>{client.removeQueries({predicate:q=>String(q.queryKey[0]).startsWith('private-')})},[client,session?.user.id]);
  useQuery({ queryKey: ['user-session'], queryFn: ({ signal }) => refreshUserSession(signal), retry: false });
  return <div className="public-portal">
    <header className="site-header">
      <div className="header-inner">
        <NavLink to="/" className="brand" aria-label="Relay Lab 首页">
          <div className="brand-mark">r<span>↗</span></div>
          <div><strong>Relay Lab<span className="brand-dot">.</span></strong><span className="brand-subtitle">中转观测站</span></div>
        </NavLink>
        <nav className="top-nav" aria-label="主导航">
          <NavLink to="/" end>中转 API 监控</NavLink>
          <NavLink to="/intelligence">智商测试</NavLink>
          <NavLink to="/test">在线测试</NavLink>
        </nav>
        <div className="public-account-nav member-nav" aria-label="用户账号">{session ? <NavLink to="/account">我的账号</NavLink> : <><NavLink to="/login">登录</NavLink><NavLink to="/register" className="public-register-link">注册</NavLink></>}</div>
      </div>
    </header>
    <main className="main"><Outlet/></main>
    <footer className="site-footer">
      <div><span className="footer-brand">Relay Lab.</span><span>让每一个模型的表现，都有据可依。</span></div>
      <span className="footer-side">BUILT FOR CLARITY <ArrowUpOutlined aria-hidden="true" rotate={45}/></span>
    </footer>
  </div>;
}
