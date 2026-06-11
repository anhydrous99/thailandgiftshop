import { test, expect, type Page, type TestInfo } from '@playwright/test';

// Stock budget (keep in sync with the orders-cursor-paging spec §8.5):
// paid orders permanently consume demo stock, and reuseExistingServer lets
// local runs accumulate consumption, so the 21 paid orders are spread across
// the three highest-stock no-variant demo products, 7 each:
//
// | Slug                         | Demo stock | Other specs' per-run draw                  | Here |
// |------------------------------|------------|--------------------------------------------|------|
// | ceramic-tuk-tuk-magnet-set   | 40         | 1 reserved (checkout.spec.ts cancel)       | 7    |
// | thai-tea-sampler             | 31         | 1 paid (checkout.spec.ts)                  | 7    |
// | elephant-pouch-set           | 27         | 1 paid (orders-admin.spec.ts); +1 transient| 7    |
// |                              |            | from the automated-refunds e2e in the same |      |
// |                              |            | file (returned on refund — net 0, but      |      |
// |                              |            | count it when reading live stock mid-suite)|      |
//
// Worst case per full-suite attempt is 9 units per product (counting the
// refund e2e's transient elephant-pouch-set unit before release); one in-run
// retry (16) still clears the tightest stock (27). guest-checkout.spec.ts
// draws nothing from these three products — it is pinned to
// coconut-curry-pantry-box (INTEGRATION.md §8); keep that allocation.

const password = 'playwright-demo-password';
const ordersPageSize = 20;

// 21 paid orders spread across the three highest-stock no-variant demo
// products (stock-budget table above). Which product backs which order is
// irrelevant to the paging assertions; only per-customer recency matters.
const orderSlugs = ['ceramic-tuk-tuk-magnet-set', 'thai-tea-sampler', 'elephant-pouch-set'];
const ordersPerProduct = 7; // 3 * 7 = ordersPageSize + 1

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

// Mirrors the sign-up + address steps of orders-admin.spec.ts so checkout has
// a saved shipping address.
async function signUpWithAddress(page: Page, email: string) {
  await page.goto('/account/sign-up');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 characters)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);

  await page.goto('/account/addresses');
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill('Paging Tester');
  await form.getByLabel('Address line 1').fill('55 Silom Road');
  await form.getByLabel('City').fill('Houston');
  await form.getByLabel('State').fill('TX');
  await form.getByLabel('ZIP code').fill('77002');
  await form.getByRole('button', { name: 'Save address' }).click();
  await expect(page).toHaveURL(/\/account\/addresses$/);
}

// Fail fast with a pointed message when a reused devserver has burned the
// stock budget; without this the order loop dies mid-checkout on a
// confusing out-of-stock error. The quantity input's max attribute equals
// live available stock for every demo product (all demo stocks < 99).
async function assertStockBudget(page: Page, slug: string, needed: number) {
  await page.goto(`/products/${slug}`);
  const available = Number(await page.getByLabel('Quantity').getAttribute('max'));
  expect(
    available,
    `${slug} has ${available} unit(s) left but this spec needs ${needed}. ` +
      'Restart the demo devserver: reuseExistingServer lets paid-order stock ' +
      'consumption accumulate across local runs.',
  ).toBeGreaterThanOrEqual(needed);
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

  test('pages past a full page of orders with signed cursors', async ({ page }, testInfo) => {
    test.setTimeout(240_000); // 21 full checkout loops against the demo server
    await signUpWithAddress(page, uniqueEmail(testInfo, 'paging'));
    for (const slug of orderSlugs) {
      await assertStockBudget(page, slug, ordersPerProduct);
    }
    for (let i = 0; i < ordersPageSize + 1; i++) {
      await placePaidOrder(page, orderSlugs[i % orderSlugs.length]);
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
