import { expect, test, type Page } from '@playwright/test';

const defaults = {
  probe_concurrency: 4, artifact_concurrency: 2, channel_concurrency: 2,
  probe_interval_seconds: 300, artifact_interval_seconds: 1800,
  probe_timeout_seconds: 60, artifact_timeout_seconds: 600, discovery_timeout_seconds: 20,
  slow_threshold_ms: 10000, retention_days: 30, online_test_retention_days: 30,
  artifact_history_scope: 'group', visitor_concurrency: 8, visitor_ip_concurrency: 2,
  visitor_models_per_minute: 5, visitor_generations_per_minute: 10,
};

async function mockSettingsAPI(page: Page) {
  let settings = { ...defaults };
  let loggedIn = true;
  let failSave = false;
  const writes: { path: string; body: Record<string, unknown> }[] = [];
  const runtimeErrors: string[] = [];
  page.on('pageerror', error => runtimeErrors.push(error.message));
  await page.route('**/api/v1/**', async route => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace('/api/v1', '');
    const session = { user: { id: 'settings-test-admin', username: 'Administrator' }, csrf_token: 'settings-test-csrf' };
    const json = (body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (path === '/setup/status') return json({ initialized: true });
    if (path === '/auth/me') return loggedIn ? json(session) : json({ error: { code: 'unauthorized', message: '请重新登录' } }, 401);
    if (path === '/auth/login') { loggedIn = true; return json(session); }
    if (path === '/admin/updates/status') return json({ current: { version: '1.5.0' }, latest: { available: false } });
    if (path === '/admin/overview') return json({ worker: { online: true, heartbeat_at: '2026-10-29T10:00:00Z' }, counts: { queued: 0, running: 0, failed: 0 }, recent_jobs: [] });
    if (path === '/admin/intelligence-status') return json({ groups: [], worker_online: true, maintenance: false });
    if (path === '/admin/settings' && request.method() === 'PUT') {
      const body = request.postDataJSON();
      writes.push({ path, body });
      if (failSave) return json({ error: { code: 'save_failed', message: '保存暂不可用' } }, 503);
      settings = body;
      return json(settings);
    }
    if (path === '/admin/settings') return json(settings);
    if (path === '/auth/password') {
      writes.push({ path, body: request.postDataJSON() });
      loggedIn = false;
      return json({ ok: true });
    }
    return json([]);
  });
  return { writes, runtimeErrors, failSave: (value: boolean) => { failSave = value; }, expireSession: () => { loggedIn = false; } };
}

