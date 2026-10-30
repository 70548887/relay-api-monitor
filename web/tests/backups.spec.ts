import { test, expect, type Page } from '@playwright/test';

const base = new URL(process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:42783');
const isolated = process.env.PLAYWRIGHT_BACKUP_TESTS === '1' && ['127.0.0.1', 'localhost'].includes(base.hostname) && !['42783', '42784', '8080', '5173', ''].includes(base.port);
const username = process.env.PLAYWRIGHT_BACKUP_ADMIN || 'backup-e2e-admin';
const password = process.env.PLAYWRIGHT_BACKUP_PASSWORD || 'Relay_Backup_E2E_2026';
test.describe.configure({mode:'serial'});

async function reauthenticate(page: Page) {
  const dialog = page.getByRole('dialog').filter({ has: page.getByLabel('管理员密码', { exact: true }) });
  await dialog.getByLabel('管理员密码', { exact: true }).fill(password);
  await dialog.getByRole('button', { name: '验证并继续', exact: true }).click();
  await expect(dialog).toHaveCount(0);
}

test('isolated backup keys, encrypted snapshot, authenticated download and restore preflight on desktop/mobile', async ({ page }) => {
  test.skip(!isolated, 'Explicit opt-in and an isolated local server/data directory are required.');
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const pageErrors: string[] = [];
  page.on('pageerror', error => pageErrors.push(error.message));
  const setup = await page.request.get('/api/v1/setup/status');
  const initialized = (await setup.json()).initialized as boolean;
  await page.goto('/admin/backups');
  await page.getByLabel('用户名', { exact: true }).fill(username);
  await page.getByLabel('密码', { exact: true }).fill(password);
  if (!initialized) await page.getByLabel('确认密码', { exact: true }).fill(password);
  const authentication = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith(initialized ? '/api/v1/auth/login' : '/api/v1/setup'));
  await page.getByRole('button', { name: initialized ? '登录工作台' : '创建管理员并登录', exact: true }).click();
  expect((await authentication).ok()).toBe(true);
  await expect(page.getByRole('link', { name: /备份与恢复$/ })).toBeVisible();
  await page.goto('/admin/backups');
  await expect(page.getByRole('heading', { name: '为每一份数据，保留恢复的余地。', exact: true })).toBeVisible();
  const before = await (await page.request.get('/api/v1/admin/backups/status')).json();
  if (!before.schedule.recipient_verified) await expect(page.getByRole('button', { name: /立即备份/ })).toBeDisabled();
  await page.getByRole('tab', { name: '存储与恢复密钥', exact: true }).click();
  await page.getByRole('button', { name: '生成并导出新密钥', exact: true }).click();
  await reauthenticate(page);
  const keyDialog = page.getByRole('dialog', { name: '导出恢复私钥', exact: true });
  await expect(keyDialog).toBeVisible();
  const recoveryIdentity = await keyDialog.getByLabel('恢复私钥', { exact: true }).inputValue();
  expect(recoveryIdentity.startsWith('AGE-SECRET-KEY-')).toBeTruthy();
  const privateDownload = page.waitForEvent('download');
  await keyDialog.getByRole('button', { name: /下载私钥$/ }).click();
  expect((await privateDownload).suggestedFilename()).toBe('relaylab-backup-recovery.key');
  await keyDialog.getByLabel('我已将私钥下载并独立保管', { exact: true }).check();
  await keyDialog.getByRole('button', { name: '已保管，启用此公钥', exact: true }).click();
  await expect(keyDialog).toHaveCount(0);
  await expect(page.getByRole('button', { name: /立即备份/ })).toBeEnabled();

  await page.getByRole('button', { name: /立即备份/ }).click();
  await page.getByRole('tab', { name: '备份记录', exact: true }).click();
  const row = page.getByRole('row').filter({ hasText: /.zip.age/ }).first();
  await expect(row).toBeVisible({ timeout: 20000 });
  await expect(row.getByRole('button', { name: /下\s*载/, exact: true })).toBeEnabled({ timeout: 30000 });
  await expect(row).toContainText('已完成');

  await page.reload();
  await expect(page.getByRole('row').filter({ hasText: /.zip.age/ }).first()).toBeVisible();
  expect(await page.locator('input').evaluateAll((inputs, identity) => inputs.some(input => (input as HTMLInputElement).value.includes(identity as string)), recoveryIdentity)).toBe(false);
  expect(await page.evaluate(() => ({ local: JSON.stringify(localStorage), session: JSON.stringify(sessionStorage) }))).not.toEqual(expect.objectContaining({ local: expect.stringContaining(recoveryIdentity) }));
  const download = page.waitForEvent('download');
  await row.getByRole('button', { name: /下\s*载/, exact: true }).click();
  await reauthenticate(page);
  expect((await download).suggestedFilename()).toMatch(/\.zip\.age$/);

  await row.getByRole('button', { name: '恢复预检', exact: true }).click();
  const restore = page.getByRole('dialog', { name: '恢复预检与执行', exact: true });
  await restore.getByLabel('恢复私钥', { exact: true }).fill(recoveryIdentity);
  await restore.getByRole('button', { name: '开始预检', exact: true }).click();
  await reauthenticate(page);
  await expect(restore.getByText('预检通过，当前数据尚未更改', { exact: true })).toBeVisible({ timeout: 20000 });
  await expect(restore.getByText('Schema 4', { exact: true })).toBeVisible();
  await expect(restore.locator('input')).toHaveCount(0);
  const current = await page.request.get('/api/v1/admin/backups/status');
  const status = await current.json();
  if (!status.native_restore_supported) await expect(restore.getByRole('button', { name: '验证密码并恢复', exact: true })).toBeDisabled();
  await restore.getByRole('button', { name: /close/i }).click();

  await page.getByRole('tab', { name: '定时与保留', exact: true }).click();
  const previewResponse = page.waitForResponse(response => response.url().endsWith('/api/v1/admin/backups/schedule/preview'));
  await page.getByRole('button', { name: '预览下三次执行', exact: true }).click();
  const preview = await previewResponse;
  expect(preview.status()).toBe(200);
  expect((await preview.json()).next_runs).toHaveLength(3);
  await expect(page.locator('.backup-next-runs > span')).toHaveCount(3);
  await expect(page.getByRole('switch', { name: '启用定时备份', exact: true })).not.toBeChecked();

  await page.setViewportSize({ width: 390, height: 844 });
  for (const tab of ['备份记录', '定时与保留', '存储与恢复密钥']) {
    await page.getByRole('tab', { name: tab, exact: true }).click();
    await expect(page.getByRole('tab', { name: tab, exact: true })).toHaveAttribute('aria-selected', 'true');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  }
  await page.screenshot({ path: test.info().outputPath('backups-mobile.png'), fullPage: true, animations: 'disabled' });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole('tab', { name: '备份记录', exact: true }).click();
  await expect(page.getByRole('heading',{name:'备份记录',exact:true})).toBeVisible();
  await page.locator('.ant-table-content').evaluateAll(elements=>elements.forEach(element=>{element.scrollLeft=0;}));
  await page.screenshot({ path: test.info().outputPath('backups-desktop.png'), fullPage: true, animations: 'disabled' });
  expect(pageErrors).toEqual([]);
});

