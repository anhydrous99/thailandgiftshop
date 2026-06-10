import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';

const adminPassword = 'admin-product-password';
const fixtureImage = path.join(process.cwd(), 'tests/fixtures/test-shirt.webp');
const excludedControlNames = ['Search', 'Filter', 'Sort'];

type ProductFlowData = {
  name: string;
  slug: string;
};

async function login(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

async function expectNoPaymentOrPIIControls(page: Page) {
  await expect(page.getByRole('button', { name: /place order|pay now/i })).toHaveCount(0);
  await expect(page.getByRole('link', { name: /place order|pay now/i })).toHaveCount(0);
  await expect(page.getByLabel(/card number/i)).toHaveCount(0);
  await expect(page.locator('input[name="email"], input[name="address"], input[name="card"], input[autocomplete^="cc-"]')).toHaveCount(0);
}

async function expectExcludedControlsAbsent(page: Page) {
  for (const name of excludedControlNames) {
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0);
  }
}

async function expectNoAdminLeakage(page: Page) {
  await expect(page.locator('[data-testid="admin-nav"], [data-testid="admin-product-row"], [data-testid="admin-category-row"]')).toHaveCount(0);
  await expect(page.locator('a[href^="/admin"], form[action^="/admin"]')).toHaveCount(0);
}

async function fillTestShirt(page: Page, data: ProductFlowData) {
  await page.getByTestId('product-name-input').fill(data.name);
  await page.getByTestId('product-slug-input').fill(data.slug);
  await page.getByTestId('product-description-input').fill('A soft cotton shirt created by the admin product workflow.');
  await page.getByTestId('product-price-input').fill('24.99');
  await page.getByTestId('product-status-select').selectOption('active');
  await page.getByTestId('product-category-checkbox').first().check();
  await page.getByTestId('product-image-input').setInputFiles(fixtureImage);
  await expect(page.getByTestId('product-image-status')).toContainText('/images/products/uploads/');

  const variantRows = page.getByTestId('variant-row');
  await variantRows.nth(0).getByTestId('variant-label-input').fill('S');
  await variantRows.nth(0).getByTestId('variant-stock-input').fill('1');
  await variantRows.nth(1).getByTestId('variant-label-input').fill('M');
  await variantRows.nth(1).getByTestId('variant-stock-input').fill('2');
  await variantRows.nth(2).getByTestId('variant-label-input').fill('XL');
  await variantRows.nth(2).getByTestId('variant-stock-input').fill('0');
}

