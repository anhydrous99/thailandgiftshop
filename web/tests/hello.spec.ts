import { expect, test } from '@playwright/test';

test('catalog shell assets and product image route work end-to-end', async ({ page }) => {
  const htmxResponsePromise = page.waitForResponse((response) =>
    response.url().endsWith('/static/vendor/htmx.min.js') && response.status() === 200,
  );

  await page.goto('/');

  const htmxResponse = await htmxResponsePromise;
  await expect(htmxResponse.headers()['content-type']).toContain('javascript');

  const cssResponse = await page.request.get('/static/assets/app.css');
  expect(cssResponse.status()).toBe(200);
  expect(cssResponse.headers()['content-type']).toContain('text/css');

  const imageResponse = await page.request.get('/images/placeholder-product.jpg');
  expect(imageResponse.status()).toBe(200);
  expect(imageResponse.headers()['content-type']).toContain('image/jpeg');

  await expect(page.getByRole('heading', { name: /Thai gifts, pantry favorites/i })).toBeVisible();
  const emptyState = page.getByRole('heading', { name: 'No products are available yet.' });
  await expect(emptyState).toBeVisible();
  await expect(emptyState).toHaveCSS('font-weight', '600');
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('catalog shell renders without JavaScript', async ({ page }) => {
    await page.goto('/');

    const cssResponse = await page.request.get('/static/assets/app.css');
    expect(cssResponse.status()).toBe(200);
    expect(cssResponse.headers()['content-type']).toContain('text/css');

    await expect(page.getByRole('heading', { name: /Thai gifts, pantry favorites/i })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No products are available yet.' })).toBeVisible();
  });
});
