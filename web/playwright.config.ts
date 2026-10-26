import { defineConfig, devices } from '@playwright/test';
export default defineConfig({testDir:'./tests',fullyParallel:true,use:{baseURL:process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:42783',trace:'retain-on-failure'},projects:[{name:'chromium',use:{...devices['Desktop Chrome'],channel:process.env.PLAYWRIGHT_CHANNEL||undefined}}],reporter:'list'});
