import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';

const adminPassword = 'admin-product-password';
const fixtureImage = path.join(process.cwd(), 'tests/fixtures/test-shirt.webp');

type CategoryFlowData = {
  productName: string;
  productSlug: string;
  categoryName: string;
  categorySlug: string;
};

async function login(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

async function createTestShirt(page: Page, data: CategoryFlowData) {
  await page.goto('/admin/products/new');
  await page.getByTestId('product-name-input').fill(data.productName);
  await page.getByTestId('product-slug-input').fill(data.productSlug);
  await page.getByTestId('product-description-input').fill('A soft cotton shirt created for category membership testing.');
  await page.getByTestId('product-price-input').fill('24.99');
  await page.getByTestId('product-status-select').selectOption('active');
  await page.getByTestId('product-image-input').setInputFiles(fixtureImage);
  await expect(page.getByTestId('product-image-status')).toContainText('/images/products/uploads/');

  const variantRows = page.getByTestId('variant-row');
  await variantRows.nth(0).getByTestId('variant-label-input').fill('S');
  await variantRows.nth(0).getByTestId('variant-stock-input').fill('1');
  await variantRows.nth(1).getByTestId('variant-label-input').fill('M');
  await variantRows.nth(1).getByTestId('variant-stock-input').fill('2');
  await variantRows.nth(2).getByTestId('variant-label-input').fill('XL');
  await variantRows.nth(2).getByTestId('variant-stock-input').fill('0');

  await page.getByTestId('product-save-button').click();
  await expect(page).toHaveURL(/\/admin\/products\/prod_[^/]+\/edit\?saved=created$/);
  return page.url().replace(/\?saved=created$/, '');
}

async function createSummerShirts(page: Page, data: CategoryFlowData) {
  await page.goto('/admin/categories/new');
  await page.getByTestId('category-name-input').fill(data.categoryName);
  await page.getByTestId('category-slug-input').fill(data.categorySlug);
  await page.getByTestId('category-description-input').fill('Breathable shirts for summer market days.');
  await page.getByTestId('category-sort-order-input').fill('35');
  await page.getByTestId('category-status-select').selectOption('active');
  await page.getByTestId('category-save-button').click();
  await expect(page).toHaveURL(new RegExp(`/admin/categories/${data.categorySlug}/edit\\?saved=created$`));
  await expect(page.getByTestId('admin-flash')).toContainText('created');
}

test('admin category membership updates public category products and archive hides category only', async ({ page }, testInfo) => {
  const uniqueSuffix = `${testInfo.workerIndex}-${Date.now()}`;
  const data: CategoryFlowData = {
    productName: `Task 9 Shirt ${uniqueSuffix}`,
    productSlug: `task-9-shirt-${uniqueSuffix}`,
    categoryName: `Task 9 Summer Shirts ${uniqueSuffix}`,
    categorySlug: `task-9-summer-shirts-${uniqueSuffix}`,
  };

  await login(page);
  const productEditURL = await createTestShirt(page, data);
  await createSummerShirts(page, data);

  await page.goto(productEditURL);
  await page.locator('label', { hasText: data.categoryName }).getByTestId('product-category-checkbox').check();
  await page.getByTestId('product-save-button').click();
  await expect(page).toHaveURL(/\/admin\/products\/prod_[^/]+\/edit\?saved=updated$/);

  await page.goto(`/admin/categories/${data.categorySlug}/edit`);
  const membershipRow = page.locator('label', { hasText: data.productName });
  await expect(membershipRow.getByTestId('category-product-checkbox')).toBeChecked();
  await page.screenshot({ path: '../.omo/evidence/task-9-category-membership.png', fullPage: true });

  await page.goto(`/categories/${data.categorySlug}`);
  await expect(page.getByRole('heading', { name: data.categoryName, level: 1 })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(data.productName) })).toBeVisible();

  await page.goto(`/admin/categories/${data.categorySlug}/edit`);
  await page.getByTestId('category-archive-button').click();
  await expect(page).toHaveURL(/\/admin\/categories\?status=archived&saved=archived$/);
  await expect(page.getByTestId('admin-category-row').filter({ hasText: data.categoryName })).toBeVisible();

  const categoryResponse = await page.goto(`/categories/${data.categorySlug}`);
  expect(categoryResponse?.status()).toBe(404);
  const productResponse = await page.goto(`/products/${data.productSlug}`);
  expect(productResponse?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: data.productName, level: 1 })).toBeVisible();
  await page.screenshot({ path: '../.omo/evidence/task-9-category-archive.png', fullPage: true });
});
