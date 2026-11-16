import { defineConfig, devices } from '@playwright/test'

/**
 * Playwright 配置文件
 * 用于租户模型定价页面浏览器测试
 */

export default defineConfig({
  testDir: './',
  
  /* 运行测试的最大时间 */
  timeout: 30 * 1000,
  
  /* 每个测试的expect超时时间 */
  expect: {
    timeout: 5000,
  },
  
  /* 失败时重试 */
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  
  /* 并行worker数量 */
  workers: process.env.CI ? 1 : undefined,
  
  /* 测试报告 */
  reporter: [
    ['html', { outputFolder: 'playwright-report' }],
    ['list'],
  ],
  
  /* 共享配置 */
  use: {
    /* 基础URL */
    baseURL: process.env.BASE_URL || 'http://localhost:8090',
    
    /* 失败时截图 */
    screenshot: 'only-on-failure',
    
    /* 失败时录制trace */
    trace: 'retain-on-failure',
    
    /* 视频录制 */
    video: 'retain-on-failure',
    
    /* 默认超时 */
    actionTimeout: 10 * 1000,
    navigationTimeout: 10 * 1000,
  },

  /* 配置多个测试项目 */
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },

    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },

    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
    },

    /* 移动设备测试（可选） */
    // {
    //   name: 'Mobile Chrome',
    //   use: { ...devices['Pixel 5'] },
    // },
    // {
    //   name: 'Mobile Safari',
    //   use: { ...devices['iPhone 12'] },
    // },
  ],

  /* 本地开发服务器配置（可选） */
  // webServer: {
  //   command: 'npm run dev',
  //   url: 'http://localhost:8090',
  //   reuseExistingServer: !process.env.CI,
  // },
})
