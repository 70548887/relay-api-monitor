import type { ReactNode } from 'react';
import { Alert, Button, Spin } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { Navigate, useLocation } from 'react-router-dom';
import { refreshUserSession, safeUserNext, useUserSession } from '../userApi';

export function RequireMember({ children }: { children: ReactNode }) {
  const session = useUserSession();
  const location = useLocation();
  const state = useQuery({ queryKey: ['user-session'], queryFn: ({ signal }) => refreshUserSession(signal), retry: false, staleTime: 30000 });
  const next = safeUserNext(`${location.pathname}${location.search}${location.hash}`);

  if (state.isLoading) return <div className="center-loading"><Spin size="large"/></div>;
  if (state.error) return <Alert type="error" showIcon message="用户登录状态读取失败" description={state.error.message} action={<Button onClick={() => void state.refetch()}>重试</Button>}/>;
  if (!session) return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace/>;
  return children;
}
