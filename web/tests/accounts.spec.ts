import {test,expect} from '@playwright/test';
const origin=new URL(process.env.PLAYWRIGHT_BASE_URL||'http://127.0.0.1:42783');
const enabled=process.env.PLAYWRIGHT_ACCOUNT_TESTS==='1'&&['127.0.0.1','localhost'].includes(origin.hostname)&&!['42783','42784','8080','5173',''].includes(origin.port);

test('ordinary registration, private test access and administrator account control are isolated',async({browser})=>{
 test.skip(!enabled,'Requires an explicitly isolated server/database and account-test opt-in');
 const adminContext=await browser.newContext();const memberContext=await browser.newContext();
 const admin=await adminContext.newPage();const member=await memberContext.newPage();
 const base=origin.origin;const name='portal-member-'+Date.now().toString(36);const password='Ordinary_Account_2026';
 try{
  await member.goto(base+'/test');await expect(member).toHaveURL(/\/login\?next=%2Ftest$/);
  await member.getByRole('link',{name:'注册账号',exact:true}).click();
  await member.getByLabel('用户名',{exact:true}).fill(name);await member.getByLabel('密码',{exact:true}).fill(password);await member.getByLabel('确认密码',{exact:true}).fill(password);
  await member.getByRole('button',{name:'注册并登录',exact:true}).click();
  await expect(member).toHaveURL(base+'/test');await expect(member.getByLabel('API Key',{exact:true})).toBeVisible();
  expect((await member.request.get(base+'/api/v1/admin/users')).status()).toBe(401);
  expect((await member.request.post(base+'/api/v1/test/models',{data:{}})).status()).toBe(403);
  await member.goto(base+'/admin/settings');await expect(member).toHaveURL(/\/admin\/login\?next=%2Fadmin%2Fsettings$/);await expect(member.getByRole('navigation',{name:'管理导航'})).toHaveCount(0);
  await member.goto(base+'/account');await expect(member.getByRole('heading',{name:'我的账号',exact:true})).toBeVisible();await expect(member.getByRole('navigation',{name:'主导航'})).toBeVisible();

  await admin.goto(base+'/admin/login');await admin.getByLabel('用户名',{exact:true}).fill('portal-admin');await admin.getByLabel('密码',{exact:true}).fill('Isolated_Admin_2026');await admin.getByRole('button',{name:'登录工作台',exact:true}).click();
  await expect(admin.getByRole('navigation',{name:'管理导航'})).toBeVisible();await expect(admin.getByRole('navigation',{name:'主导航'})).toHaveCount(0);
  expect((await admin.request.post(base+'/api/v1/test/models',{data:{}})).status()).toBe(401);
  await admin.getByRole('link',{name:'用户管理',exact:true}).click();
  const row=admin.getByRole('row').filter({hasText:name});await expect(row).toBeVisible();
  await row.getByRole('button',{name:/停\s*用/,exact:true}).click();await admin.getByRole('button',{name:/确\s*定/,exact:true}).click();await expect(row).toContainText('已停用');
  expect((await member.request.get(base+'/api/v1/user/auth/me')).status()).toBe(401);
  await member.goto(base+'/test');await expect(member).toHaveURL(/\/login\?next=%2Ftest$/);
  await row.getByRole('button',{name:/启\s*用/,exact:true}).click();await admin.getByRole('button',{name:/确\s*定/,exact:true}).click();await expect(row).toContainText('正常');
  await row.getByRole('button',{name:'重置密码',exact:true}).click();await admin.getByRole('dialog').getByLabel('新密码',{exact:true}).fill('Reset_Ordinary_2026');await admin.getByRole('dialog').getByRole('button',{name:'重置密码',exact:true}).click();await expect(admin.getByRole('dialog')).toHaveCount(0);
  await member.getByLabel('用户名',{exact:true}).fill(name);await member.getByLabel('密码',{exact:true}).fill(password);await member.getByRole('button',{name:'登录用户端',exact:true}).click();await expect(member.getByRole('alert')).toBeVisible();
  await member.getByLabel('密码',{exact:true}).fill('Reset_Ordinary_2026');await member.getByRole('button',{name:'登录用户端',exact:true}).click();await expect(member).toHaveURL(base+'/test');
  await admin.getByRole('tab',{name:'管理员账号',exact:true}).click();const own=admin.getByRole('row').filter({hasText:'portal-admin'});await expect(own.getByRole('button',{name:/停\s*用/,exact:true})).toBeDisabled();await expect(own.getByRole('button',{name:'重置密码',exact:true})).toBeDisabled();
  await admin.getByRole('button',{name:'退出登录',exact:true}).click();await expect(admin).toHaveURL(base+'/admin/login');
  await member.goto(base+'/account');await expect(member.getByRole('heading',{name:'我的账号',exact:true})).toBeVisible();
 }finally{await memberContext.close();await adminContext.close()}
});
