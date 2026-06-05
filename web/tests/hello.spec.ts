import { expect, test } from '@playwright/test';

test('Tailwind and HTMX hello world work end-to-end', async ({ page }) => {
  const htmxResponsePromise = page.waitForResponse((response) =>
    response.url().endsWith('/static/vendor/htmx.min.js') && response.status() === 200,
  );

  await page.goto('/');

  const htmxResponse = await htmxResponsePromise;
  await expect(htmxResponse.headers()['content-type']).toContain('javascript');

  const cssResponse = await page.request.get('/static/assets/app.css');
  expect(cssResponse.status()).toBe(200);
  expect(cssResponse.headers()['content-type']).toContain('text/css');

  const card = page.locator('#hello-status');
  await expect(card).toContainText('Hello from thailandgiftshop.com');
  await expect(card).toHaveCSS('border-radius', '16px');
  await expect(card).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)');

  const pageURL = page.url();
  await page.getByRole('link', { name: 'Refresh greeting with HTMX' }).click();

  await expect(card).toContainText('HTMX refreshed this greeting');
  expect(page.url()).toBe(pageURL);
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('fallback link refreshes greeting through full-page navigation', async ({ page }) => {
    await page.goto('/');

    const cssResponse = await page.request.get('/static/assets/app.css');
    expect(cssResponse.status()).toBe(200);
    expect(cssResponse.headers()['content-type']).toContain('text/css');

    await page.getByRole('link', { name: 'Refresh greeting with HTMX' }).click();

    await expect(page).toHaveURL('/?hello=fallback');
    await expect(page.locator('#hello-status')).toContainText('Fallback greeting refreshed');
  });
});
