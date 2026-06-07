import { expect, test } from '@playwright/test';

const excludedControlNames = ['Cart', 'Checkout', 'Account', 'Search'];

const implementedPages = [
  '/',
  '/products',
  '/products/mango-sticky-rice-kit',
  '/categories',
  '/categories/market-finds',
  '/story',
];

function expectCanonicalPath(pageURL: string, path: string) {
  expect(new URL(pageURL).pathname).toBe(path);
}

test('catalog slug navigation clicks category and product cards to canonical URLs', async ({ page }) => {
  await page.goto('/');

  await page.getByRole('link', { name: /Bangkok Market Finds/ }).click();
  await expect(page).toHaveURL(/\/categories\/market-finds$/);
  expectCanonicalPath(page.url(), '/categories/market-finds');
  await expect(page.getByRole('heading', { name: 'Bangkok Market Finds', level: 1 })).toBeVisible();

  await page.goto('/products');
  await page.getByRole('link', { name: /Mango Sticky Rice Treats/ }).click();
  await expect(page).toHaveURL(/\/products\/mango-sticky-rice-kit$/);
  expectCanonicalPath(page.url(), '/products/mango-sticky-rice-kit');
  await expect(page.getByRole('heading', { name: 'Mango Sticky Rice Treats', level: 1 })).toBeVisible();
});

test('legacy and trailing-slash redirects resolve to canonical URLs', async ({ page }) => {
  const redirects = [
    { from: '/shop', to: '/products' },
    { from: '/about', to: '/story' },
    { from: '/products/', to: '/products' },
    { from: '/categories/', to: '/categories' },
    { from: '/story/', to: '/story' },
  ];

  for (const redirect of redirects) {
    await page.goto(redirect.from);
    expectCanonicalPath(page.url(), redirect.to);
  }
});

test('implemented pages omit cart checkout account and search controls', async ({ page }) => {
  for (const path of implementedPages) {
    await page.goto(path);

    for (const name of excludedControlNames) {
      await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0);
      await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0);
    }
  }
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('catalog pages render without JavaScript', async ({ page }) => {
    await page.goto('/products');
    await expect(page.getByRole('heading', { name: 'Products', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Mango Sticky Rice Treats/ })).toBeVisible();

    await page.goto('/products/mango-sticky-rice-kit');
    await expect(page.getByRole('heading', { name: 'Mango Sticky Rice Treats', level: 1 })).toBeVisible();
    await expect(page.getByText('Coconut cream, sweet rice, mango candy')).toBeVisible();

    await page.goto('/categories');
    await expect(page.getByRole('heading', { name: 'Categories', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Bangkok Market Finds/ })).toBeVisible();

    await page.goto('/categories/market-finds');
    await expect(page.getByRole('heading', { name: 'Bangkok Market Finds', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Mango Sticky Rice Treats/ })).toBeVisible();

    await page.goto('/story');
    await expect(page.getByRole('heading', { name: 'Our Story', level: 1 })).toBeVisible();
    await expect(page.getByText('slug-based links that work without JavaScript')).toBeVisible();
  });
});
