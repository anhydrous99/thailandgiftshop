import { expect, test, type Page } from '@playwright/test';

const excludedControlNames = ['Search', 'Filter', 'Sort'];

async function expectExcludedControlsAbsent(page: Page) {
  for (const name of excludedControlNames) {
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0);
  }
}

async function expectHomeBuildingBannerVisible(page: Page) {
  const banner = page.getByTestId('home-building-banner');

  await expect(banner).toBeVisible();
  await expect(banner).toBeInViewport();
  await expect(banner).toContainText('This page is being built.');
  await expect(banner).toContainText('We are still preparing the shop experience.');

  const bannerPrecedesHeroHeading = await banner.evaluate((element) => {
    const heroHeading = document.querySelector('h1');

    return (
      heroHeading !== null &&
      Boolean(element.compareDocumentPosition(heroHeading) & Node.DOCUMENT_POSITION_FOLLOWING)
    );
  });

  expect(bannerPrecedesHeroHeading).toBe(true);
}

async function expectSkipLinkBox(page: Page, maxWidth: number, maxHeight: number) {
  const box = await page.getByRole('link', { name: 'Skip to content' }).boundingBox();

  expect(box).not.toBeNull();
  expect(box?.width).toBeLessThanOrEqual(maxWidth);
  expect(box?.height).toBeLessThanOrEqual(maxHeight);
}

test('home page assets, nav anchors, cart link, and aisle category route work end-to-end', async ({ page }) => {
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

  const mainNavigation = page.getByRole('navigation', { name: 'Main navigation' });
  await expect(mainNavigation).toBeVisible();
  await expect(mainNavigation.getByRole('link', { name: 'Thailand Gift Shop home' })).toBeVisible();
  await expect(mainNavigation.getByRole('link', { name: 'Products', exact: true })).toHaveAttribute('href', '/products');
  await expect(mainNavigation.getByRole('link', { name: 'Aisles', exact: true })).toHaveAttribute('href', '#shop-aisles');
  await expect(mainNavigation.getByRole('link', { name: 'Categories', exact: true })).toHaveAttribute('href', '#categories');
  await expect(mainNavigation.getByRole('link', { name: 'Story', exact: true })).toHaveAttribute('href', '#story');
  await expect(mainNavigation.getByRole('link', { name: 'Cart', exact: true })).toHaveAttribute('href', '/cart');
  await expectHomeBuildingBannerVisible(page);
  await expect(page.getByRole('heading', { name: 'Thailand Gift Shop', level: 1 })).toBeVisible();
  await expect(page.getByText('Bangkok gift shop online')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Latest products' })).toBeVisible();
  await expect(page.getByRole('link', { name: /Elephant Cotton Pouches/ })).toBeVisible();
  const marketFindsAisle = page.getByTestId('home-aisle-card').filter({ hasText: 'Bangkok Market Finds' });
  await expect(marketFindsAisle).toBeVisible();
  await expectExcludedControlsAbsent(page);

  await marketFindsAisle.click();
  await expect(page).toHaveURL(/\/categories\/market-finds$/);
  await expect(page.getByRole('heading', { name: 'Bangkok Market Finds', level: 1 })).toBeVisible();
});

test('skip link is visually hidden even if external CSS is stale or unavailable', async ({ page }) => {
  await page.route('**/static/assets/app.css', (route) => route.abort());

  await page.goto('/');

  await expectSkipLinkBox(page, 1, 1);

  await page.keyboard.press('Tab');
  const focusedBox = await page.getByRole('link', { name: 'Skip to content' }).boundingBox();

  expect(focusedBox).not.toBeNull();
  expect(focusedBox?.width).toBeGreaterThan(40);
  expect(focusedBox?.height).toBeGreaterThan(20);
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('home page renders without JavaScript', async ({ page }) => {
    await page.goto('/');

    const cssResponse = await page.request.get('/static/assets/app.css');
    expect(cssResponse.status()).toBe(200);
    expect(cssResponse.headers()['content-type']).toContain('text/css');

    await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    await expectHomeBuildingBannerVisible(page);
    await expect(page.getByRole('heading', { name: 'Thailand Gift Shop', level: 1 })).toBeVisible();
    await expect(page.getByText('Bangkok gift shop online')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Latest products' })).toBeVisible();
    await expect(page.getByRole('link', { name: /Elephant Cotton Pouches/ })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Cart', exact: true }).first()).toBeVisible();
    await expectExcludedControlsAbsent(page);
  });
});
