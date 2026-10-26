import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp, ConfigProvider } from 'antd';
import { bindSessionCache } from './sessionCache';
import { AppRoutes } from './App';
import zhCN from 'antd/es/locale/zh_CN';
import './styles.css';
import './admin-shell.css';
import './member.css';

const client = new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 15000, refetchOnWindowFocus: false } } });
bindSessionCache(client);

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider locale={zhCN} theme={{ token: { colorPrimary: '#15796f', colorInfo: '#15796f', colorSuccess: '#2c9a78', colorWarning: '#d49a39', colorError: '#d66a69', colorText: '#20313e', colorTextSecondary: '#7b8791', colorBgLayout: '#f7f8f6', colorBorder: '#e2e7e4', borderRadius: 8, fontFamily: '"Inter", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif', fontSize: 13, controlHeight: 36 }, components: { Button: { primaryShadow: 'none' }, Table: { headerBg: '#f6f8f7', headerColor: '#829089', rowHoverBg: '#f6faf8' }, Tabs: { itemSelectedColor: '#15796f' }, Modal: { paddingContentHorizontalLG: 24 } } }}>
      <AntApp><QueryClientProvider client={client}><BrowserRouter><AppRoutes/></BrowserRouter></QueryClientProvider></AntApp>
    </ConfigProvider>
  </React.StrictMode>,
);
