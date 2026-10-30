import { test, expect, type Page } from '@playwright/test';
import fs from 'node:fs';

const fixturePath = process.env.PLAYWRIGHT_CLOUD_FIXTURE;
const fixture = fixturePath ? JSON.parse(fs.readFileSync(fixturePath, 'utf8')) as { url:string; s3_url:string; identity:string; wrong_identity:string; username:string; password:string } : undefined;
const base = new URL(process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:42783');
const isolated = !!fixture && process.env.PLAYWRIGHT_CLOUD_TESTS === '1' && ['127.0.0.1','localhost'].includes(base.hostname) && !['42783','42784','55835','58374','8080','5173',''].includes(base.port) && fixture.url === base.origin;

async function cloudPassword(page:Page) {
  const dialog=page.getByRole('dialog',{name:'确认导入云端备份',exact:true});
  await dialog.getByLabel('管理员密码',{exact:true}).fill(fixture!.password);
  await dialog.getByRole('button',{name:'验证并导入',exact:true}).click();
}
async function managementPassword(page:Page) {
  const dialog=page.getByRole('dialog').filter({has:page.getByLabel('管理员密码',{exact:true})});
  await dialog.getByLabel('管理员密码',{exact:true}).fill(fixture!.password);
  await dialog.getByRole('button',{name:'验证并继续',exact:true}).click();
  await expect(dialog).toHaveCount(0);
}

test('isolated cloud scan, private import and source-preserving deletion',async({page})=>{
  test.skip(!isolated,'Explicit fixture and isolated non-production port required.');test.setTimeout(120000);page.setDefaultTimeout(15000);
  const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
  const initialized=(await(await page.request.get('/api/v1/setup/status')).json()).initialized as boolean;
  await page.goto('/admin/backups');await page.getByLabel('用户名',{exact:true}).fill(fixture!.username);await page.getByLabel('密码',{exact:true}).fill(fixture!.password);if(!initialized)await page.getByLabel('确认密码',{exact:true}).fill(fixture!.password);
  const auth=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(initialized?'/api/v1/auth/login':'/api/v1/setup'));
  await page.getByRole('button',{name:initialized?'登录工作台':'创建管理员并登录',exact:true}).click();expect((await auth).ok()).toBe(true);await expect(page.getByRole('link',{name:/备份与恢复$/})).toBeVisible();await page.goto('/admin/backups');
  const member=await(await page.request.get('/api/v1/auth/me')).json();const headers={'X-CSRF-Token':member.csrf_token};
  const previous=await(await page.request.get('/api/v1/admin/backups/records')).json();
  if(previous.length){expect((await page.request.post('/api/v1/admin/backups/reauth',{headers,data:{password:fixture!.password}})).ok()).toBe(true);for(const record of previous){expect(record.cloud_imported).toBe(true);expect((await page.request.patch(`/api/v1/admin/backups/records/${record.id}/protection`,{headers,data:{protected:false}})).ok()).toBe(true);expect((await page.request.delete(`/api/v1/admin/backups/records/${record.id}`,{headers})).ok()).toBe(true);}await page.reload();}
  await page.getByRole('button',{name:/云端备份目录$/}).click();const cloud=page.getByRole('dialog',{name:'云端备份目录',exact:true});
  await cloud.getByRole('button',{name:/扫描云端备份$/}).click();await expect(cloud.getByText('first-backup.zip.age',{exact:true})).toBeVisible();
  await cloud.getByRole('button',{name:'继续扫描',exact:true}).click();await expect(cloud.getByText('second-backup.zip.age',{exact:true})).toBeVisible();
  const first=cloud.getByRole('row').filter({hasText:'first-backup.zip.age'});await first.getByRole('button',{name:/选择导入$/}).click();
  await cloud.getByLabel('云端备份恢复私钥',{exact:true}).fill(fixture!.wrong_identity);await cloud.getByRole('button',{name:'验证密码并导入',exact:true}).click();await cloudPassword(page);await expect(cloud.getByText('云端备份或恢复私钥校验失败，未导入',{exact:true})).toBeVisible();await expect(cloud.getByLabel('云端备份恢复私钥',{exact:true})).toHaveValue('');
  expect((await(await page.request.get('/api/v1/admin/backups/records')).json()).length).toBe(0);
  await cloud.getByLabel('云端备份恢复私钥',{exact:true}).fill(fixture!.identity);await cloud.getByRole('button',{name:'验证密码并导入',exact:true}).click();let imports=0;page.on('request',r=>{if(r.method()==='POST'&&r.url().endsWith('/api/v1/admin/backups/cloud/import'))imports++;});await cloudPassword(page);
  await expect(cloud.getByText('已导入',{exact:true})).toBeVisible();await expect(cloud.getByLabel('云端备份恢复私钥',{exact:true})).toHaveCount(0);expect(imports).toBe(1);
  await cloud.getByRole('button',{name:/关\s*闭$/,exact:true}).click();const runs=await(await page.request.get('/api/v1/admin/backups/records')).json();expect(runs).toHaveLength(1);expect(runs[0].protected).toBe(true);expect(runs[0].cloud_imported).toBe(true);expect(runs[0].trigger).toBe('imported');
  const row=page.getByRole('row').filter({hasText:runs[0].file_name});await expect(row).toContainText('云端导入');await row.getByRole('button',{name:'解除保护',exact:true}).click();await managementPassword(page);
  await expect(row.getByRole('button',{name:/删\s*除$/,exact:true})).toBeEnabled();await row.getByRole('button',{name:/删\s*除$/,exact:true}).click();await page.getByRole('tooltip').getByRole('button',{name:/删\s*除$/,exact:true}).click();await managementPassword(page);await expect(row).toHaveCount(0);
  const stats=await(await page.request.get(fixture!.s3_url+'/__fixture/stats')).json();expect(stats.deletes).toBe(0);expect(stats.source_exists).toBe(true);
  await page.reload();expect(await page.locator('input').evaluateAll((inputs,key)=>inputs.some(input=>(input as HTMLInputElement).value.includes(key as string)),fixture!.identity)).toBe(false);
  expect(await page.evaluate(()=>JSON.stringify(localStorage)+JSON.stringify(sessionStorage))).not.toContain(fixture!.identity);
  await page.setViewportSize({width:390,height:844});await page.getByRole('button',{name:/云端备份目录$/}).click();const mobileCloud=page.getByRole('dialog',{name:'云端备份目录',exact:true});await mobileCloud.getByRole('button',{name:/扫描云端备份$/}).click();await expect(mobileCloud.getByText('first-backup.zip.age',{exact:true})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  await page.screenshot({path:test.info().outputPath('cloud-mobile.png'),fullPage:true,animations:'disabled'});await page.setViewportSize({width:1280,height:900});await page.screenshot({path:test.info().outputPath('cloud-desktop.png'),fullPage:true,animations:'disabled'});expect(errors).toEqual([]);
});
