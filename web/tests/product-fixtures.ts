import { expect, type Browser, type TestInfo } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { testServerURL } from '../test-server';

// Create stock owned by one test, through the real admin flow. A separate
// context keeps admin cookies/CSRF independent of the shopper (including
// specs with JavaScript disabled). Products need no uploads or external APIs.
export async function createTestProduct(
  browser: Browser,
  testInfo: TestInfo,
  options: { stock: number; variantLabel?: string; price?: string },
): Promise<{ slug: string; name: string }> {
  const slug = `test-product-${testInfo.workerIndex}-${randomUUID()}`;
  const name = `Test Product ${slug}`;
  const context = await browser.newContext({ baseURL: testServerURL });
  try {
    const page = await context.newPage();
    await page.goto('/admin/login');
    await page.getByLabel('Password').fill('admin-product-password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page).toHaveURL(/\/admin$/);
    await page.goto('/admin/products/new');
    await page.getByTestId('product-name-input').fill(name);
    await page.getByTestId('product-slug-input').fill(slug);
    await page.getByTestId('product-description-input').fill('Local browser-test stock fixture.');
    await page.getByTestId('product-price-input').fill(options.price ?? '10.00');
    await page.getByTestId('product-status-select').selectOption('active');
    if (options.variantLabel) {
      const variant = page.getByTestId('variant-row').first();
      await variant.getByTestId('variant-label-input').fill(options.variantLabel);
      await variant.getByTestId('variant-stock-input').fill(String(options.stock));
    } else {
      await page.getByLabel('Fallback stock').fill(String(options.stock));
    }
    await page.getByTestId('product-save-button').click();
    await expect(page).toHaveURL(/\/admin\/products\/prod_[^/]+\/edit\?saved=created$/);
    return { slug, name };
  } finally {
    await context.close();
  }
}