test('native restore keeps a read-only progress page after administrator sessions are revoked',async({page})=>{
  test.skip(!isolated,'Explicit opt-in and isolated data are required.');test.setTimeout(180000);page.setDefaultTimeout(15000);
  const initialized=(await(await page.request.get('/api/v1/setup/status')).json()).initialized as boolean;
  await page.goto('/admin/backups');await page.getByLabel('用户名',{exact:true}).fill(username);await page.getByLabel('密码',{exact:true}).fill(password);if(!initialized)await page.getByLabel('确认密码',{exact:true}).fill(password);
  await page.getByRole('button',{name:initialized?'登录工作台':'创建管理员并登录',exact:true}).click();await expect(page.getByRole('link',{name:/备份与恢复$/})).toBeVisible();
  const status=await(await page.request.get('/api/v1/admin/backups/status')).json();test.skip(!status.native_restore_supported,'A real restore_v1 launcher is required; preflight-only deployment cannot prove native recovery.');
  const auth=await(await page.request.get('/api/v1/auth/me')).json();const headers={'X-CSRF-Token':auth.csrf_token,'Origin':base.origin};
  const keys=await(await page.request.post('/api/v1/admin/backups/keys/generate',{headers})).json();
  expect((await page.request.put('/api/v1/admin/backups/keys/recipient',{headers,data:{recipient:keys.recipient}})).ok()).toBe(true);expect((await page.request.post('/api/v1/admin/backups/keys/verify',{headers,data:{identity:keys.identity}})).ok()).toBe(true);
  const run=await(await page.request.post('/api/v1/admin/backups/records',{headers,data:{request_id:'browser-native-'+Date.now()}})).json();expect(run.id).toBeTruthy();
  await expect.poll(async()=>{const response=await page.request.get('/api/v1/admin/backups/records/'+run.id);return response.ok()?(await response.json()).state:'unavailable';},{timeout:30000}).toBe('succeeded');
  const marker='after-backup-marker-'+Date.now();expect((await page.request.post('/api/v1/admin/platforms',{headers,data:{name:marker,public:false}})).ok()).toBe(true);
  await page.goto('/admin/backups');const row=page.getByRole('row').filter({hasText:run.file_name});await expect(row).toBeVisible();await row.getByRole('button',{name:'恢复预检',exact:true}).click();
  const restore=page.getByRole('dialog',{name:'恢复预检与执行',exact:true});await restore.getByLabel('恢复私钥',{exact:true}).fill(keys.identity);await restore.getByRole('button',{name:'开始预检',exact:true}).click();await reauthenticate(page);await expect(restore.getByText('预检通过，当前数据尚未更改',{exact:true})).toBeVisible();
  let applyCalls=0;page.on('request',request=>{if(request.method()==='POST'&&request.url().endsWith('/api/v1/admin/backups/restore/apply'))applyCalls++;});
  await restore.getByRole('button',{name:'验证密码并恢复',exact:true}).click();await reauthenticate(page);await expect(page).toHaveURL(/\/restore-progress\/[a-f0-9]{32}$/);await expect(page.getByRole('heading',{name:'整站恢复进度',exact:true})).toBeVisible();
  await expect.poll(async()=>{try{return(await page.request.get('/api/v1/auth/me')).status();}catch{return 0;}},{timeout:90000}).toBe(401);
  await page.evaluate(()=>window.dispatchEvent(new Event('focus')));await expect(page.getByText('恢复已完成',{exact:true})).toBeVisible({timeout:90000});await expect(page).toHaveURL(/\/restore-progress\/[a-f0-9]{32}$/);expect(applyCalls).toBe(1);
  await page.getByRole('link',{name:'重新登录并核对备份',exact:true}).click();await page.getByLabel('用户名',{exact:true}).fill(username);await page.getByLabel('密码',{exact:true}).fill(password);await page.getByRole('button',{name:'登录工作台',exact:true}).click();await expect(page.getByRole('heading',{name:'为每一份数据，保留恢复的余地。',exact:true})).toBeVisible();
  const platforms=await(await page.request.get('/api/v1/admin/platforms')).json();expect(platforms.some((item:{name:string})=>item.name===marker)).toBe(false);expect((await(await page.request.get('/api/v1/admin/backups/status')).json()).schedule.reconciliation_required).toBe(true);
});
