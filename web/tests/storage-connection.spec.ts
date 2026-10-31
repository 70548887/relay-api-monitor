import { test, expect } from '@playwright/test';
import path from 'node:path';
import { mkdir } from 'node:fs/promises';
import type { BackupStorage } from '../src/backupApi';

for(const width of [1280,390])test(`storage connection feedback remains local and responsive at ${width}px`,async({page})=>{
  const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
  await page.setViewportSize({width,height:900});
  let tests=0,statusReads=0,finishTest!:()=>void;
  const pending=new Promise<void>(resolve=>{finishTest=resolve;});
  const storage={id:'storage',revision:1,endpoint:'https://mock-storage.invalid',region:'auto',bucket:'fixture-bucket',prefix:'backups',force_path_style:false,access_key_mask:'FIX…KEY',secret_configured:true};
  const schedule={enabled:false,cron:'0 2 * * *',timezone:'Asia/Shanghai',mode:'local',retain_days:14,retain_count:10,monthly_enabled:false,monthly_days:[1],monthly_month_end:false,monthly_retain_count:0,recipient:'',recipient_id:'',recipient_verified:false,reconciliation_required:false};
  await page.route('**/api/**',async route=>{
    const request=route.request(),pathname=new URL(request.url()).pathname;
    const json=(body:unknown,status=200)=>route.fulfill({status,json:body});
    if(pathname==='/api/v1/setup/status')return json({initialized:true});
    if(pathname==='/api/v1/auth/me')return json({user:{id:'mock-admin',username:'Admin'},csrf_token:'mock-csrf'});
    if(pathname==='/api/v1/admin/backups/status'){statusReads++;return json({schedule,storage,records:[],next_runs:[],native_restore_supported:false,deployment_kind:'unmanaged'});}
    if(pathname==='/api/v1/admin/backups/records')return json([]);
    if(pathname==='/api/v1/admin/backups/storage/test'){
      tests++;expect(request.postDataJSON().secret_access_key).toBe('');expect(request.headers()['x-csrf-token']).toBe('mock-csrf');await pending;
      return json({error:{code:'storage_access_denied',message:'测试夹具：对象写入权限不足'},result:{status:'failed',stage:'write',duration_ms:1532,http_status:403,request_id:'mock-diagnostic-123',test_scope:'saved',storage_id:storage.id,storage_revision:storage.revision,cleanup_error_code:'cleanup_denied',cleanup_message:'测试夹具：删除权限不足'}},502);
    }
    if(pathname==='/api/v1/admin/backups/storage'&&request.method()==='PUT')return json(storage);
    // All application API calls are fulfilled here. No API reaches a live server.
    return json(pathname.includes('overview')?{worker:{online:true},counts:{},recent_jobs:[]}:{});
  });
  await page.goto('/admin/backups');await page.getByRole('tab',{name:'存储与恢复密钥',exact:true}).click();
  const feedback=page.locator('.backup-storage-test');const button=feedback.locator('button').first();
  await button.click();await expect(feedback.getByText('连接测试进行中',{exact:true})).toBeVisible();await expect(button).toBeDisabled();
  await page.waitForTimeout(1200);await expect(feedback.locator('.backup-storage-test-metrics')).not.toContainText('客户端已等待：0.0 秒');expect(tests).toBe(1);
  await button.dispatchEvent('click');expect(tests).toBe(1);finishTest();
  await expect(feedback.getByText('连接测试失败',{exact:true})).toBeVisible();await expect(feedback.getByText('测试夹具：对象写入权限不足',{exact:true})).toBeVisible();await expect(feedback.getByText('上游 HTTP：403',{exact:true})).toBeVisible();await expect(feedback.getByText('mock-diagnostic-123',{exact:true})).toBeVisible();await expect.poll(()=>statusReads).toBeGreaterThan(1);
  await page.getByRole('button',{name:'保存存储配置',exact:true}).click();await expect(page.getByText('存储配置已保存，凭据只显示掩码',{exact:true})).toBeVisible();await expect(page.getByLabel('存储桶',{exact:true})).toHaveValue(storage.bucket);await expect(feedback.getByText('连接测试失败',{exact:true})).toBeVisible();
  await page.getByLabel('端点地址',{exact:true}).fill('');await button.click();await expect(feedback.getByText('配置需要完善，尚未发送请求',{exact:true})).toBeVisible();await expect(page.getByLabel('端点地址',{exact:true})).toBeFocused();expect(tests).toBe(1);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);expect(errors).toEqual([]);
  // Restore the failure card for a viewport-sized review image, max 1280px.
  await page.getByLabel('端点地址',{exact:true}).fill(storage.endpoint);await button.click();await expect(feedback.getByText('连接测试失败',{exact:true})).toBeVisible();await feedback.scrollIntoViewIfNeeded();
  const folder=path.resolve('../release/s3-repair-20261030');await mkdir(folder,{recursive:true});await page.screenshot({path:path.join(folder,`storage-feedback-${width}.png`),animations:'disabled'});
});

