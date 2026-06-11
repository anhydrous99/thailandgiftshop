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
      EMAIL_SENDER_MODE: 'fake',
      CART_COOKIE_SECRET: 'playwright-cart-cookie-secret',
      CUSTOMER_SESSION_SECRET: 'playwright-customer-session-secret-with-enough-entropy',
      PUBLIC_BASE_URL: 'http://127.0.0.1:8080',
      ADMIN_PASSWORD_HASH: '$2a$04$RweyV8hL8/jLlHcngOnoDeMY96aEfS2xL7EI1hQG8CpcIxvzR05Cy',
      ADMIN_SESSION_SECRET: 'playwright-admin-session-secret-with-enough-entropy',
    },
    url: 'http://127.0.0.1:8080/',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
