import { test, expect, type Page, type TestInfo } from '@playwright/test';

import { createTestProduct } from './product-fixtures';

const password = 'playwright-demo-password';
const ordersPageSize = 20;

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

// Mirrors the sign-up + address steps of orders-admin.spec.ts so checkout has
// a saved shipping address.
async function signUpWithAddress(page: Page, email: string) {
  await page.goto('/account/sign-up');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 bytes)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);

  await page.goto('/account/addresses');
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill('Paging Tester');
  await form.getByLabel('Address line 1').fill('55 Silom Road');
  await form.getByLabel('City').fill('Houston');
  await form.getByLabel('State').selectOption('TX');
  await form.getByLabel('ZIP code').fill('77002');
  await form.getByRole('button', { name: 'Save address' }).click();
  await expect(page).toHaveURL(/\/account\/addresses$/);
}

async function placePaidOrder(page: Page, slug: string) {
  await page.goto(`/products/${slug}`);
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await page.getByRole('link', { name: 'Check out', exact: true }).click();
  await page.getByTestId('place-order-button').click();
  await page.getByTestId('fake-pay-button').click();
  await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?placed=1$/);
}

test.describe('order history paging without JavaScript', () => {
  test.use({ javaScriptEnabled: false, viewport: { width: 1280, height: 2400 } });

  test('pages past a full page of orders with signed cursors', async ({ page, browser }, testInfo) => {
    test.setTimeout(240_000); // 21 full checkout loops against the demo server
    const product = await createTestProduct(browser, testInfo, { stock: ordersPageSize + 1 });
    await signUpWithAddress(page, uniqueEmail(testInfo, 'paging'));
    for (let i = 0; i < ordersPageSize + 1; i++) {
      await placePaidOrder(page, product.slug);
    }

    await page.goto('/orders');
    await expect(page.getByTestId('order-row')).toHaveCount(ordersPageSize);
    const next = page.getByTestId('orders-next-page');
    await expect(next).toBeVisible();
    const href = await next.getAttribute('href');
    expect(href).toMatch(/^\/orders\?after=./);
    expect(href).not.toMatch(/after=[a-z0-9]{26}$/); // signed token, not the pre-deploy bare order ID

    await next.click();
    await expect(page.getByTestId('order-row')).toHaveCount(1);
    await expect(page.getByTestId('orders-next-page')).toHaveCount(0);

    // A tampered cursor falls back to the first page, never an error page.
    await page.goto('/orders?after=tampered-cursor-value');
    await expect(page.getByTestId('order-row')).toHaveCount(ordersPageSize);
  });
});