for(const width of [1280,390])test(`storage verification binds to the tested revision at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:900});const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
  let storage:BackupStorage={id:'storage-1',revision:1,endpoint:'https://mock-storage.invalid',region:'auto',bucket:'fixture-bucket',prefix:'backups',force_path_style:false,secret_configured:true,health:{status:'untested',message:''}};
  let tests=0,legacy=false;
  const schedule={enabled:false,cron:'0 2 * * *',timezone:'Asia/Shanghai',mode:'local',retain_days:14,retain_count:10,monthly_enabled:false,monthly_days:[1],monthly_month_end:false,monthly_retain_count:0,recipient:'',recipient_id:'',recipient_verified:false,reconciliation_required:false};
  await page.route('**/api/**',async route=>{
    const request=route.request(),pathname=new URL(request.url()).pathname,json=(body:unknown)=>route.fulfill({json:body});
    if(pathname==='/api/v1/setup/status')return json({initialized:true});
    if(pathname==='/api/v1/auth/me')return json({user:{id:'mock-admin',username:'Admin'},csrf_token:'mock-csrf'});
    if(pathname==='/api/v1/admin/backups/status')return json({schedule,storage,next_runs:[],records:[],native_restore_supported:false,deployment_kind:'unmanaged'});
    if(pathname==='/api/v1/admin/backups/records')return json([]);
    if(pathname==='/api/v1/admin/backups/storage/test'){
      tests++;if(legacy)return json({ok:true});const input=request.postDataJSON(),saved=input.prefix===storage.prefix&&!input.secret_access_key&&!input.access_key_id;
      if(saved)storage={...storage,health:{status:'ok',message:'夹具：当前修订验证通过'}};
      return json({ok:true,result:{status:'ok',stage:'completed',message:'夹具：读写删除完成',duration_ms:48,test_scope:saved?'saved':'draft',...(saved?{storage_id:storage.id,storage_revision:storage.revision}:{})}});
    }
    if(pathname==='/api/v1/admin/backups/storage'&&request.method()==='PUT'){
      const input=request.postDataJSON();if(input.prefix!==storage.prefix||input.secret_access_key||input.access_key_id)storage={...storage,prefix:input.prefix,id:`storage-${storage.revision+1}`,revision:storage.revision+1,health:{status:'untested',message:''}};return json(storage);
    }
    return json({});
  });
  await page.goto('/admin/backups');await page.getByRole('tab',{name:'存储与恢复密钥',exact:true}).click();
  const feedback=page.locator('.backup-storage-test'),button=feedback.locator('button').first(),save=page.getByRole('button',{name:'保存存储配置',exact:true});
  await button.click();await expect(feedback.getByText('连接测试通过',{exact:true})).toBeVisible();await expect(feedback.locator('.ant-alert')).toHaveClass(/ant-alert-success/);await expect(page.getByText('连接已验证',{exact:true})).toBeVisible();
  await save.click();await expect(save).toBeEnabled();await expect(feedback.getByText('连接测试通过',{exact:true})).toBeVisible();expect(storage.revision).toBe(1);
  await page.getByRole('tab',{name:'备份记录',exact:true}).click();await page.getByRole('tab',{name:'存储与恢复密钥',exact:true}).click();await expect(feedback.getByText('连接测试通过',{exact:true})).toBeVisible();
  await page.getByLabel('对象 Key 前缀',{exact:true}).fill('other-backups');await expect(feedback.getByText('此前配置测试通过，当前表单尚未验证',{exact:true})).toBeVisible();await expect(feedback.locator('.ant-alert')).toHaveClass(/ant-alert-warning/);
  await button.click();await expect(feedback.getByText('草稿连接测试通过',{exact:true})).toBeVisible();await save.click();await expect(save).toBeEnabled();await expect(feedback.getByText('草稿连接测试通过',{exact:true})).toBeVisible();await expect(page.getByText('已配置 · 尚未验证',{exact:true})).toBeVisible();expect(storage.revision).toBe(2);
  await button.click();await expect(feedback.getByText('连接测试通过',{exact:true})).toBeVisible();await expect(feedback.getByText('测试对象：已保存配置 · 修订 2',{exact:true})).toBeVisible();
  await page.getByLabel('Secret Access Key',{exact:true}).fill('fixture-ephemeral-secret');await expect(feedback.getByText('此前配置测试通过，当前表单尚未验证',{exact:true})).toBeVisible();await save.click();await expect(save).toBeEnabled();await expect(page.getByLabel('Secret Access Key',{exact:true})).toHaveValue('');await expect(page.getByText('已配置 · 尚未验证',{exact:true})).toBeVisible();await expect(feedback.getByText('此前配置测试通过，此结果不代表当前配置',{exact:true})).toBeVisible();await expect(feedback.locator('.ant-alert')).toHaveClass(/ant-alert-warning/);expect(storage.revision).toBe(3);
  await feedback.scrollIntoViewIfNeeded();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  const folder=path.resolve('../release/s3-repair-20261030');await mkdir(folder,{recursive:true});await page.screenshot({path:path.join(folder,`storage-binding-${width}.png`),animations:'disabled'});
  const beforeReload=tests;await page.reload();await page.getByRole('tab',{name:'存储与恢复密钥',exact:true}).click();await expect(feedback.locator('.ant-alert')).toHaveCount(0);expect(tests).toBe(beforeReload);await expect(page.getByText('已配置 · 尚未验证',{exact:true})).toBeVisible();
  legacy=true;await button.click();await expect(feedback.getByText('连接测试通过，配置归属待核对',{exact:true})).toBeVisible();await expect(feedback.locator('.ant-alert')).toHaveClass(/ant-alert-warning/);expect(errors).toEqual([]);
});
