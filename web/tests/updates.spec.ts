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
  const refreshed=page.waitForEvent('framenavigated',{predicate:frame=>frame===page.mainFrame()});
  data.current.commit=data.latest!.commit;data.maintenance!.worker_online=true;
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  // The confirmed build deliberately reloads embedded assets. Wait for the
  // new document and restored authentication before assertions and teardown.
  await refreshed;
  await expect(page.getByText('70548887/relay-api-monitor',{exact:true})).toBeVisible();
  await expect(page.getByText('升级完成',{exact:true}).first()).toBeVisible();
  expect(await page.evaluate(()=>sessionStorage.getItem('relaylab-update-refresh:browser-op'))).toBe('done');
});

for(const viewport of [{name:'desktop',width:1280,height:900},{name:'mobile',width:390,height:844}]){
  test(`persistent version notice fits ${viewport.name} and opens update details`,async({page})=>{
    await page.setViewportSize({width:viewport.width,height:viewport.height});
    const data=fixture();data.current.version='1.4.2';data.latest!.current_version='1.4.2';data.latest!.latest_version='v1.4.3';data.install_enabled=false;data.install_mode='container';
    const posts=await mount(page,data);
    const versionLink=page.getByRole('link',{name:/当前版本 v1.4.2，发现新版本 v1.4.3/});
    await expect(versionLink).toBeVisible();
    await expect(versionLink).toHaveAttribute('href','/admin/updates');
    await expect(versionLink.getByText('新版本 v1.4.3')).toBeVisible();
    await expect(page.getByRole('navigation',{name:'管理导航'}).getByRole('img',{name:'有新版本'})).toHaveCount(1);
    await expect(page.getByText('Docker 部署：支持版本检查与更新提示',{exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:'检查更新',exact:true})).toBeEnabled();
    for(const name of ['下载并校验','安装升级','回滚'])await expect(page.getByRole('button',{name,exact:true})).toBeDisabled();
    const size=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));
    expect(size.scroll).toBeLessThanOrEqual(size.width);
    await page.screenshot({path:`../release/version-notice-20261029/${viewport.name}.jpg`,quality:80,fullPage:false});
    await versionLink.click();
    await expect(page).toHaveURL(/\/admin\/updates$/);
    expect(posts).toEqual([]);
  });
}

test('container mode blocks old ready installation but permits explicit release check',async({page})=>{
  const data=fixture();data.install_enabled=false;data.install_mode='container';data.state='ready';data.operation!.state='ready';
  const posts=await mount(page,data);
  for(const name of ['下载并校验','安装升级','回滚'])await expect(page.getByRole('button',{name,exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'检查更新',exact:true}).click();
  await expect.poll(()=>posts).toEqual(['/api/v1/admin/updates/check']);
});

test('version notice fits tablet and compact desktop widths',async({page})=>{
  const data=fixture();data.current.version='1.4.2';data.latest!.latest_version='v1.4.3';data.install_enabled=false;data.install_mode='container';
  await mount(page,data);
  for(const width of [768,820,1024]){
    await page.setViewportSize({width,height:900});
    const header=page.locator('.admin-topbar');
    const version=header.getByRole('link',{name:/当前版本 v1.4.2，发现新版本 v1.4.3/});
    await expect(version).toBeVisible();
    await expect(header.getByRole('link',{name:'访问用户端'})).toBeVisible();
    const size=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth,header:document.querySelector('.admin-topbar')!.getBoundingClientRect().right}));
    expect(size.scroll,`page overflow at ${width}px`).toBeLessThanOrEqual(size.width);
    expect(size.header,`header overflow at ${width}px`).toBeLessThanOrEqual(size.width);
  }
});

test('audit: failed release checks remain visible in narrow headers',async({page})=>{
  const data=fixture();data.install_mode='container';data.install_enabled=false;
  data.state='failed';data.operation={id:'check-error',kind:'check',state:'failed',error_code:'timeout',message:'GitHub timed out'};
  data.latest!.compatible=false;
  await mount(page,data);
  await expect(page.getByText('通过容器更新',{exact:true})).toBeVisible();
  for(const width of [390,768,1280]){
    await page.setViewportSize({width,height:844});
    await expect(page.locator('.admin-topbar').getByText('版本检查失败',{exact:true})).toBeVisible();
    const size=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));
    expect(size.scroll,`error notice overflow at ${width}px`).toBeLessThanOrEqual(size.width);
  }
});

test('audit: cached release status is not presented as a fresh check after disconnect',async({page})=>{
  const data=fixture();data.latest!.available=false;data.latest!.latest_version=data.current.version;
  data.state='idle';data.operation={kind:'check',state:'idle'};data.checked_at='2026-10-29T01:00:00Z';
  await mount(page,data);
  await page.route('**/api/v1/admin/updates/status',route=>route.fulfill({status:503,json:{error:{code:'unavailable',message:'fixture disconnected'}}}));
  await page.getByRole('button',{name:'刷新',exact:true}).click();
  await expect(page.getByText('上次检查未发现新版本',{exact:true})).toBeVisible();
  await expect(page.getByText('当前已是最新版本',{exact:true})).toHaveCount(0);
  await expect(page.locator('.admin-topbar').getByText('状态读取失败',{exact:true})).toBeVisible();
});
