import { expect, test } from '@playwright/test';

test('home page assets, nav anchors, and product image route work end-to-end', async ({ page }) => {
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

  const logoResponse = await page.request.get('/static/logo.svg');
  expect(logoResponse.status()).toBe(200);
  expect(logoResponse.headers()['content-type']).toContain('image/svg+xml');

  const heroResponse = await page.request.get('/static/home-hero.png');
  expect(heroResponse.status()).toBe(200);
  expect(heroResponse.headers()['content-type']).toContain('image/png');

  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Thailand Gift Shop home' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Latest', exact: true })).toHaveAttribute('href', '#latest');
  await expect(page.getByRole('link', { name: 'Gift Sets', exact: true })).toHaveAttribute('href', '#gift-sets');
  await expect(page.getByRole('link', { name: 'Categories', exact: true })).toHaveAttribute('href', '#categories');
  await expect(page.getByRole('link', { name: 'Story', exact: true })).toHaveAttribute('href', '#story');
  await expect(page.getByRole('heading', { name: 'Thailand Gift Shop', level: 1 })).toBeVisible();
  await expect(page.getByText('Premium Thai gifting')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Latest products' })).toBeVisible();
  const emptyState = page.getByRole('heading', { name: 'No products are available yet.' });
  await expect(emptyState).toBeVisible();
  await expect(emptyState).toHaveCSS('font-weight', '700');
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('home page renders without JavaScript', async ({ page }) => {
    await page.goto('/');

    const cssResponse = await page.request.get('/static/assets/app.css');
    expect(cssResponse.status()).toBe(200);
    expect(cssResponse.headers()['content-type']).toContain('text/css');

    await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Thailand Gift Shop', level: 1 })).toBeVisible();
    await expect(page.getByText('Premium Thai gifting')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Latest products' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No products are available yet.' })).toBeVisible();
  });
});
