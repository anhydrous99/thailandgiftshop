import { defineConfig, devices } from '@playwright/test';
import { randomBytes } from 'node:crypto';
import { testServerPort, testServerURL } from './test-server';

const testSecret = (name: string) => `${name}-${randomBytes(32).toString('base64url')}`;

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  use: {
    baseURL: testServerURL,
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
      HOST: '127.0.0.1',
      PORT: testServerPort,
      APP_ENV: 'test',
      CATALOG_DEMO_STORE: '1',
      PAYMENTS_PROVIDER: 'fake',
      ADDRESS_VALIDATOR_MODE: 'fake',
      // Inline/name overrides must not bypass the local test credentials.
      ADMIN_CREDENTIALS_SECRET_JSON: '',
      ADMIN_CREDENTIALS_SECRET_NAME: '',
      ADMIN_ORIGIN_HEADER_SECRET: '',
      ADMIN_ORIGIN_HEADER_PREVIOUS_SECRET: '',
      STRIPE_CREDENTIALS_SECRET_JSON: '',
      STRIPE_CREDENTIALS_SECRET_NAME: '',
      CATALOG_TABLE_NAME: '',
      COMMERCE_TABLE_NAME: '',
      STATIC_DIR: 'web/static',
      IMAGE_DIR: 'web/product-images',
      AWS_EC2_METADATA_DISABLED: 'true',
      EMAIL_SENDER_MODE: 'fake',
      CART_COOKIE_SECRET: testSecret('playwright-cart-cookie-secret'),
      CUSTOMER_SESSION_SECRET: testSecret('playwright-customer-session-secret'),
      PUBLIC_BASE_URL: testServerURL,
      ADMIN_PASSWORD_HASH: '$2a$04$RweyV8hL8/jLlHcngOnoDeMY96aEfS2xL7EI1hQG8CpcIxvzR05Cy',
      ADMIN_SESSION_SECRET: testSecret('playwright-admin-session-secret'),
    },
    url: `${testServerURL}/`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