test('admin product creates public variant cart flow archives and logs out cleanly', async ({ page }, testInfo) => {
  const uniqueSuffix = `${testInfo.workerIndex}-${Date.now()}`;
  const data: ProductFlowData = {
    name: `Task 12 Shirt ${uniqueSuffix}`,
    slug: `task-12-shirt-${uniqueSuffix}`,
  };

  await login(page);
  await page.goto('/admin/products/new');
  await fillTestShirt(page, data);
  await page.getByTestId('product-save-button').click();
  await expect(page).toHaveURL(/\/admin\/products\/prod_[^/]+\/edit\?saved=created$/);
  await expect(page.getByTestId('admin-flash')).toContainText('created');
  const editURL = page.url().replace(/\?saved=created$/, '');

  await page.goto('/admin/products');
  await expect(page.getByTestId('admin-product-row').filter({ hasText: data.name })).toBeVisible();

  await page.goto(`/products/${data.slug}`);
  await expect(page.getByRole('heading', { name: data.name, level: 1 })).toBeVisible();
  await expect(page.locator(`img[alt="${data.name}"]`)).toHaveAttribute('src', /\/images\/products\/uploads\//);
  await expect(page.getByTestId('variant-select')).toContainText('S');
  await expect(page.getByTestId('variant-select')).toContainText('M');
  await expect(page.getByTestId('variant-select')).toContainText('XL - out of stock');
  await expect(page.getByTestId('variant-select').locator('option', { hasText: 'XL - out of stock' })).toHaveAttribute('disabled', '');
  await expectExcludedControlsAbsent(page);
  await expectNoAdminLeakage(page);

  await page.getByTestId('variant-select').selectOption({ label: 'M' });
  await page.getByLabel('Quantity').fill('2');
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
  const cartLine = page.getByTestId('cart-line-item').filter({ hasText: data.name });
  await expect(cartLine).toBeVisible();
  await expect(cartLine.getByText('Size M')).toBeVisible();
  await expect(cartLine.getByLabel('Quantity')).toHaveValue('2');
  await expect(cartLine.getByLabel('Quantity')).toHaveAttribute('max', '2');
  await expect(page.getByRole('link', { name: 'Cart (2)', exact: true }).first()).toBeVisible();

  await cartLine.getByLabel('Quantity').fill('3');
  await cartLine.getByRole('button', { name: 'Update' }).click();
  await expect(page).toHaveURL(/\/cart$/);
  const cappedCartLine = page.getByTestId('cart-line-item').filter({ hasText: data.name });
  await expect(cappedCartLine.getByLabel('Quantity')).toHaveAttribute('max', '2');
  await expect(cappedCartLine.getByLabel('Quantity')).toHaveValue('3');
  expect(await cappedCartLine.getByLabel('Quantity').evaluate((input: HTMLInputElement) => input.validity.rangeOverflow)).toBe(true);

  const variantID = await cappedCartLine.locator('input[name="variant_id"]').first().inputValue();
  const serverCapResponse = await page.request.post(`/cart/items/${data.slug}/quantity`, {
    form: { variant_id: variantID, quantity: '3' },
  });
  expect(serverCapResponse.status()).toBe(200);
  await page.reload();
  const serverCappedCartLine = page.getByTestId('cart-line-item').filter({ hasText: data.name });
  await expect(serverCappedCartLine.getByLabel('Quantity')).toHaveValue('2');
  await expect(serverCappedCartLine.getByLabel('Quantity')).toHaveAttribute('max', '2');
  await expect(page.getByRole('link', { name: 'Cart (2)', exact: true }).first()).toBeVisible();
  await expectExcludedControlsAbsent(page);
  await expectNoPaymentOrPIIControls(page);
  await expectNoAdminLeakage(page);
  await page.screenshot({ path: '../.omo/evidence/task-12-full-flow.png', fullPage: true });

  await page.goto('/admin/products/new');
  await fillTestShirt(page, data);
  await page.getByTestId('product-save-button').click();
  await expect(page.getByTestId('product-validation-summary')).toContainText('Slug is already used');

  await page.goto(editURL);
  await page.getByTestId('product-archive-button').click();
  await expect(page).toHaveURL(/\/admin\/products\?status=archived&saved=archived$/);
  await expect(page.getByTestId('admin-product-row').filter({ hasText: data.name })).toBeVisible();

  const publicResponse = await page.goto(`/products/${data.slug}`);
  expect(publicResponse?.status()).toBe(404);

  await page.goto('/admin');
  await page.getByTestId('admin-logout-button').click();
  await expect(page).toHaveURL(/\/admin\/login$/);
  await expect(page.getByTestId('admin-nav')).toHaveCount(0);

  await page.goto('/admin/products');
  await expect(page).toHaveURL(/\/admin\/login$/);
  await expect(page.getByTestId('admin-product-row').filter({ hasText: data.name })).toHaveCount(0);

  await page.goto('/admin');
  await expect(page).toHaveURL(/\/admin\/login$/);
  await expect(page.getByTestId('admin-nav')).toHaveCount(0);

  await page.goto('/cart');
  await expect(page.getByRole('heading', { name: 'Cart', level: 1 })).toBeVisible();
  await expectExcludedControlsAbsent(page);
  await expectNoPaymentOrPIIControls(page);
  await expectNoAdminLeakage(page);
  await page.screenshot({ path: '../.omo/evidence/task-12-logout-public-regression.png', fullPage: true });
});
