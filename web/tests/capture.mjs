import { chromium } from '@playwright/test';
import { mkdir, stat } from 'node:fs/promises';
import path from 'node:path';
const output = path.resolve(import.meta.dirname, '../../release/screenshots');
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome' });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 });
  await page.goto(process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:42783', { waitUntil: 'networkidle' });
  await page.getByRole('heading', { name: '每一次请求，都值得被看见。' }).waitFor();
  await page.screenshot({ path: path.join(output, 'monitor.png'), fullPage: false });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: path.join(output, 'monitor-mobile.png'), fullPage: false });
  for (const name of ['monitor.png', 'monitor-mobile.png']) {
    const file = path.join(output, name);
    const details = await stat(file);
    if (details.size > 500 * 1024) throw new Error(`${name} exceeds 500 KB`);
    console.log(`${file}: ${details.size} bytes`);
  }
} finally {
  await browser.close();
}
