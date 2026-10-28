import { test, expect, type Page } from '@playwright/test';

const session = { user: { id: 'portal-admin', username: 'portal-admin' }, csrf_token: 'portal-test-csrf' };
const memberSession = { user: { id: 'portal-member', username: 'portal-member', disabled: false, created_at: '2026-10-26T00:00:00Z' }, csrf_token: 'portal-member-csrf' };
const overview = { worker: { online: true, heartbeat_at: '2026-10-26T00:00:00Z' }, counts: { queued: 0, running: 0, failed: 0 }, recent_jobs: [] };

async function mockPortal(page: Page, options: { authenticated?: boolean; initialized?: boolean; memberAuthenticated?: boolean } = {}) {
  const state = { authenticated: options.authenticated ?? false, initialized: options.initialized ?? true, memberAuthenticated: options.memberAuthenticated ?? false, requests: [] as string[], logins: 0, logouts: 0 };
  await page.route('**/api/v1/**', async route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    state.requests.push(path);
    if (path === '/api/v1/user/auth/me') return route.fulfill(state.memberAuthenticated
      ? { json: memberSession }
      : { status: 401, json: { error: { code: 'unauthorized', message: '请先登录' } } });
    if (path === '/api/v1/setup/status') return route.fulfill({ json: { initialized: state.initialized } });
    if (path === '/api/v1/setup') {
      state.initialized = true;
      return route.fulfill({ status: 201, json: {} });
    }
    if (path === '/api/v1/auth/login') {
      state.logins++;
      state.authenticated = true;
      return route.fulfill({ json: session });
    }
    if (path === '/api/v1/auth/logout') {
      state.logouts++;
      state.authenticated = false;
      return route.fulfill({ status: 204 });
    }
    if (path === '/api/v1/auth/me') return route.fulfill(state.authenticated
      ? { json: session }
      : { status: 401, json: { error: { code: 'unauthorized', message: '请先登录' } } });
    if (path.startsWith('/api/v1/admin/')) {
      if (!state.authenticated) return route.fulfill({ status: 401, json: { error: { code: 'unauthorized', message: '会话已过期' } } });
      if (path === '/api/v1/admin/overview') return route.fulfill({ json: overview });
      return route.fulfill({ json: [] });
    }
    if (path === '/api/v1/public/monitor') return route.fulfill({ json: {
      platforms: [], channels: [], models: [],
      summary: { platforms: 0, models: 0, normal: 0, slow: 0, failed: 0, stale: 0 }, updated_at: '2026-10-26T00:00:00Z',
    } });
    if (path === '/api/v1/public/templates') return route.fulfill({ json: [] });
    if (path === '/api/v1/public/intelligence') return route.fulfill({ json: { groups: [], history_scope: 'group', updated_at: '2026-10-26T00:00:00Z', worker_online: true } });
    return route.fulfill({ status: 404, json: { error: { code: 'unexpected_test_request', message: path } } });
  });
  return state;
}

async function signIn(page: Page) {
  await page.getByLabel('用户名', { exact: true }).fill('portal-admin');
  await page.getByLabel('密码', { exact: true }).fill('Portal_Test_Only_2026');
  await page.getByRole('button', { name: '登录工作台', exact: true }).click();
}

function nextDestination(page: Page) {
  return new URL(page.url()).searchParams.get('next');
}

test('public monitor and gallery have member entry links and never initialize an administrator session', async ({ page }) => {
  const state = await mockPortal(page, { authenticated: true });
  for (const [path, heading] of [
    ['/', '中转 API 监控'],
    ['/intelligence', '同一道题，看看模型如何作答。'],
  ]) {
    await page.goto(path);
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
    const navigation = page.getByRole('navigation', { name: '主导航', exact: true });
    await expect(navigation.getByRole('link')).toHaveCount(3);
    await expect(page.getByRole('link', { name: '登录', exact: true })).toHaveAttribute('href', '/login');
    await expect(page.getByRole('link', { name: '注册', exact: true })).toHaveAttribute('href', '/register');
    await expect(page.locator('a[href^="/admin"]')).toHaveCount(0);
    await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: '退出登录', exact: true })).toHaveCount(0);
  }
  expect(state.requests.some(path => /\/api\/v1\/(auth|setup|admin)(\/|$)/.test(path))).toBe(false);
  expect(state.requests).toContain('/api/v1/public/monitor');
  expect(state.requests).toContain('/api/v1/public/intelligence');
});

