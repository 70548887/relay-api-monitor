import { test, expect, type Page } from '@playwright/test';
import type { UpdateSnapshot } from '../src/types';

function fixture():UpdateSnapshot {
  return {enabled:true,repository:'70548887/relay-api-monitor',launcher_managed:true,launcher_protocol:2,auto_restart:true,
    current:{version:'1.2.1',commit:'a'.repeat(40),os:'windows',arch:'amd64'},
    latest:{current_version:'1.2.1',latest_version:'v1.2.2',commit:'b'.repeat(40),compatible:true,available:true,asset_name:'relaylab-windows-amd64.exe',asset_size:2048,sha256:'c'.repeat(64),notes:'修复在线升级'},
    release:{tag_name:'v1.2.2',name:'Release v1.2.2 - 修复在线升级',assets:[{name:'relaylab-windows-amd64.exe',size:2048,sha256:'c'.repeat(64),verified:true}]},
    state:'available',operation:{id:'browser-op',state:'available',version:'v1.2.2',progress:0},maintenance:{active:false,draining_requests:0,running_jobs:0,worker_online:true}};
}
async function mount(page:Page,data:UpdateSnapshot) {
  const posts:string[]=[];
  await page.route('**/api/v1/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    if(path.endsWith('/setup/status'))return route.fulfill({json:{initialized:true}});
    if(path.endsWith('/auth/me'))return route.fulfill({json:{user:{id:'browser-admin',username:'browser-admin'},csrf_token:'test-csrf'}});
    if(path.endsWith('/updates/history'))return route.fulfill({json:{history:[]}});
    if(path.includes('/updates')){
      if(route.request().method()==='POST')posts.push(path);
      return route.fulfill({status:route.request().method()==='POST'?202:200,json:data});
    }
    return route.fulfill({json:{}});
  });
  await page.goto('/admin/updates');
  await expect(page.getByText('70548887/relay-api-monitor',{exact:true})).toBeVisible();
  return posts;
}

test('compatible available update can download but needs ready before install',async({page})=>{
  const data=fixture();const posts=await mount(page,data);
  await expect(page.getByRole('button',{name:'下载并校验'})).toBeEnabled();
  await expect(page.getByRole('button',{name:'安装升级',exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'下载并校验'}).click();
  await expect.poll(()=>posts).toContain('/api/v1/admin/updates/download');
  await expect(page.getByText('升级完成',{exact:true})).toHaveCount(0);
});

test('incompatible/current versions and old launcher disable actions',async({page})=>{
  const data=fixture();data.latest!.compatible=false;await mount(page,data);
  await expect(page.getByRole('button',{name:'下载并校验'})).toBeDisabled();
  data.latest!.compatible=true;data.latest!.available=false;
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  await expect(page.getByText('当前已是最新版本')).toBeVisible();
  await expect(page.getByRole('button',{name:'下载并校验'})).toBeDisabled();
  data.latest!.available=true;data.operation!.state='ready';data.launcher_protocol=1;
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  await expect(page.getByText('需要协议 2 启动器才能安装或回滚')).toBeVisible();
  await expect(page.getByRole('button',{name:'安装升级',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'回滚',exact:true})).toBeDisabled();
});

test('ready managed installation is accepted without premature success',async({page})=>{
  const data=fixture();data.operation!.state='ready';const posts=await mount(page,data);
  await page.getByRole('button',{name:'安装升级',exact:true}).click();
  data.operation!.state='validating';
  await page.getByRole('button',{name:'安装升级',exact:true}).last().click();
  await expect.poll(()=>posts).toContain('/api/v1/admin/updates/apply');
  await expect(page.getByText('正在验证网站与 Worker').first()).toBeVisible();
  await expect(page.getByText('升级完成',{exact:true})).toHaveCount(0);
});

test('pending restart and persisted failure describe actual outcome',async({page})=>{
  const data=fixture();data.auto_restart=false;data.operation!.state='pending_restart';await mount(page,data);
  await expect(page.getByText('升级包已准备，等待手动重启',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'准备升级并等待重启'})).toBeDisabled();
  data.operation={id:'failed-op',state:'failed',error_code:'health_check_failed',message:'目标 Worker 未启动'};
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  await expect(page.getByText('health_check_failed',{exact:true})).toBeVisible();
  await expect(page.getByText('目标 Worker 未启动').first()).toBeVisible();
});

test('download telemetry and maintenance counts fit mobile viewport',async({page})=>{
  await page.setViewportSize({width:390,height:844});
  const data=fixture();data.operation={id:'download',state:'downloading',version:'v1.2.2',progress:50,bytes_downloaded:1024,total_bytes:2048,bytes_per_second:512,eta_seconds:2};
  data.maintenance={active:true,draining_requests:3,running_jobs:2,worker_online:false};
  await mount(page,data);
  await expect(page.getByText('下载速度：512 B/s')).toBeVisible();
  await expect(page.getByText('预计剩余：2 秒')).toBeVisible();
  await expect(page.getByText('在途请求：3')).toBeVisible();
  await expect(page.getByText('运行任务：2')).toBeVisible();
  const size=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));
  expect(size.scroll).toBeLessThanOrEqual(size.width);
});

test('terminal success still requires actual target commit and worker',async({page})=>{
  const data=fixture();data.operation!.state='succeeded';data.current.version='1.2.2';data.maintenance!.worker_online=false;
  await mount(page,data);
  await expect(page.getByText('安装结果已返回，正在确认目标版本与 Worker').first()).toBeVisible();
  await expect(page.getByText('升级完成',{exact:true})).toHaveCount(0);
  data.current.commit=data.latest!.commit;data.maintenance!.worker_online=true;
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  await expect(page.getByText('升级完成',{exact:true}).first()).toBeVisible();
});