test('opens settings from navigation and supports direct reload, back, and expired-session return', async ({ page }) => {
  const fixture = await mockSettingsAPI(page);
  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible();
  await page.getByRole('link', { name: '系统设置', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/settings$/);
  await expect(page.getByRole('heading', { level: 1, name: '系统设置' })).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('健康检测并发', { exact: true })).toHaveValue('4');
  await page.goBack();
  await expect(page).toHaveURL(/\/admin$/);
  await page.getByRole('link', { name: '系统设置', exact: true }).click();
  fixture.expireSession();
  await page.reload();
  await expect(page).toHaveURL(/\/admin\/login\?next=%2Fadmin%2Fsettings$/);
  await page.getByLabel('用户名', { exact: true }).fill('Administrator');
  await page.getByLabel('密码', { exact: true }).fill('settings-test-password');
  await page.getByRole('button', { name: '登录工作台', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/settings$/);
  await expect(page.getByRole('heading', { level: 1, name: '系统设置' })).toBeVisible();
  expect(fixture.runtimeErrors).toEqual([]);
});

test('keeps drafts, reveals hidden validation errors, saves every category, and preserves failed edits', async ({ page }) => {
  const fixture = await mockSettingsAPI(page);
  await page.goto('/admin/settings');
  await page.getByLabel('健康检测并发', { exact: true }).fill('7');
  await page.getByRole('tab', { name: '请求策略', exact: true }).click();
  await page.getByLabel('检测超时', { exact: true }).fill('');
  await page.getByRole('tab', { name: '访客额度', exact: true }).click();
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await expect(page.getByRole('tab', { name: '请求策略', exact: true })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByLabel('检测超时', { exact: true })).toBeFocused();
  await page.getByLabel('检测超时', { exact: true }).fill('75');
  await page.getByRole('tab', { name: '数据与历史', exact: true }).click();
  await page.getByLabel('检测数据保留', { exact: true }).fill('45');
  await page.getByRole('radio', { name: /每个模型最近 8 次/ }).check();
  await page.getByRole('tab', { name: '任务调度', exact: true }).click();
  await expect(page.getByLabel('健康检测并发', { exact: true })).toHaveValue('7');
  await expect(page.getByRole('status')).toContainText('4 项更改');
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await expect(page.getByText('系统设置已保存', { exact: true })).toBeVisible();
  expect(fixture.writes[0]).toEqual({ path: '/admin/settings', body: { ...defaults, probe_concurrency: 7, probe_timeout_seconds: 75, retention_days: 45, artifact_history_scope: 'model' } });
  fixture.failSave(true);
  await page.getByLabel('健康检测并发', { exact: true }).fill('9');
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await expect(page.getByText('保存暂不可用', { exact: true })).toBeVisible();
  await expect(page.getByLabel('健康检测并发', { exact: true })).toHaveValue('9');
  await page.getByRole('button', { name: '撤销更改', exact: true }).click();
  await expect(page.getByLabel('健康检测并发', { exact: true })).toHaveValue('7');
  expect(fixture.runtimeErrors).toEqual([]);
});

test('changes a password separately and returns to the settings login', async ({ page }) => {
  const fixture = await mockSettingsAPI(page);
  await page.goto('/admin/settings');
  await page.getByLabel('健康检测并发', { exact: true }).fill('6');
  await page.getByRole('tab', { name: '账号安全', exact: true }).click();
  await expect(page.getByRole('button', { name: '保存设置', exact: true })).not.toBeVisible();
  await page.getByLabel('当前密码', { exact: true }).fill('settings-old-password');
  await page.getByLabel('新密码', { exact: true }).fill('settings-new-password');
  await page.getByLabel('确认新密码', { exact: true }).fill('settings-wrong-password');
  await page.getByRole('button', { name: '更新密码', exact: true }).click();
  await expect(page.getByText('两次密码不一致', { exact: true })).toBeVisible();
  expect(fixture.writes).toHaveLength(0);
  await page.getByLabel('确认新密码', { exact: true }).fill('settings-new-password');
  await page.getByRole('button', { name: '更新密码', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/login\?next=%2Fadmin%2Fsettings$/);
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible();
  expect(fixture.writes).toEqual([{ path: '/auth/password', body: { old_password: 'settings-old-password', new_password: 'settings-new-password' } }]);
  expect(fixture.runtimeErrors).toEqual([]);
});

test('fits desktop and narrow layouts and supports keyboard category navigation', async ({ page }, testInfo) => {
  const fixture = await mockSettingsAPI(page);
  await page.setViewportSize({ width: 1280, height: 960 });
  await page.goto('/admin/settings');
  await expect(page.getByLabel('健康检测并发', { exact: true })).toHaveValue('4');
  await expect(page.getByRole('button', { name: '保存设置', exact: true })).toBeEnabled();
  await page.screenshot({ path: testInfo.outputPath('settings-desktop.png'), fullPage: true });
  const firstTab = page.getByRole('tab', { name: '任务调度', exact: true });
  await firstTab.focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: '请求策略', exact: true })).toBeFocused();
  await page.keyboard.press('End');
  await expect(page.getByRole('tab', { name: '账号安全', exact: true })).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('Home');
  await expect(firstTab).toHaveAttribute('aria-selected', 'true');
  for (const width of [1920, 1024, 736, 375, 320]) {
    await page.setViewportSize({ width, height: 960 });
    for (const name of ['任务调度', '请求策略', '数据与历史', '访客额度', '账号安全']) {
      await page.getByRole('tab', { name, exact: true }).click();
      const dimensions = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
      expect(dimensions.scroll, `${width}px / ${name}: horizontal overflow`).toBeLessThanOrEqual(dimensions.client + 1);
      const controls = page.getByRole('tabpanel').locator('input:visible:not([type="radio"])');
      for (const input of await controls.all()) {
        const box = await input.boundingBox();
        expect(box!.width).toBeGreaterThan(40);
        expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      }
    }
    if (width === 375) {
      await firstTab.click();
      await page.screenshot({ path: testInfo.outputPath('settings-mobile.png') });
    }
  }
  expect(fixture.runtimeErrors).toEqual([]);
});

test('keeps keyboard-focused fields uncovered in mobile and short viewports', async ({ page }, testInfo) => {
  const fixture = await mockSettingsAPI(page);
  const fields = ['健康检测并发', '作品生成并发', '单分组并发', '默认检测间隔', '默认创作间隔'];
  for (const viewport of [{ width: 375, height: 667 }, { width: 375, height: 420 }, { width: 1280, height: 600 }]) {
    await page.setViewportSize(viewport);
    await page.goto('/admin/settings');
    const save = page.getByRole('button', { name: '保存设置', exact: true });
    await expect(save).toBeEnabled();
    await expect(page.locator('.settings-savebar')).toHaveCSS('position', 'static');
    await page.getByRole('tab', { name: '任务调度', exact: true }).focus();
    for (const label of fields) {
      await page.keyboard.press('Tab');
      const input = page.getByLabel(label, { exact: true });
      await expect(input).toBeFocused();
      const focusIsVisible = await input.evaluate(element => {
        const rect = element.getBoundingClientRect();
        const centerX = rect.left + rect.width / 2;
        const centerY = rect.top + rect.height / 2;
        return centerX >= 0 && centerX < innerWidth && centerY >= 0 && centerY < innerHeight && document.elementFromPoint(centerX, centerY) === element;
      });
      expect(focusIsVisible, `${viewport.width}×${viewport.height} / ${label}: focused input is covered`).toBe(true);
    }
    await save.scrollIntoViewIfNeeded();
    await expect(save).toBeInViewport();
    await save.click();
    await expect(page.getByText('系统设置已保存', { exact: true })).toBeVisible();
    if (viewport.width === 375 && viewport.height === 667) await page.screenshot({ path: testInfo.outputPath('settings-mobile-keyboard.png') });
  }
  expect(fixture.writes).toHaveLength(3);
  expect(fixture.runtimeErrors).toEqual([]);
});
