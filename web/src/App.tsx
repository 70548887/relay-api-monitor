import { Suspense, lazy } from 'react';
import { NavLink, Route, Routes } from 'react-router-dom';
import { Spin } from 'antd';
import { PublicLayout } from './layouts/PublicLayout';
import { RequireMember } from './layouts/UserGuard';

const Connections = lazy(() => import('./pages/Connections'));
const TestRecords = lazy(() => import('./pages/TestRecords'));
const Monitor = lazy(() => import('./pages/Monitor'));
const Gallery = lazy(() => import('./pages/Gallery'));
const OnlineTest = lazy(() => import('./pages/OnlineTest'));
const AdminLayout = lazy(() => import('./layouts/AdminAuth').then(module => ({ default: module.AdminLayout })));
const AdminLoginPage = lazy(() => import('./layouts/AdminAuth').then(module => ({ default: module.AdminLoginPage })));
const AdminSetupPage = lazy(() => import('./layouts/AdminAuth').then(module => ({ default: module.AdminSetupPage })));
const OverviewPage = lazy(() => import('./pages/Admin').then(module => ({ default: module.OverviewPage })));
const PlatformsPage = lazy(() => import('./pages/Admin').then(module => ({ default: module.PlatformsPage })));
const ModelsPage = lazy(() => import('./pages/Admin').then(module => ({ default: module.ModelsPage })));
const JobsPage = lazy(() => import('./pages/Admin').then(module => ({ default: module.JobsPage })));
const SettingsPage = lazy(() => import('./pages/Admin').then(module => ({ default: module.SettingsPage })));
const UpdatesPage = lazy(() => import('./pages/Updates').then(module => ({ default: module.UpdatesPage })));
const UsersPage = lazy(() => import('./pages/Users').then(module => ({ default: module.UsersPage })));
const UserLoginPage = lazy(() => import('./pages/UserAccount').then(module => ({ default: module.UserLoginPage })));
const UserRegisterPage = lazy(() => import('./pages/UserAccount').then(module => ({ default: module.UserRegisterPage })));
const UserAccountPage = lazy(() => import('./pages/UserAccount').then(module => ({ default: module.UserAccountPage })));

export function AppRoutes() {
  return <Suspense fallback={<div className="center-loading"><Spin size="large"/></div>}><Routes>
    <Route path="/admin/login" element={<AdminLoginPage/>}/>
    <Route path="/admin/setup" element={<AdminSetupPage/>}/>
    <Route path="/admin" element={<AdminLayout/>}>
      <Route index element={<OverviewPage/>}/>
      <Route path="platforms" element={<PlatformsPage/>}/>
      <Route path="models" element={<ModelsPage/>}/>
      <Route path="jobs" element={<JobsPage/>}/>
      <Route path="settings" element={<SettingsPage/>}/>
      <Route path="updates" element={<UpdatesPage/>}/>
      <Route path="users" element={<UsersPage/>}/>
 <Route path="user-connections" element={<Connections admin/>}/>
 <Route path="test-records" element={<TestRecords admin/>}/>
      <Route path="*" element={<div className="empty-panel panel"><h1>管理页面不存在</h1><NavLink to="/admin">返回管理总览</NavLink></div>}/>
    </Route>
    <Route element={<PublicLayout/>}>
      <Route index element={<Monitor/>}/>
      <Route path="intelligence" element={<Gallery/>}/>
      <Route path="test" element={<RequireMember><OnlineTest/></RequireMember>}/>
      <Route path="login" element={<UserLoginPage/>}/>
      <Route path="register" element={<UserRegisterPage/>}/>
      <Route path="account" element={<UserAccountPage/>}/>
 <Route path="account/connections" element={<RequireMember><Connections/></RequireMember>}/>
 <Route path="account/tests" element={<RequireMember><TestRecords/></RequireMember>}/>
 <Route path="account/tests/:id" element={<RequireMember><TestRecords/></RequireMember>}/>
      <Route path="*" element={<div className="empty-panel panel"><h1>页面不存在</h1><NavLink to="/">返回监控首页</NavLink></div>}/>
    </Route>
  </Routes></Suspense>;
}