test('online testing redirects an anonymous visitor to member login', async ({ page }) => {
  const state = await mockPortal(page);
  await page.goto('/test');
  await expect(page).toHaveURL(/\/login\?/);
  expect(nextDestination(page)).toBe('/test');
  await expect(page.getByLabel('用户名', { exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '主导航', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
  expect(state.requests.some(path => /\/api\/v1\/(auth|setup|admin)(\/|$)/.test(path))).toBe(false);
});

test('an authenticated member can open online testing without initializing an administrator session', async ({ page }) => {
  const state = await mockPortal(page, { memberAuthenticated: true });
  await page.goto('/test');
  await expect(page.getByRole('heading', { name: '带上你的 Key，即刻试一试。', exact: true })).toBeVisible();
  await expect(page.getByLabel('API Key', { exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: '我的账号', exact: true })).toHaveAttribute('href', '/account');
  await expect(page.locator('a[href^="/admin"]')).toHaveCount(0);
  expect(state.requests.some(path => /\/api\/v1\/(auth|setup|admin)(\/|$)/.test(path))).toBe(false);
});

test('an ordinary member session shows the account link and cannot unlock the administrator workspace', async ({ page }) => {
  const state = await mockPortal(page, { memberAuthenticated: true });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: '中转 API 监控', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: '我的账号', exact: true })).toHaveAttribute('href', '/account');
  await expect(page.locator('a[href^="/admin"]')).toHaveCount(0);
  expect(state.requests).toContain('/api/v1/user/auth/me');
  expect(state.requests).not.toContain('/api/v1/auth/me');
  await page.goto('/admin/models');
  await expect(page).toHaveURL(/\/admin\/login\?/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(nextDestination(page)).toBe('/admin/models');
  expect(state.requests.some(path => path.startsWith('/api/v1/admin/'))).toBe(false);
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
});

test('administrator login has its own page and does not expose protected workspace data', async ({ page }) => {
  const state = await mockPortal(page);
  await page.goto('/admin/login');
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  await expect(page.getByLabel('用户名', { exact: true })).toBeVisible();
  await expect(page.getByLabel('密码', { exact: true })).toBeVisible();
  await expect(page.locator('.site-header, .site-footer')).toHaveCount(0);
  await expect(page.getByRole('navigation', { name: '主导航', exact: true })).toHaveCount(0);
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
  expect(state.requests.some(path => path.startsWith('/api/v1/admin/'))).toBe(false);
});

test('the administrator root redirects an anonymous visitor to the dedicated login', async ({ page }) => {
  const state = await mockPortal(page);
  await page.goto('/admin');
  await expect(page).toHaveURL(/\/admin\/login\?/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(nextDestination(page)).toBe('/admin');
  expect(state.requests.some(path => path.startsWith('/api/v1/admin/'))).toBe(false);
});

test('first administrator setup uses a separate route and then opens the protected workspace', async ({ page }) => {
  const state = await mockPortal(page, { initialized: false });
  await page.goto('/admin');
  await expect(page).toHaveURL(/\/admin\/setup(?:\?|$)/);
  await expect(page.getByRole('heading', { name: '创建首个管理员', exact: true })).toBeVisible();
  await expect(page.locator('.site-header, .site-footer')).toHaveCount(0);
  await page.getByLabel('用户名', { exact: true }).fill('portal-admin');
  await page.getByLabel('密码', { exact: true }).fill('Portal_Test_Only_2026');
  await page.getByLabel('确认密码', { exact: true }).fill('Portal_Test_Only_2026');
  await page.getByRole('button', { name: '创建管理员并登录', exact: true }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  expect(state.initialized).toBe(true);
  expect(state.logins).toBe(1);
});

test('protected deep links retain their local destination through login', async ({ page }) => {
  const state = await mockPortal(page);
  const destination = '/admin/models?view=active#models';
  await page.goto(destination);
  await expect(page).toHaveURL(/\/admin\/login\?/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(nextDestination(page)).toBe(destination);
  expect(state.requests.some(path => path.startsWith('/api/v1/admin/'))).toBe(false);
  await signIn(page);
  await expect(page).toHaveURL(url => `${url.pathname}${url.search}${url.hash}` === destination);
  await expect(page.getByRole('heading', { name: '把每个模型，安排明白。', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '主导航', exact: true })).toHaveCount(0);
  await expect(page.locator('.site-header, .site-footer')).toHaveCount(0);
});

for (const destination of ['https://example.test/admin/models', '//example.test/admin/models', '/test', '/admin/login', '/admin/setup']) {
  test(`login rejects unsafe or recursive next destination ${destination}`, async ({ page }) => {
    await mockPortal(page);
    await page.goto(`/admin/login?next=${encodeURIComponent(destination)}`);
    await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
    await signIn(page);
    await expect(page).toHaveURL(/\/admin$/);
    await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  });
}

test('an expired workspace session redirects to login and keeps the current destination', async ({ page }) => {
  const state = await mockPortal(page, { authenticated: true });
  const destination = '/admin?view=recent#queue';
  await page.goto(destination);
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  await expect(page.locator('.worker-state')).toHaveText('在线');
  state.authenticated = false;
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/login\?/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(nextDestination(page)).toBe(destination);
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
  await signIn(page);
  await expect(page).toHaveURL(url => `${url.pathname}${url.search}${url.hash}` === destination);
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
});

test('workspace navigation, browser history and logout remain independent of public navigation', async ({ page }) => {
  const state = await mockPortal(page, { authenticated: true });
  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  const navigation = page.getByRole('navigation', { name: '管理导航', exact: true });
  await navigation.getByRole('link', { name: '模型配置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '把每个模型，安排明白。', exact: true })).toBeVisible();
  await navigation.getByRole('link', { name: '平台与分组', exact: true }).click();
  await expect(page.getByRole('heading', { name: '平台与分组', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/models$/);
  await expect(page.getByRole('heading', { name: '把每个模型，安排明白。', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/login$/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(state.logouts).toBe(1);
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/login\?/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toHaveCount(0);
});

test('the administrator workspace fits a 390 pixel screen with navigation and logout visible', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockPortal(page, { authenticated: true });
  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: '管理导航', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '退出登录', exact: true })).toBeVisible();
  const size = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  expect(size.scroll).toBeLessThanOrEqual(size.width);
});
