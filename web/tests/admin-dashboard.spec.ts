import { expect, test, type Page } from '@playwright/test';

const adminPassword = 'admin-product-password';

async function login(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

test('admin dashboard shows metrics navigation and deterministic empty filters', async ({ page }) => {
  const consoleErrors: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });

  await login(page);
  await expect(page.getByTestId('admin-metric-products')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-products-active')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-products-draft')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-products-archived')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-categories')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-low-stock')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-metric-upload-failures')).toHaveText(/\d+/);
  await expect(page.getByTestId('admin-nav-products')).toBeVisible();
  await expect(page.getByTestId('admin-nav-categories')).toBeVisible();
  await page.screenshot({ path: '../.omo/evidence/task-10-dashboard-polish.png', fullPage: true });

  await page.goto('/admin/products?status=archived&q=does-not-exist-999');
  await expect(page.getByTestId('admin-products-filter-form')).toBeVisible();
  await expect(page.getByTestId('admin-products-empty')).toContainText('No products match these filters.');
  await expect(page.getByTestId('admin-product-row')).toHaveCount(0);
  await page.screenshot({ path: '../.omo/evidence/task-10-empty-state.png', fullPage: true });

  await page.goto('/admin/categories?status=archived&q=does-not-exist-999');
  await expect(page.getByTestId('admin-categories-filter-form')).toBeVisible();
  await expect(page.getByTestId('admin-categories-empty')).toContainText('No categories match these filters.');
  await expect(page.getByTestId('admin-category-row')).toHaveCount(0);

  expect(consoleErrors).toEqual([]);
});
