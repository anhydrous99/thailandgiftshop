import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  use: {
    baseURL: 'http://127.0.0.1:8080',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    command: 'go run ./cmd/devserver',
    cwd: '..',
    env: {
      CATALOG_DEMO_STORE: '1',
      CART_COOKIE_SECRET: 'playwright-cart-cookie-secret',
    },
    url: 'http://127.0.0.1:8080/',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
